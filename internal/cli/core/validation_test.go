package core

import (
	"errors"
	"testing"
)

func TestValidateManifestField(t *testing.T) {
	t.Run("trims whitespace", func(t *testing.T) {
		got, err := ValidateManifestField("field", "  value  ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "value" {
			t.Fatalf("expected trimmed value, got %q", got)
		}
	})

	t.Run("rejects empty value", func(t *testing.T) {
		_, err := ValidateManifestField("field", "   ")
		if err == nil || !errors.Is(err, ErrFieldRequired) {
			t.Fatalf("expected ErrFieldRequired, got %v", err)
		}
	})

	t.Run("rejects control characters", func(t *testing.T) {
		_, err := ValidateManifestField("field", "bad\t")
		if err == nil || !errors.Is(err, ErrControlCharsNotAllowed) {
			t.Fatalf("expected ErrControlCharsNotAllowed, got %v", err)
		}
	})
}

func TestValidateK8sNameAndNamespace(t *testing.T) {
	t.Run("returns sanitized values for valid input", func(t *testing.T) {
		name, ns, err := ValidateK8sNameAndNamespace("server name", ErrInvalidServerName, "my-server", "test-ns")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "my-server" || ns != "test-ns" {
			t.Fatalf("unexpected values: name=%q namespace=%q", name, ns)
		}
	})

	t.Run("rejects invalid name with base", func(t *testing.T) {
		_, _, err := ValidateK8sNameAndNamespace("server name", ErrInvalidServerName, "BadName", "test-ns")
		if err == nil || !errors.Is(err, ErrInvalidServerName) {
			t.Fatalf("expected ErrInvalidServerName, got %v", err)
		}
	})

	t.Run("rejects invalid namespace", func(t *testing.T) {
		_, _, err := ValidateK8sNameAndNamespace("server name", ErrInvalidServerName, "my-server", "bad\tns")
		if err == nil {
			t.Fatal("expected error for invalid namespace")
		}
	})

	t.Run("accepts nil base", func(t *testing.T) {
		_, _, err := ValidateK8sNameAndNamespace("resource name", nil, "BadName", "ns")
		if err == nil {
			t.Fatal("expected error for invalid name even with nil base")
		}
	})
}
