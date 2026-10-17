package policy

import "testing"

func TestCredentialBrokerRequiresNewSchemaAndEnforcedPlatformSession(t *testing.T) {
	doc := &Document{Server: Server{Name: "server", Namespace: "team", UID: "uid"}, CredentialBindings: []string{"upstream"}, Policy: &Config{Mode: "allow-list", DefaultDecision: "deny"}, Session: &Session{Required: true, Store: "platform"}}
	if err := Stamp(doc, ""); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != SchemaVersionCredentialBroker {
		t.Fatal("old gateways could silently ignore credentials")
	}
	if err := Validate(doc); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Document){func(d *Document) { d.Policy = &Config{Mode: "observe", DefaultDecision: "allow"} }, func(d *Document) { d.Session = nil }, func(d *Document) { d.CredentialBindings = []string{"../foreign"} }, func(d *Document) { d.CredentialBindings = []string{"upstream", "upstream"} }} {
		candidate := *doc
		change(&candidate)
		_ = Stamp(&candidate, "")
		if Validate(&candidate) == nil {
			t.Fatal("unsafe credential policy activated")
		}
	}
	doc.SchemaVersion = SchemaVersionGrantMonitor
	doc.Revision, _ = ComputeRevision(doc)
	if Validate(doc) == nil {
		t.Fatal("credential-enabled document accepted under old schema")
	}
}
