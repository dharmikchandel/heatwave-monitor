// Command gateway runs the API gateway: the single public entry point that
// routes requests to the backend services and composes their answers.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dharmikchandel/heatwave-monitor/backend/internal/config"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/gateway"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
)

func main() {
	app := httpx.New("gateway")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, app); err != nil {
		app.Log.Error("gateway failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, app *httpx.App) error {
	var origins []string
	for _, o := range strings.Split(config.String("CORS_ORIGINS", ""), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}

	cfg := gateway.Config{
		WeatherURL:    config.String("WEATHER_BASE_URL", "http://localhost:8081"),
		ProcessingURL: config.String("PROCESSING_BASE_URL", "http://localhost:8082"),
		PredictionURL: config.String("PREDICTION_BASE_URL", "http://localhost:8083"),
		RiskURL:       config.String("RISK_BASE_URL", "http://localhost:8084"),
		AlertURL:      config.String("ALERT_BASE_URL", "http://localhost:8085"),

		UpstreamTimeout: config.Duration("UPSTREAM_TIMEOUT", 3*time.Second),
		ClimateTimeout:  config.Duration("CLIMATE_TIMEOUT", 5*time.Second),
		BreakerFailures: config.Int("BREAKER_FAILURES", 5),
		BreakerCooldown: config.Duration("BREAKER_COOLDOWN", 10*time.Second),

		RateLimit:      gateway.RateConfig{PerMinute: config.Float("RATE_LIMIT_PER_MIN", 240), Burst: config.Int("RATE_LIMIT_BURST", 60)},
		WriteRateLimit: gateway.RateConfig{PerMinute: config.Float("WRITE_RATE_LIMIT_PER_MIN", 10), Burst: config.Int("WRITE_RATE_LIMIT_BURST", 5)},
		TrustProxy:     config.Bool("TRUST_PROXY", false),
		CORSOrigins:    origins,
		AdminToken:     os.Getenv("ADMIN_TOKEN"),
	}

	gw := gateway.New(cfg, app.Log)
	app.Wrap = gw.Wrap
	gw.Register(app.Mux)
	app.Metrics.AddCollector(func(w io.Writer) { gw.Collector(w) })

	if cfg.AdminToken == "" {
		app.Log.Warn("ADMIN_TOKEN is not set: admin endpoints (delete, refresh, simulation, subscriptions, acknowledge) are disabled")
	}
	if cfg.TrustProxy {
		app.Log.Info("trusting X-Forwarded-For for client addresses; only enable this behind a proxy you control")
	}
	app.Log.Info("gateway starting", "cors_origins", origins, "rate_limit_per_min", cfg.RateLimit.PerMinute)
	return app.ListenAndServe(ctx, ":"+config.String("PORT", "8080"))
}
