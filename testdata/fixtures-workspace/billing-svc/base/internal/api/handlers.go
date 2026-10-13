// Package api serves the billing HTTP API.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"example.com/billing-svc/internal/invoice"
)

// Server handles the billing endpoints.
type Server struct {
	store *invoice.Store
	now   func() time.Time
	seq   atomic.Int64
}

// NewServer returns a server over store that reads the time from now.
func NewServer(store *invoice.Store, now func() time.Time) *Server {
	return &Server{store: store, now: now}
}

// Routes returns the API's handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /invoices", s.createInvoice)
	mux.HandleFunc("GET /invoices/{id}", s.getInvoice)
	mux.HandleFunc("POST /invoices/{id}/pay", s.payInvoice)
	return mux
}

type createRequest struct {
	CustomerID  string `json:"customer_id"`
	Currency    string `json:"currency"`
	AmountCents int64  `json:"amount_cents"`
}

func (s *Server) createInvoice(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.CustomerID == "" || req.AmountCents <= 0 {
		http.Error(w, "customer_id and a positive amount_cents are required", http.StatusBadRequest)
		return
	}
	id := fmt.Sprintf("inv_%04d", s.seq.Add(1))
	inv := invoice.New(id, req.CustomerID, strings.ToUpper(req.Currency), req.AmountCents, s.now())
	s.store.Put(inv)
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) getInvoice(w http.ResponseWriter, r *http.Request) {
	inv, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) payInvoice(w http.ResponseWriter, r *http.Request) {
	inv, err := s.store.MarkPaid(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, invoice.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
