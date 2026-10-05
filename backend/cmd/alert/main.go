// Command alert runs the alert and notification service: it turns risk
// assessments into alert episodes and delivers notifications by webhook or log.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/alert"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/config"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/sqlitex"
)

func main() {
	app := httpx.New("alert")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, app); err != nil {
		app.Log.Error("alert service failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, app *httpx.App) error {
	var (
		port   = config.String("PORT", "8085")
		dbPath = config.String("DB_PATH", "data/alert.db")
	)

	openLevel := engine.RiskLevel(config.String("OPEN_LEVEL", string(engine.Danger)))
	if openLevel.Severity() < engine.Caution.Severity() {
		return fmt.Errorf("OPEN_LEVEL must be one of caution, extreme-caution, danger, extreme-danger; got %q", openLevel)
	}

	db, err := sqlitex.Open(ctx, dbPath, alert.Migrations())
	if err != nil {
		return err
	}
	defer db.Close()
	app.CheckDB(db)

	var allowed []string
	for _, h := range strings.Split(config.String("WEBHOOK_ALLOWED_HOSTS", ""), ",") {
		if h = strings.TrimSpace(h); h != "" {
			allowed = append(allowed, h)
		}
	}

	svc := &alert.Service{
		DB:               db,
		Log:              app.Log,
		OpenLevel:        openLevel,
		ResolveAfter:     config.Duration("RESOLVE_AFTER", time.Hour),
		Cooldown:         config.Duration("COOLDOWN", 2*time.Hour),
		AllowedHosts:     allowed,
		MaxSubscriptions: config.Int("MAX_SUBSCRIPTIONS", 100),
		Retention:        config.Duration("ALERT_RETENTION", 30*24*time.Hour),
	}
	notifier := alert.NewNotifier(svc)
	notifier.MaxAttempts = config.Int("WEBHOOK_MAX_ATTEMPTS", 10)

	// Convenient for demos: make sure alerts show up in the service log even with no subscribers.
	if config.Bool("DEFAULT_LOG_SUBSCRIPTION", false) {
		subs, err := svc.ListSubscriptions(ctx, nil)
		if err != nil {
			return err
		}
		if len(subs) == 0 {
			if _, err := svc.CreateSubscription(ctx, alert.NewSubscription{MinLevel: string(openLevel), Channel: alert.ChannelLog, Label: "default log subscription"}); err != nil {
				return fmt.Errorf("default subscription: %w", err)
			}
			app.Log.Info("created the default log subscription", "min_level", openLevel)
		}
	}

	app.Metrics.AddCollector(func(w io.Writer) {
		qctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		handled, rejected, stale, opened, resolved := svc.Counts()
		fmt.Fprintf(w, "# HELP alert_events_total Risk assessments handled, by outcome.\n# TYPE alert_events_total counter\n")
		fmt.Fprintf(w, "alert_events_total{result=\"handled\"} %d\nalert_events_total{result=\"rejected\"} %d\nalert_events_total{result=\"stale\"} %d\n", handled, rejected, stale)
		fmt.Fprintf(w, "# HELP alert_transitions_total Alert lifecycle transitions since start.\n# TYPE alert_transitions_total counter\n")
		fmt.Fprintf(w, "alert_transitions_total{kind=\"opened\"} %d\nalert_transitions_total{kind=\"resolved\"} %d\n", opened, resolved)
		if n, err := svc.OpenAlertCount(qctx); err == nil {
			fmt.Fprintf(w, "# HELP alerts_open Alerts currently open.\n# TYPE alerts_open gauge\nalerts_open %d\n", n)
		}
		if counts, err := svc.NotificationCounts(qctx); err == nil {
			fmt.Fprintf(w, "# HELP alert_notifications Notifications by delivery status.\n# TYPE alert_notifications gauge\n")
			for _, st := range []string{"pending", "sent", "failed", "cancelled"} {
				fmt.Fprintf(w, "alert_notifications{status=%q} %d\n", st, counts[st])
			}
		}
	})

	(&alert.API{Svc: svc, Notifier: notifier}).Register(app.Mux)

	go notifier.Run(ctx)
	go svc.Run(ctx)

	app.Log.Info("alert service starting", "db", dbPath, "open_level", openLevel,
		"resolve_after", svc.ResolveAfter.String(), "cooldown", svc.Cooldown.String(), "webhook_hosts", allowed)
	return app.ListenAndServe(ctx, ":"+port)
}
