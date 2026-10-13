package users

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetUser(t *testing.T) {
	store := NewMemoryStore()
	created := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	_ = store.Put(User{ID: "u1", Email: "ada@example.com", DisplayName: "Ada", CreatedAt: created})
	srv := httptest.NewServer(Routes(store))
	defer srv.Close()

	tests := []struct {
		name   string
		path   string
		status int
	}{
		{"found", "/u1", http.StatusOK},
		{"missing", "/nope", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
			if tc.status != http.StatusOK {
				return
			}
			var body userResponse
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.DisplayName != "Ada" || body.TeamID != nil {
				t.Fatalf("unexpected body %+v", body)
			}
		})
	}
}
