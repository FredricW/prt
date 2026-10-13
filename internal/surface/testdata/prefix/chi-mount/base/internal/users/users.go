package users

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns the user endpoints.
func Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/{id}", get)
	return r
}

func get(w http.ResponseWriter, r *http.Request) {}
