// Package users serves user records.
package users

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// ErrNotFound is returned when a user id is unknown.
var ErrNotFound = errors.New("users: not found")

// User is the stored user record.
type User struct {
	ID          string
	Email       string
	DisplayName string
	TeamID      string
	CreatedAt   time.Time
}

// Store looks up and updates users.
type Store interface {
	Get(id string) (User, error)
	Put(u User) error
}

// MemoryStore is an in-memory Store for development.
type MemoryStore struct {
	mu    sync.RWMutex
	users map[string]User
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{users: map[string]User{}}
}

// Get returns the user with id.
func (s *MemoryStore) Get(id string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}

// Put stores u.
func (s *MemoryStore) Put(u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = u
	return nil
}

type userResponse struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName string  `json:"displayName"`
	TeamID      *string `json:"teamId"`
	CreatedAt   string  `json:"createdAt"`
}

func toResponse(u User) userResponse {
	r := userResponse{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		CreatedAt:   u.CreatedAt.UTC().Format(time.RFC3339),
	}
	if u.TeamID != "" {
		team := u.TeamID
		r.TeamID = &team
	}
	return r
}

// Routes mounts the user endpoints.
func Routes(store Store) http.Handler {
	r := chi.NewRouter()
	r.Get("/{id}", func(w http.ResponseWriter, req *http.Request) {
		u, err := store.Get(chi.URLParam(req, "id"))
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "no such user")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toResponse(u))
	})
	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}
