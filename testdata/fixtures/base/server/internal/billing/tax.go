package billing

import (
	"math"
	"strings"
)

// vatRates are standard VAT rates in basis points by ISO country code.
var vatRates = map[string]int64{
	"DE": 1900,
	"FR": 2000,
	"NL": 2100,
	"NO": 2500,
	"SE": 2500,
}

// TaxCents returns the VAT due on netCents for country, rounded half-up.
// Unknown countries are zero-rated.
func TaxCents(country string, netCents int64) int64 {
	bp, ok := vatRates[strings.ToUpper(country)]
	if !ok {
		return 0
	}
	return (netCents*bp + 5000) / 10000
}

// legacyTaxCents is the float-based calculation used before basis points.
// Deprecated: kept for the 2023 reconciliation, which has been closed.
func legacyTaxCents(country string, netCents int64) int64 {
	rate := float64(vatRates[strings.ToUpper(country)]) / 10000
	return int64(math.Round(float64(netCents) * rate))
}
