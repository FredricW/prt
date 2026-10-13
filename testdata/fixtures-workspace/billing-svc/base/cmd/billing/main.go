// Command billing serves the billing API.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"example.com/billing-svc/internal/api"
	"example.com/billing-svc/internal/invoice"
)

func main() {
	addr := os.Getenv("BILLING_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewServer(invoice.NewStore(), time.Now).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("billing listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
