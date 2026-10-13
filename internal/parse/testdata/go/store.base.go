package store

import (
	"errors"
	"fmt"
)

// UserStore keeps users in memory.
type UserStore struct {
	users map[string]string
}

// Get returns the user's name.
func (s *UserStore) Get(id string) (string, error) {
	name, ok := s.users[id]
	if !ok {
		return "", errors.New("not found")
	}
	return name, nil
}

func helper(a int) int {
	return a + 1
}

func other(b int) int {
	return b * 2
}

// Describe formats a user for logs.
func Describe(id, name string) string {
	label := name
	if label == "" {
		label = "unknown"
	}
	return fmt.Sprintf("%s (%s)", label, id)
}

// Sum adds the values.
func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}

// Max returns the largest value.
func Max(xs []int) int {
	best := xs[0]
	for _, x := range xs {
		// keep the larger one
		if x > best {
			best = x
		}
	}
	return best
}
