package credential

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

type storeKey struct{ issuer, subject, service string }

type storeEntry struct {
	key     storeKey
	values  map[string]string
	expires time.Time
	seq     uint64
}

// Store keeps the header values a person submitted at sign-in. Values live only
// in process memory, are bounded in count and lifetime, and are never logged. An
// entry is reachable only by the exact (issuer, subject, service) it was stored
// under, and the subject is a random value minted per sign-in.
type Store struct {
	TTL time.Duration
	Max int
	Now func() time.Time

	mu    sync.Mutex
	items map[storeKey]*storeEntry
	seq   uint64
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// NewSubject returns an unguessable subject for one sign-in.
func NewSubject() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Put stores a copy of values, replacing any earlier entry for the identity.
func (s *Store) Put(issuer, subject, service string, values map[string]string) {
	copied := make(map[string]string, len(values))
	for name, value := range values {
		copied[name] = value
	}
	now := s.now()
	k := storeKey{issuer, subject, service}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[storeKey]*storeEntry{}
	}
	for key, entry := range s.items {
		if !entry.expires.After(now) {
			delete(s.items, key)
		}
	}
	s.seq++
	s.items[k] = &storeEntry{key: k, values: copied, expires: now.Add(s.TTL), seq: s.seq}
	limit := s.Max
	if limit < 1 {
		limit = 1
	}
	for len(s.items) > limit {
		var oldest *storeEntry
		for _, entry := range s.items {
			if oldest == nil || entry.seq < oldest.seq {
				oldest = entry
			}
		}
		delete(s.items, oldest.key)
	}
}

// Get returns a copy of the live values for the identity and service.
func (s *Store) Get(issuer, subject, service string) (map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[storeKey{issuer, subject, service}]
	if !ok {
		return nil, false
	}
	if !entry.expires.After(s.now()) {
		delete(s.items, entry.key)
		return nil, false
	}
	out := make(map[string]string, len(entry.values))
	for name, value := range entry.values {
		out[name] = value
	}
	return out, true
}

// Delete forgets the values for an identity and service.
func (s *Store) Delete(issuer, subject, service string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, storeKey{issuer, subject, service})
}

// Len reports the number of stored entries, expired ones included until swept.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}
