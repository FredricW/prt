// Command api serves the portal HTTP API.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"example.com/portal/server/internal/auth"
	"example.com/portal/server/internal/billing"
	"example.com/portal/server/internal/users"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	store := users.NewMemoryStore()
	invoices := billing.NewService(billing.NewMemoryLedger())

	r := chi.NewRouter()
	r.Use(auth.Middleware(auth.NewSessionStore(12 * time.Hour)))
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","version":"0.14.2"}`))
		})
		r.Mount("/users", users.Routes(store))
		r.Mount("/billing", billing.Routes(invoices))
	})

	addr := os.Getenv("PORTAL_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{Addr: addr, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	logger.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
