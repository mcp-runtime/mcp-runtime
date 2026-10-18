package credentialbroker

import (
	"context"
	"errors"
	"testing"
)

type stubReader struct{}

func (stubReader) Read(context.Context, Source) (Value, error) { return Value{Bytes: []byte("v")}, nil }

var cloudKinds = []string{"gcp", "aws", "azure", "vault"}

func TestOnlyKubernetesSourcesAreAccepted(t *testing.T) {
	source := Source{Provider: "kubernetes", Resource: "upstream-token", Key: "token", Namespace: "team-one", Name: "upstream", TeamID: testTeam}
	if err := source.Validate(); err != nil {
		t.Fatalf("kubernetes source must stay supported: %v", err)
	}
	refs := map[string]string{
		"gcp":   "projects/project-1/secrets/upstream",
		"aws":   "arn:aws:secretsmanager:us-east-1:123456789012:secret:upstream-Abc123",
		"azure": "https://fixture.vault.azure.net/secrets/upstream",
		"vault": "secret/team/upstream",
	}
	for _, kind := range cloudKinds {
		s := Source{Provider: kind, Resource: refs[kind], Namespace: "team-one", Name: "upstream", TeamID: testTeam}
		if kind == "vault" {
			s.Key = "token"
		}
		if err := s.Validate(); !errors.Is(err, ErrProviderNotSupported) {
			t.Errorf("%s source: want ErrProviderNotSupported, got %v", kind, err)
		}
	}
}

func TestRegistryAcceptsOnlyKubernetesReaders(t *testing.T) {
	r := Registry{}
	if err := r.Register("kubernetes", stubReader{}); err != nil {
		t.Fatal(err)
	}
	if r.Reader("kubernetes") == nil {
		t.Fatal("kubernetes reader missing")
	}
	for _, kind := range append(cloudKinds, "missing") {
		if err := r.Register(kind, stubReader{}); err == nil {
			t.Errorf("%s reader was registered", kind)
		}
		// Even a map literal that bypasses Register never yields a cloud reader.
		if (Registry{kind: stubReader{}}).Reader(kind) != nil {
			t.Errorf("%s reader resolved", kind)
		}
	}
}
