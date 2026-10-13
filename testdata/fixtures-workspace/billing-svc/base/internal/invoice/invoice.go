// Package invoice holds invoices and the arithmetic on them.
package invoice

import (
	"errors"
	"sync"
	"time"
)

// Status is where an invoice is in its life.
type Status string

// Invoice statuses.
const (
	StatusOpen Status = "open"
	StatusPaid Status = "paid"
)

// PaymentTerms is how long a customer has to pay an invoice.
const PaymentTerms = 30 * 24 * time.Hour

// ErrNotFound is returned for an unknown invoice id.
var ErrNotFound = errors.New("invoice not found")

// Invoice is an amount a customer owes, in minor units of Currency.
type Invoice struct {
	ID          string    `json:"id"`
	CustomerID  string    `json:"customer_id"`
	Currency    string    `json:"currency"`
	AmountCents int64     `json:"amount_cents"`
	IssuedAt    time.Time `json:"issued_at"`
	DueAt       time.Time `json:"due_at"`
	Status      Status    `json:"status"`
}

// New returns an open invoice issued at now and due after PaymentTerms.
func New(id, customerID, currency string, amountCents int64, now time.Time) Invoice {
	return Invoice{
		ID:          id,
		CustomerID:  customerID,
		Currency:    currency,
		AmountCents: amountCents,
		IssuedAt:    now,
		DueAt:       now.Add(PaymentTerms),
		Status:      StatusOpen,
	}
}

// Overdue reports whether inv is still unpaid after its due date.
func (inv Invoice) Overdue(now time.Time) bool {
	return inv.Status == StatusOpen && now.After(inv.DueAt)
}

// Store keeps invoices in memory.
type Store struct {
	mu   sync.Mutex
	byID map[string]Invoice
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{byID: map[string]Invoice{}}
}

// Put adds or replaces an invoice.
func (s *Store) Put(inv Invoice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[inv.ID] = inv
}

// Get returns the invoice with the given id.
func (s *Store) Get(id string) (Invoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.byID[id]
	if !ok {
		return Invoice{}, ErrNotFound
	}
	return inv, nil
}

// MarkPaid records the payment of an invoice and returns it.
func (s *Store) MarkPaid(id string) (Invoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.byID[id]
	if !ok {
		return Invoice{}, ErrNotFound
	}
	inv.Status = StatusPaid
	s.byID[id] = inv
	return inv, nil
}
