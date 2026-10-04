package httpx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// App bundles what every service needs: a mux, logger, metrics and readiness
// checks. Register routes on Mux, then call ListenAndServe.
type App struct {
	Name    string
	Log     *slog.Logger
	Mux     *http.ServeMux
	Metrics *Metrics

	checks map[string]func(context.Context) error
}

// New returns an App for the named service with /healthz, /readyz and /metrics
// already registered.
func New(name string) *App {
	a := &App{
		Name:    name,
		Log:     NewLogger(name),
		Mux:     http.NewServeMux(),
		Metrics: NewMetrics(name),
		checks:  map[string]func(context.Context) error{},
	}
	a.Mux.HandleFunc("GET /healthz", a.healthz)
	a.Mux.HandleFunc("GET /readyz", a.readyz)
	a.Mux.HandleFunc("GET /metrics", a.metrics)
	return a
}

// AddCheck registers a named readiness check; /readyz fails while any check errors.
func (a *App) AddCheck(name string, fn func(context.Context) error) { a.checks[name] = fn }

// CheckDB registers a readiness check that pings db.
func (a *App) CheckDB(db *sql.DB) { a.AddCheck("database", db.PingContext) }

// Handler returns the mux wrapped in the shared middleware.
func (a *App) Handler() http.Handler { return Middleware(a.Log, a.Metrics, a.Mux) }

func (a *App) healthz(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": a.Name})
}

func (a *App) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	names := make([]string, 0, len(a.checks))
	for n := range a.checks {
		names = append(names, n)
	}
	sort.Strings(names)

	results := make(map[string]string, len(names))
	status, label := http.StatusOK, "ready"
	for _, n := range names {
		if err := a.checks[n](ctx); err != nil {
			results[n] = err.Error()
			status, label = http.StatusServiceUnavailable, "unavailable"
		} else {
			results[n] = "ok"
		}
	}
	WriteJSON(w, status, map[string]any{"status": label, "service": a.Name, "checks": results})
}

func (a *App) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	a.Metrics.WriteText(w)
}

// ListenAndServe serves until ctx is cancelled, then drains in-flight requests
// for up to 10 seconds.
func (a *App) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.Log.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	a.Log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
