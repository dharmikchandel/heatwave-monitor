// Command risk runs the risk assessment service: it turns heatwave probabilities
// and processed weather into risk tiers with plain-language reasons, then passes
// them to the alert service.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/config"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/engine"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/outbox"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/risk"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/sqlitex"
)

func main() {
	app := httpx.New("risk")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, app); err != nil {
		app.Log.Error("risk service failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, app *httpx.App) error {
	var (
		port   = config.String("PORT", "8084")
		dbPath = config.String("DB_PATH", "data/risk.db")
		alert  = config.String("ALERT_URL", "http://localhost:8085/internal/events")
	)

	db, err := sqlitex.Open(ctx, dbPath, risk.Migrations())
	if err != nil {
		return err
	}
	defer db.Close()
	app.CheckDB(db)

	dispatcher := &outbox.Dispatcher{
		DB:      db,
		Source:  "risk",
		Targets: map[string]string{"alert": alert},
		Client:  httpx.NewClient(5 * time.Second),
		Log:     app.Log,
	}
	svc := &risk.Service{DB: db, Log: app.Log, Retention: config.Duration("ASSESSMENT_RETENTION", 24*time.Hour)}

	app.Metrics.AddCollector(func(w io.Writer) {
		qctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		assessed, rejected, stale := svc.Counts()
		fmt.Fprintf(w, "# HELP risk_events_total Predictions handled, by outcome.\n# TYPE risk_events_total counter\n")
		fmt.Fprintf(w, "risk_events_total{result=\"assessed\"} %d\n", assessed)
		fmt.Fprintf(w, "risk_events_total{result=\"rejected\"} %d\n", rejected)
		fmt.Fprintf(w, "risk_events_total{result=\"stale\"} %d\n", stale)
		counts := svc.LevelCounts()
		fmt.Fprintf(w, "# HELP risk_assessments_total Assessments by alert level.\n# TYPE risk_assessments_total counter\n")
		for _, l := range engine.RiskLevelOrder {
			fmt.Fprintf(w, "risk_assessments_total{level=%q} %d\n", string(l), counts[l])
		}
		if n, err := outbox.Pending(qctx, db); err == nil {
			fmt.Fprintf(w, "# HELP outbox_pending_events Events waiting to be delivered downstream.\n# TYPE outbox_pending_events gauge\noutbox_pending_events %d\n", n)
		}
	})

	(&risk.API{Svc: svc, Dispatcher: dispatcher}).Register(app.Mux)

	go dispatcher.Run(ctx)
	go svc.Run(ctx)

	app.Log.Info("risk service starting", "db", dbPath, "alert_url", alert)
	return app.ListenAndServe(ctx, ":"+port)
}
