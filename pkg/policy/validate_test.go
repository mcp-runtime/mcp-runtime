package policy

import (
	"strings"
	"testing"
)

func TestValidateAcceptsWellFormedDocument(t *testing.T) {
	doc := &Document{
		SchemaVersion: SchemaVersion,
		Server:        Server{Name: "demo", Namespace: "mcp-servers"},
		Auth:          &Auth{IssuerURL: "https://issuer.example.com", Audience: "https://resource.example.com/mcp"},
		Policy:        &Config{Mode: "allow-list", DefaultDecision: "deny"},
		Tools: []Tool{
			{Name: "read-file", RequiredTrust: "low", SideEffect: "read"},
			{Name: "write-file", RequiredTrust: "high", SideEffect: "write"},
		},
		Grants: []Grant{{
			Name:               "dev",
			HumanID:            "user@example.com",
			MaxTrust:           "high",
			AllowedSideEffects: []string{"read", "write"},
			ToolRules:          []ToolAccess{{Name: "read-file", Decision: "allow", RequiredTrust: "low"}},
		}},
		Sessions: []Binding{{Name: "s1", HumanID: "user@example.com", ConsentedTrust: "high"}},
	}
	if err := Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateRejects(t *testing.T) {
	// restamp controls whether to re-run Stamp after the mutation so that the
	// revision reflects the mutated body. Schema-version and revision checks fire
	// before the body digest is verified, so those test cases must NOT restamp.
	cases := []struct {
		name    string
		mutate  func(*Document)
		restamp bool
		wantSub string
	}{
		// Schema version is checked before revision; no restamp needed.
		{"nil schema version", func(d *Document) { d.SchemaVersion = "" }, false, "schema version"},
		{"unsupported schema version", func(d *Document) { d.SchemaVersion = "v999" }, false, "schema version"},
		// Revision checks: clear or corrupt the stamped revision directly.
		{"missing revision", func(d *Document) { d.Revision = "" }, false, "revision is required"},
		{"revision mismatch", func(d *Document) { d.Revision = "sha256:bad" }, false, "revision mismatch"},
		// Body-level checks: restamp after mutation so revision matches the mutated
		// body and the specific field error is what fires.
		{"missing server name", func(d *Document) { d.Server.Name = "" }, true, "server.name"},
		{"auth without OAuth or adapter trust", func(d *Document) { d.Auth = &Auth{} }, true, "OAuth issuer/audience or adapter trust_domain"},
		{"oauth without audience", func(d *Document) {
			d.Auth = &Auth{IssuerURL: "https://issuer.example.com"}
		}, true, "audience"},
		{"oauth audience not a URI", func(d *Document) {
			d.Auth = &Auth{IssuerURL: "https://issuer.example.com", Audience: "mcp-runtime"}
		}, true, "absolute URI"},
		{"oauth audience with fragment", func(d *Document) {
			d.Auth = &Auth{IssuerURL: "https://issuer.example.com", Audience: "https://mcp.example.com/mcp#frag"}
		}, true, "fragment"},
		{"invalid policy mode", func(d *Document) { d.Policy = &Config{Mode: "deny-everything"} }, true, "policy mode"},
		{"invalid default decision", func(d *Document) { d.Policy = &Config{DefaultDecision: "maybe"} }, true, "default decision"},
		{"invalid tool trust", func(d *Document) { d.Tools = []Tool{{Name: "t", RequiredTrust: "ultra"}} }, true, "required_trust"},
		{"invalid tool side effect", func(d *Document) { d.Tools = []Tool{{Name: "t", SideEffect: "explode"}} }, true, "side_effect"},
		{"duplicate tool", func(d *Document) { d.Tools = []Tool{{Name: "t"}, {Name: "t"}} }, true, "duplicate tool"},
		{"empty tool name", func(d *Document) { d.Tools = []Tool{{Name: ""}} }, true, "name is required"},
		{"duplicate grant", func(d *Document) { d.Grants = []Grant{{Name: "g"}, {Name: "g"}} }, true, "duplicate grant"},
		{"invalid grant trust", func(d *Document) { d.Grants = []Grant{{Name: "g", MaxTrust: "extreme"}} }, true, "max_trust"},
		{"invalid allowed side effect", func(d *Document) {
			d.Grants = []Grant{{Name: "g", AllowedSideEffects: []string{"boom"}}}
		}, true, "allowed side effect"},
		{"invalid tool rule decision", func(d *Document) {
			d.Grants = []Grant{{Name: "g", ToolRules: []ToolAccess{{Name: "t", Decision: "perhaps"}}}}
		}, true, "decision"},
		{"duplicate tool rule", func(d *Document) {
			d.Grants = []Grant{{Name: "g", ToolRules: []ToolAccess{{Name: "t"}, {Name: "t"}}}}
		}, true, "duplicate tool rule"},
		{"duplicate session", func(d *Document) { d.Sessions = []Binding{{Name: "s"}, {Name: "s"}} }, true, "duplicate session"},
		{"invalid consented trust", func(d *Document) {
			d.Sessions = []Binding{{Name: "s", ConsentedTrust: "godmode"}}
		}, true, "consented_trust"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := &Document{
				SchemaVersion: SchemaVersion,
				Server:        Server{Name: "demo"},
			}
			if err := Stamp(doc, ""); err != nil {
				t.Fatalf("Stamp() error = %v", err)
			}
			tc.mutate(doc)
			if tc.restamp {
				if err := Stamp(doc, ""); err != nil {
					t.Fatalf("Stamp() after mutation error = %v", err)
				}
			}
			err := Validate(doc)
			if err == nil {
				t.Fatalf("Validate() error = nil, want error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidateAcceptsAdapterTrustWithoutOAuth(t *testing.T) {
	doc := &Document{
		Server: Server{Name: "demo", Namespace: "mcp-servers"},
		Auth:   &Auth{TrustDomain: "example.org"},
	}
	if err := Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if err := Validate(doc); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateNilDocument(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Fatal("Validate(nil) error = nil, want error")
	}
}

func grantExpiryTestDocument(expiresAt string) *Document {
	return &Document{
		Server: Server{Name: "demo", Namespace: "mcp-servers"},
		Policy: &Config{Mode: "allow-list", DefaultDecision: "deny"},
		Tools:  []Tool{{Name: "read-file", RequiredTrust: "low", SideEffect: "read"}},
		Grants: []Grant{{
			Name:               "dev",
			HumanID:            "user@example.com",
			MaxTrust:           "low",
			AllowedSideEffects: []string{"read"},
			ExpiresAt:          expiresAt,
		}},
	}
}

func TestStampSelectsSchemaVersionForGrantExpiry(t *testing.T) {
	plain := grantExpiryTestDocument("")
	if err := Stamp(plain, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if plain.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q without expiring grants", plain.SchemaVersion, SchemaVersion)
	}

	expiring := grantExpiryTestDocument("2030-01-01T00:00:00Z")
	if err := Stamp(expiring, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if expiring.SchemaVersion != SchemaVersionGrantExpiry {
		t.Fatalf("SchemaVersion = %q, want %q with an expiring grant", expiring.SchemaVersion, SchemaVersionGrantExpiry)
	}
	if err := Validate(expiring); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsGrantExpiryUnderV1(t *testing.T) {
	doc := grantExpiryTestDocument("2030-01-01T00:00:00Z")
	if err := Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	// A renderer that forgot to bump the schema must not produce a document
	// that older gateways would read while dropping expires_at.
	doc.SchemaVersion = SchemaVersion
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("Validate() error = %v, want schema version error", err)
	}
}

func TestValidateRejectsInvalidGrantExpiry(t *testing.T) {
	doc := grantExpiryTestDocument("next tuesday")
	if err := Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "expires_at") {
		t.Fatalf("Validate() error = %v, want expires_at error", err)
	}
}
