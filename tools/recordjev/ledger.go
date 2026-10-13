package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// ledgerHeader starts a new SPEND.md.
const ledgerHeader = `# Real model spend

Ledger of billed model calls made to record fixtures (DECISIONS "Real model
spend capped at $5" and "M1 Jev recordings flow"). Rows are appended by
` + "`tools/recordjev`" + ` after every live run; Jev recordings stop at $1.00
cumulative.

| When (UTC) | Service | Calls | Run cost | Cumulative | Recordings written |
| --- | --- | --- | --- | --- | --- |
`

// ledgerRow is one appended row.
type ledgerRow struct {
	At         time.Time
	Service    string
	Calls      int
	Cost       float64
	Cumulative float64
	Written    int
}

// String renders the row as a Markdown table line.
func (r ledgerRow) String() string {
	return fmt.Sprintf("| %s | %s | %d | $%.4f | $%.4f | %d |\n",
		r.At.UTC().Format(time.RFC3339), r.Service, r.Calls, r.Cost, r.Cumulative, r.Written)
}

// ledgerSpent returns the summed run cost of a service's rows in a ledger
// file (0 when the file does not exist). Rows that do not parse are
// errors: a ledger the tool cannot read must not silently reset the budget.
func ledgerSpent(path, service string) (float64, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read ledger: %w", err)
	}
	return parseLedger(string(b), service)
}

// parseLedger sums the "Run cost" column of rows whose Service is service.
func parseLedger(text, service string) (float64, error) {
	total := 0.0
	for i, line := range strings.Split(text, "\n") {
		cells := tableCells(line)
		if len(cells) < 4 || cells[1] != service {
			continue
		}
		cost, err := strconv.ParseFloat(strings.TrimPrefix(cells[3], "$"), 64)
		if err != nil {
			return 0, fmt.Errorf("ledger line %d: bad run cost %q: %w", i+1, cells[3], err)
		}
		total += cost
	}
	return total, nil
}

// tableCells splits a Markdown table row into trimmed cells (nil if the
// line is not a table row).
func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// appendLedger appends a row, creating the file with a header if missing.
func appendLedger(path string, r ledgerRow) error {
	_, err := os.Stat(path)
	header := errors.Is(err, fs.ErrNotExist)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	if header {
		if _, err := f.WriteString(ledgerHeader); err != nil {
			_ = f.Close()
			return fmt.Errorf("write ledger header: %w", err)
		}
	}
	if _, err := f.WriteString(r.String()); err != nil {
		_ = f.Close()
		return fmt.Errorf("write ledger row: %w", err)
	}
	return f.Close()
}
