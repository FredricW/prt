package billing

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"time"
)

// LegacyExport is the CSV row format the old finance system imported.
//
// Deprecated: finance moved to the ledger API in 2024; no route serves this.
type LegacyExport struct {
	Number  string
	OrgID   string
	Amount  string
	Issued  string
	IsPaid  bool
	Comment string
}

// toLegacy converts an Invoice to the legacy export row.
func toLegacy(inv Invoice) LegacyExport {
	return LegacyExport{
		Number: inv.Number,
		OrgID:  inv.OrgID,
		Amount: fmt.Sprintf("%.2f", float64(inv.AmountCents)/100),
		Issued: inv.IssuedAt.Format("02.01.2006"),
		IsPaid: inv.Status == StatusPaid,
	}
}

// WriteLegacyCSV writes invoices in the legacy finance CSV format.
//
// Deprecated: see LegacyExport.
func WriteLegacyCSV(w io.Writer, invs []Invoice) error {
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	if err := cw.Write([]string{"nr", "org", "amount", "issued", "paid", "comment"}); err != nil {
		return err
	}
	for _, inv := range invs {
		row := toLegacy(inv)
		rec := []string{row.Number, row.OrgID, row.Amount, row.Issued, strconv.FormatBool(row.IsPaid), row.Comment}
		if err := cw.Write(rec); err != nil {
			return fmt.Errorf("legacy csv %s: %w", inv.Number, err)
		}
	}
	cw.Flush()
	return cw.Error()
}

// parseLegacyDate parses the dd.mm.yyyy dates the old system produced.
func parseLegacyDate(s string) (time.Time, error) {
	return time.Parse("02.01.2006", s)
}
