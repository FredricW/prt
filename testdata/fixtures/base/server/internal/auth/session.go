// Package auth manages bearer-token sessions.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Session is an authenticated user session.
type Session struct {
	Token     string
	UserID    string
	ExpiresAt time.Time
}

// SessionStore keeps sessions in memory.
type SessionStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]Session
}

// NewSessionStore returns a store whose sessions live for ttl.
func NewSessionStore(ttl time.Duration) *SessionStore {
	return &SessionStore{ttl: ttl, sessions: map[string]Session{}}
}

// Create starts a session for userID.
func (s *SessionStore) Create(userID string, now time.Time) (Session, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Session{}, err
	}
	sess := Session{Token: hex.EncodeToString(buf), UserID: userID, ExpiresAt: now.Add(s.ttl)}
	s.mu.Lock()
	s.sessions[sess.Token] = sess
	s.mu.Unlock()
	return sess, nil
}

// Lookup returns the live session for token.
func (s *SessionStore) Lookup(token string, now time.Time) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok || now.After(sess.ExpiresAt) {
		delete(s.sessions, token)
		return Session{}, false
	}
	return sess, true
}
