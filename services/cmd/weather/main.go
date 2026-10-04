// Command weather runs the weather data service: it polls a weather source for
// watched locations, stores each observation, and hands it to the processing
// service through the transactional outbox.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // IANA zones available even in minimal container images

	"github.com/dharmikchandel/heatwave-monitor/services/internal/config"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/outbox"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/sqlitex"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/weather"
)

func main() {
	app := httpx.New("weather")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, app); err != nil {
		app.Log.Error("weather service failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, app *httpx.App) error {
	var (
		port         = config.String("PORT", "8081")
		dbPath       = config.String("DB_PATH", "data/weather.db")
		sourceName   = config.String("WEATHER_SOURCE", "openmeteo")
		pollInterval = config.Duration("POLL_INTERVAL", 15*time.Minute)
		pastDays     = config.Int("PAST_DAYS", 3)
		processing   = config.String("PROCESSING_URL", "http://localhost:8082/internal/events")
	)

	db, err := sqlitex.Open(ctx, dbPath, weather.Migrations())
	if err != nil {
		return err
	}
	defer db.Close()
	app.CheckDB(db)

	dispatcher := &outbox.Dispatcher{
		DB:      db,
		Source:  "weather",
		Targets: map[string]string{"processing": processing},
		Client:  httpx.NewClient(5 * time.Second),
		Log:     app.Log,
	}

	svc := &weather.Service{
		DB:           db,
		Dispatcher:   dispatcher,
		Log:          app.Log,
		Retention:    config.Duration("OBS_RETENTION", 24*time.Hour),
		MaxLocations: config.Int("MAX_LOCATIONS", 50),
	}

	switch sourceName {
	case "openmeteo":
		svc.Source = &weather.OpenMeteo{
			BaseURL:  config.String("OPEN_METEO_URL", weather.DefaultOpenMeteoURL),
			Client:   &http.Client{Timeout: 15 * time.Second},
			PastDays: pastDays,
		}
		svc.RequestGap = config.Duration("REQUEST_GAP", 300*time.Millisecond)
	case "simulated":
		scenario, err := weather.ParseScenario(config.String("SIMULATED_SCENARIO", "normal"))
		if err != nil {
			return fmt.Errorf("SIMULATED_SCENARIO: %w", err)
		}
		svc.DefaultScenario = scenario
		svc.Source = &weather.Simulated{
			Scenarios: svc,
			PastDays:  pastDays,
			NullRate:  config.Float("SIMULATED_NULL_RATE", 0),
		}
	default:
		return fmt.Errorf("WEATHER_SOURCE must be \"openmeteo\" or \"simulated\", got %q", sourceName)
	}

	if config.Bool("SEED_LOCATIONS", true) {
		n, err := svc.Seed(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			app.Log.Info("seeded starter locations", "count", n)
		}
	}

	registerMetrics(app, svc)
	(&weather.API{Svc: svc}).Register(app.Mux)

	go dispatcher.Run(ctx)
	go svc.Poll(ctx, pollInterval)

	app.Log.Info("weather service starting", "source", svc.Source.Name(), "poll_interval", pollInterval.String(), "db", dbPath)
	err = app.ListenAndServe(ctx, ":"+port)
	svc.Wait()
	return err
}

func registerMetrics(app *httpx.App, svc *weather.Service) {
	app.Metrics.AddCollector(func(w io.Writer) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		ok, failed := svc.FetchCounts()
		fmt.Fprintf(w, "# HELP weather_fetch_total Weather source fetches by outcome.\n# TYPE weather_fetch_total counter\n")
		fmt.Fprintf(w, "weather_fetch_total{source=%q,result=\"ok\"} %d\n", svc.Source.Name(), ok)
		fmt.Fprintf(w, "weather_fetch_total{source=%q,result=\"error\"} %d\n", svc.Source.Name(), failed)

		if locs, err := svc.ListLocations(ctx); err == nil {
			fmt.Fprintf(w, "# HELP weather_locations Active watched locations.\n# TYPE weather_locations gauge\nweather_locations %d\n", len(locs))
		}
		if n, err := outbox.Pending(ctx, svc.DB); err == nil {
			fmt.Fprintf(w, "# HELP outbox_pending_events Events waiting to be delivered downstream.\n# TYPE outbox_pending_events gauge\noutbox_pending_events %d\n", n)
		}
	})
}
