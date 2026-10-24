package credential

import (
	"testing"
	"time"
)

func TestStoreScopesExpiresAndEvicts(t *testing.T) {
	now := time.Now()
	s := &Store{TTL: time.Minute, Max: 2, Now: func() time.Time { return now }}
	s.Put("iss", "u1", "svc", map[string]string{"token": "one"})
	if v, ok := s.Get("iss", "u1", "svc"); !ok || v["token"] != "one" {
		t.Fatalf("get = %v %v", v, ok)
	}
	for name, args := range map[string][3]string{"subject": {"iss", "u2", "svc"}, "service": {"iss", "u1", "x"}, "issuer": {"x", "u1", "svc"}} {
		if _, ok := s.Get(args[0], args[1], args[2]); ok {
			t.Fatalf("other %s resolved", name)
		}
	}
	s.Put("iss", "u2", "svc", map[string]string{"token": "two"})
	s.Put("iss", "u3", "svc", map[string]string{"token": "three"})
	if _, ok := s.Get("iss", "u1", "svc"); ok || s.Len() != 2 {
		t.Fatal("oldest entry was not evicted")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := s.Get("iss", "u3", "svc"); ok {
		t.Fatal("entry outlived its TTL")
	}
	s.Put("iss", "u4", "svc", map[string]string{"token": "four"})
	s.Delete("iss", "u4", "svc")
	if _, ok := s.Get("iss", "u4", "svc"); ok {
		t.Fatal("delete did not remove")
	}
}

func TestStoreCopiesValues(t *testing.T) {
	s := &Store{TTL: time.Minute, Max: 2}
	in := map[string]string{"token": "one"}
	s.Put("i", "u", "s", in)
	in["token"] = "changed"
	v, _ := s.Get("i", "u", "s")
	v["token"] = "mutated"
	if again, _ := s.Get("i", "u", "s"); again["token"] != "one" {
		t.Fatalf("store aliased caller maps: %v", again)
	}
}
