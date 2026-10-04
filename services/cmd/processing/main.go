// Command processing runs the data processing service: it consumes raw weather
// observations from the weather service, cleans them and computes heat index,
// daily aggregates and trend, then passes the result to the prediction service.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/services/internal/config"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/outbox"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/processing"
	"github.com/dharmikchandel/heatwave-monitor/services/internal/sqlitex"
)

func main() {
	app := httpx.New("processing")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, app); err != nil {
		app.Log.Error("processing service failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, app *httpx.App) error {
	var (
		port       = config.String("PORT", "8082")
		dbPath     = config.String("DB_PATH", "data/processing.db")
		prediction = config.String("PREDICTION_URL", "http://localhost:8083/internal/events")
	)

	db, err := sqlitex.Open(ctx, dbPath, processing.Migrations())
	if err != nil {
		return err
	}
	defer db.Close()
	app.CheckDB(db)

	dispatcher := &outbox.Dispatcher{
		DB:      db,
		Source:  "processing",
		Targets: map[string]string{"prediction": prediction},
		Client:  httpx.NewClient(5 * time.Second),
		Log:     app.Log,
	}
	svc := &processing.Service{
		DB:        db,
		Log:       app.Log,
		Retention: config.Duration("SNAPSHOT_RETENTION", 24*time.Hour),
	}

	app.Metrics.AddCollector(func(w io.Writer) {
		qctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		processed, rejected, stale := svc.Counts()
		fmt.Fprintf(w, "# HELP processing_events_total Weather observations handled, by outcome.\n# TYPE processing_events_total counter\n")
		fmt.Fprintf(w, "processing_events_total{result=\"processed\"} %d\n", processed)
		fmt.Fprintf(w, "processing_events_total{result=\"rejected\"} %d\n", rejected)
		fmt.Fprintf(w, "processing_events_total{result=\"stale\"} %d\n", stale)
		if n, err := outbox.Pending(qctx, db); err == nil {
			fmt.Fprintf(w, "# HELP outbox_pending_events Events waiting to be delivered downstream.\n# TYPE outbox_pending_events gauge\noutbox_pending_events %d\n", n)
		}
	})

	(&processing.API{Svc: svc, Dispatcher: dispatcher}).Register(app.Mux)

	go dispatcher.Run(ctx)
	go svc.Run(ctx)

	app.Log.Info("processing service starting", "db", dbPath, "prediction_url", prediction)
	return app.ListenAndServe(ctx, ":"+port)
}
