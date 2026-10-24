package credential

import "testing"

func TestWrapUnwrapBindsIdentityAndService(t *testing.T) {
	b := NewBinder([]byte("0123456789abcdef0123456789abcdef"))
	wrapped := b.Wrap("iss", "alice", "gitlab", "up.stream-1")
	if id, ok := b.Unwrap("iss", "alice", "gitlab", wrapped); !ok || id != "up.stream-1" {
		t.Fatalf("round trip failed: %q %v", id, ok)
	}
	for name, args := range map[string][3]string{
		"other user": {"iss", "bob", "gitlab"}, "other service": {"iss", "alice", "gerrit"}, "other issuer": {"other", "alice", "gitlab"},
	} {
		if _, ok := b.Unwrap(args[0], args[1], args[2], wrapped); ok {
			t.Fatalf("%s accepted", name)
		}
	}
	for _, bad := range []string{"", "plain", "x.", ".tag", wrapped + "x", "up.stream-1.AAAA"} {
		if _, ok := b.Unwrap("iss", "alice", "gitlab", bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, ok := NewBinder([]byte("another-key-another-key-another-k")).Unwrap("iss", "alice", "gitlab", wrapped); ok {
		t.Fatal("accepted a tag from a different key")
	}
	if b.Wrap("iss", "a", "s", "") != "" || b.Wrap("iss", "a", "s", "has space") != "" || b.Wrap("iss", "a", "s", "bad\nid") != "" {
		t.Fatal("unusable upstream ids must not be wrapped")
	}
}
