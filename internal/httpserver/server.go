package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"wallet-scan/internal/db"
)

// Server exposes internal health, status, capacity, and balance endpoints.
type Server struct {
	Store   *db.Store
	Balance *BalanceService
}

// Handler builds the internal HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		status, err := s.Store.ReadStatus(r.Context())
		if err != nil {
			http.Error(w, "status unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})
	if s.Balance != nil {
		mux.HandleFunc("/v1/capacity", s.Balance.HandleCapacity)
		mux.HandleFunc("/v1/balance", s.Balance.HandleBalance)
	}
	return mux
}

// ListenAndServe starts the internal server until context cancellation.
func (s *Server) ListenAndServe(ctx context.Context, address string) error {
	server := &http.Server{Addr: address, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
