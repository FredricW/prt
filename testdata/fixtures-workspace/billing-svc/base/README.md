# billing-svc

Issues invoices to acme customers and records their payments.

    go run ./cmd/billing   # listens on $BILLING_ADDR (default :8081)
    go test ./...

## API

- `POST /invoices` creates an invoice: `{"customer_id", "currency", "amount_cents"}`
- `GET /invoices/{id}` returns one invoice
- `POST /invoices/{id}/pay` marks an invoice paid

Amounts are integers in the currency's minor unit (cents, øre, pence).
Invoices are due 30 days after they are issued.
