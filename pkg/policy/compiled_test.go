package policy

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
)

func TestCompiledAuthorizationMatchesLinear(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewPCG(319, 307))
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	principals := []string{"", "alice", "bob", "carol"}
	expiries := []string{"", now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), "invalid"}
	trusts := []string{"", "low", "medium", "high", "invalid"}
	for variant := 0; variant < 100; variant++ {
		doc := &Document{Policy: &Config{Mode: "allow-list", DefaultDecision: "deny", PolicyVersion: "v1"}, Session: &Session{Required: variant%2 == 0}}
		if variant%7 == 0 {
			doc.Session.Store = "platform"
		}
		if variant%11 == 0 {
			doc.Policy.Mode = "observe"
		}
		if variant%13 == 0 {
			doc.Policy.DefaultDecision = "allow"
		}
		for i := 0; i < 4; i++ {
			doc.Tools = append(doc.Tools, Tool{Name: ToolName(fmt.Sprintf("tool-%d", i)), RequiredTrust: trusts[random.IntN(len(trusts))], SideEffect: []string{"", "read", "write", "destructive"}[random.IntN(4)]})
		}
		for i := 0; i < 30; i++ {
			doc.Grants = append(doc.Grants, Grant{Name: fmt.Sprintf("grant-%02d", i), Namespace: Namespace(principals[random.IntN(4)]), HumanID: HumanID(principals[random.IntN(4)]), AgentID: AgentID(principals[random.IntN(4)]), TeamID: TeamID(principals[random.IntN(4)]), MaxTrust: trusts[random.IntN(5)], AllowedSideEffects: []string{"read", "write"}, Disabled: random.IntN(5) == 0, ExpiresAt: expiries[random.IntN(4)], Mode: []string{"", "monitor"}[random.IntN(2)], ToolRules: []ToolAccess{{Name: ToolName(fmt.Sprintf("tool-%d", random.IntN(4))), Decision: []string{"allow", "deny"}[random.IntN(2)]}}})
		}
		for i := 0; i < 20; i++ {
			doc.Sessions = append(doc.Sessions, Binding{Name: SessionID(fmt.Sprintf("session-%d", i%4)), HumanID: HumanID(principals[random.IntN(4)]), AgentID: AgentID(principals[random.IntN(4)]), TeamID: TeamID(principals[random.IntN(4)]), ConsentedTrust: trusts[random.IntN(5)], Revoked: random.IntN(5) == 0, ExpiresAt: expiries[random.IntN(4)]})
		}
		compiled, err := Compile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 100; i++ {
			request := Request{Identity: Identity{HumanID: HumanID(principals[random.IntN(4)]), AgentID: AgentID(principals[random.IntN(4)]), TeamID: TeamID(principals[random.IntN(4)])}, RPCMethod: "tools/call", ToolName: ToolName(fmt.Sprintf("tool-%d", random.IntN(5)))}
			if i%3 != 0 {
				request.Identity.SessionID = SessionID(fmt.Sprintf("session-%d", random.IntN(5)))
			}
			if i%7 == 0 {
				session := doc.Sessions[random.IntN(len(doc.Sessions))]
				request.Session = &session
			}
			if got, want := Authorize(compiled, request, now), Authorize(doc, request, now); got != want {
				t.Fatalf("variant=%d request=%+v: indexed=%+v linear=%+v", variant, request, got, want)
			}
		}
	}
}

func TestCompileOwnsNestedPolicyData(t *testing.T) {
	doc := &Document{Policy: &Config{Mode: "allow-list"}, Auth: &Auth{Headers: []string{"X-Credential"}}, Tools: []Tool{{Name: "tool", Labels: map[string]string{"owner": "original"}}}, Grants: []Grant{{HumanID: "alice", AllowedSideEffects: []string{"read"}, ToolRules: []ToolAccess{{Name: "tool", Decision: "allow"}}}}}
	compiled, err := Compile(doc)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(compiled)
	doc.Policy.Mode = "observe"
	doc.Auth.Headers[0] = "X-Changed"
	doc.Tools[0].Labels["owner"] = "changed"
	doc.Grants[0].AllowedSideEffects[0] = "destructive"
	doc.Grants[0].ToolRules[0].Decision = "deny"
	after, _ := json.Marshal(compiled)
	if string(before) != string(after) {
		t.Fatal("published snapshot changed when source data mutated")
	}
}

func benchmarkPolicy(size int) (*Document, Request) {
	doc := &Document{Policy: &Config{Mode: "allow-list", DefaultDecision: "deny", PolicyVersion: "v1"}, Session: &Session{Required: true}}
	for i := 0; i < size; i++ {
		subject := HumanID(fmt.Sprintf("human-%d", i))
		tool := ToolName(fmt.Sprintf("tool-%d", i))
		doc.Sessions = append(doc.Sessions, Binding{Name: SessionID(fmt.Sprintf("session-%d", i)), HumanID: subject, ConsentedTrust: "high"})
		doc.Tools = append(doc.Tools, Tool{Name: tool, RequiredTrust: "low", SideEffect: "read"})
		doc.Grants = append(doc.Grants, Grant{Name: fmt.Sprintf("grant-%d", i), HumanID: subject, MaxTrust: "high", AllowedSideEffects: []string{"read"}})
	}
	return doc, Request{Identity: Identity{HumanID: HumanID(fmt.Sprintf("human-%d", size-1)), SessionID: SessionID(fmt.Sprintf("session-%d", size-1))}, RPCMethod: "tools/call", ToolName: ToolName(fmt.Sprintf("tool-%d", size-1))}
}

func BenchmarkAuthorizeScaling(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		doc, request := benchmarkPolicy(size)
		compiled, err := Compile(doc)
		if err != nil {
			b.Fatal(err)
		}
		for name, snapshot := range map[string]*Document{"linear": doc, "indexed": compiled} {
			for _, missing := range []bool{false, true} {
				request := request
				if missing {
					request.Identity.SessionID = "missing"
				}
				b.Run(fmt.Sprintf("%s/%d/missing=%v", name, size, missing), func(b *testing.B) {
					now := time.Now()
					b.ReportAllocs()
					for b.Loop() {
						decision := Authorize(snapshot, request, now)
						if decision.Allowed == missing {
							b.Fatal(decision)
						}
					}
				})
			}
		}
	}
}

func BenchmarkSessionLookupScaling(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		doc, request := benchmarkPolicy(size)
		compiled, err := Compile(doc)
		if err != nil {
			b.Fatal(err)
		}
		for name, snapshot := range map[string]*Document{"linear": doc, "indexed": compiled} {
			for _, missing := range []bool{false, true} {
				request := request
				if missing {
					request.Identity.SessionID = "missing"
				}
				b.Run(fmt.Sprintf("%s/%d/missing=%v", name, size, missing), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if _, found := findPolicySession(snapshot, request); found == missing {
							b.Fatal("incorrect session lookup")
						}
					}
				})
			}
		}
	}
}
