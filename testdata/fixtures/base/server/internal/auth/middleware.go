package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type ctxKey struct{}

// UserID returns the authenticated user id stored by Middleware.
func UserID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxKey{}).(string)
	return id, ok
}

// Middleware rejects requests without a valid bearer token, except /health.
func Middleware(store *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/health") {
				next.ServeHTTP(w, r)
				return
			}
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok {
				http.Error(w, `{"code":"unauthenticated"}`, http.StatusUnauthorized)
				return
			}
			sess, ok := store.Lookup(token, time.Now())
			if !ok {
				http.Error(w, `{"code":"session_expired"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess.UserID)))
		})
	}
}
