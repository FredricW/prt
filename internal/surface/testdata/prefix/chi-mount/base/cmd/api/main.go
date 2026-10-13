package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"example.com/app/internal/users"
)

func main() {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Mount("/users", users.Routes())
	})
	http.ListenAndServe(":8080", r)
}
