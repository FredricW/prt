// Package billing issues invoices and serves them over HTTP.
package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Status is an invoice's lifecycle state.
type Status string

// Invoice states.
const (
	StatusDraft Status = "draft"
	StatusOpen  Status = "open"
	StatusPaid  Status = "paid"
	StatusVoid  Status = "void"
)

// Invoice is one issued invoice.
type Invoice struct {
	ID          string    `json:"id"`
	Number      string    `json:"number"`
	OrgID       string    `json:"orgId"`
	AmountCents int64     `json:"amountCents"`
	Currency    string    `json:"currency"`
	Status      Status    `json:"status"`
	IssuedAt    time.Time `json:"issuedAt"`
}

// Ledger persists invoices.
type Ledger interface {
	List(orgID string) ([]Invoice, error)
	Save(inv Invoice) error
}

// MemoryLedger is an in-memory Ledger.
type MemoryLedger struct {
	mu       sync.Mutex
	invoices []Invoice
}

// NewMemoryLedger returns an empty ledger.
func NewMemoryLedger() *MemoryLedger { return &MemoryLedger{} }

// List returns the org's invoices, newest first.
func (l *MemoryLedger) List(orgID string) ([]Invoice, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Invoice
	for _, inv := range l.invoices {
		if inv.OrgID == orgID {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssuedAt.After(out[j].IssuedAt) })
	return out, nil
}

// Save appends inv.
func (l *MemoryLedger) Save(inv Invoice) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.invoices = append(l.invoices, inv)
	return nil
}

// Service issues invoices.
type Service struct {
	ledger Ledger
	seq    int
	mu     sync.Mutex
}

// NewService returns a Service backed by ledger.
func NewService(ledger Ledger) *Service { return &Service{ledger: ledger} }

// Issue creates an open invoice for orgID including tax for country.
func (s *Service) Issue(orgID, country string, netCents int64, now time.Time) (Invoice, error) {
	if netCents <= 0 {
		return Invoice{}, errors.New("billing: amount must be positive")
	}
	s.mu.Lock()
	s.seq++
	n := s.seq
	s.mu.Unlock()
	inv := Invoice{
		ID:          fmt.Sprintf("inv_%06d", n),
		Number:      fmt.Sprintf("%d-%04d", now.Year(), n),
		OrgID:       orgID,
		AmountCents: netCents + TaxCents(country, netCents),
		Currency:    "EUR",
		Status:      StatusOpen,
		IssuedAt:    now,
	}
	return inv, s.ledger.Save(inv)
}

// renumberLegacy rewrites pre-2024 invoice numbers to the YYYY-NNNN scheme.
// Deprecated: the migration ran in March 2024; nothing calls this any more.
func renumberLegacy(invs []Invoice) []Invoice {
	out := make([]Invoice, len(invs))
	for i, inv := range invs {
		if inv.IssuedAt.Year() < 2024 {
			inv.Number = fmt.Sprintf("%d-%04d", inv.IssuedAt.Year(), i+1)
		}
		out[i] = inv
	}
	return out
}

// Routes mounts the billing endpoints.
func Routes(s *Service) http.Handler {
	r := chi.NewRouter()
	r.Get("/invoices", func(w http.ResponseWriter, req *http.Request) {
		invs, err := s.ledger.List(req.Header.Get("X-Org-ID"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if st := req.URL.Query().Get("status"); st != "" {
			invs = filterStatus(invs, Status(st))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(invs)
	})
	return r
}

func filterStatus(invs []Invoice, st Status) []Invoice {
	out := invs[:0]
	for _, inv := range invs {
		if inv.Status == st {
			out = append(out, inv)
		}
	}
	return out
}
