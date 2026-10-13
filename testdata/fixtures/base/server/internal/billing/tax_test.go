package billing

import "testing"

func TestTaxCents(t *testing.T) {
	tests := []struct {
		country string
		net     int64
		want    int64
	}{
		{"NO", 10000, 2500},
		{"de", 999, 190},
		{"US", 10000, 0},
	}
	for _, tc := range tests {
		if got := TaxCents(tc.country, tc.net); got != tc.want {
			t.Errorf("TaxCents(%q, %d) = %d, want %d", tc.country, tc.net, got, tc.want)
		}
	}
}
