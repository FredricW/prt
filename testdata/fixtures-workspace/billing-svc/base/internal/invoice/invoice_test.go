package invoice

import (
	"errors"
	"testing"
	"time"
)

var issued = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestNewIsDueAfterPaymentTerms(t *testing.T) {
	inv := New("inv_0001", "cus_1", "EUR", 12_500, issued)
	if want := issued.Add(PaymentTerms); !inv.DueAt.Equal(want) {
		t.Errorf("DueAt = %v, want %v", inv.DueAt, want)
	}
	if inv.Status != StatusOpen {
		t.Errorf("Status = %q, want open", inv.Status)
	}
}

func TestOverdue(t *testing.T) {
	inv := New("inv_0001", "cus_1", "EUR", 12_500, issued)
	paid := inv
	paid.Status = StatusPaid
	for _, c := range []struct {
		name string
		inv  Invoice
		now  time.Time
		want bool
	}{
		{"before due", inv, inv.DueAt.Add(-time.Hour), false},
		{"on due date", inv, inv.DueAt, false},
		{"after due", inv, inv.DueAt.Add(time.Hour), true},
		{"paid late", paid, inv.DueAt.Add(time.Hour), false},
	} {
		if got := c.inv.Overdue(c.now); got != c.want {
			t.Errorf("%s: Overdue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStoreMarkPaid(t *testing.T) {
	s := NewStore()
	s.Put(New("inv_0001", "cus_1", "EUR", 12_500, issued))
	inv, err := s.MarkPaid("inv_0001")
	if err != nil || inv.Status != StatusPaid {
		t.Fatalf("MarkPaid = %+v, %v", inv, err)
	}
	if _, err := s.MarkPaid("inv_9999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkPaid(unknown) err = %v, want ErrNotFound", err)
	}
}
