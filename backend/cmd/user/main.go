// Command user runs the user service: accounts, login sessions, watchlists and admin
// account management. It is reached only through the API gateway.
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
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/httpx"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/sqlitex"
	"github.com/dharmikchandel/heatwave-monitor/backend/internal/user"
)

func main() {
	app := httpx.New("user")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, app); err != nil {
		app.Log.Error("user service failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, app *httpx.App) error {
	db, err := sqlitex.Open(ctx, config.String("DB_PATH", "data/user.db"), user.Migrations())
	if err != nil {
		return err
	}
	defer db.Close()
	app.CheckDB(db)

	svc := &user.Service{
		DB:           db,
		Log:          app.Log,
		Hasher:       user.NewHasher(config.Int("HASH_CONCURRENCY", 4)),
		SessionTTL:   config.Duration("SESSION_TTL", 7*24*time.Hour),
		MaxWatchlist: config.Int("MAX_WATCHLIST", 50),
	}

	// The first administrator comes from configuration, and only when none exists yet.
	adminEmail, adminPassword := os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD")
	created, err := svc.SeedAdmin(ctx, adminEmail, adminPassword)
	if err != nil {
		return fmt.Errorf("seed administrator: %w", err)
	}
	switch {
	case created:
		app.Log.Info("created the administrator account", "email", adminEmail)
	case adminPassword == "":
		app.Log.Warn("ADMIN_PASSWORD is not set: no administrator is created (an existing one is unaffected)")
	}

	app.Metrics.AddCollector(func(w io.Writer) {
		qctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if users, sessions, err := svc.Counts(qctx); err == nil {
			fmt.Fprintf(w, "# HELP user_accounts Registered accounts.\n# TYPE user_accounts gauge\nuser_accounts %d\n", users)
			fmt.Fprintf(w, "# HELP user_sessions Live login sessions.\n# TYPE user_sessions gauge\nuser_sessions %d\n", sessions)
		}
	})

	(&user.API{Svc: svc}).Register(app.Mux)
	go svc.Run(ctx)

	app.Log.Info("user service starting")
	return app.ListenAndServe(ctx, ":"+config.String("PORT", "8086"))
}
