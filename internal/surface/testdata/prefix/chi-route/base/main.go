package main

import "github.com/go-chi/chi/v5"

func main() {
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Route("/v2", func(r chi.Router) {
			r.Get("/a", a)
			r.Get("/b", b)
		})
		r.Get("/c", c)
	})
}
