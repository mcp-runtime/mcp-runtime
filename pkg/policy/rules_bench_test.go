package policy

import (
	"fmt"
	"testing"
	"time"
)

// Unlike distinct-subject lookup scaling, every grant here matches the caller.
// The last exact tool rule must be inspected, including explicit-deny cases.
func BenchmarkAuthorizeMatchingRules(b *testing.B) {
	for _, grants := range []int{1, 10, 100} {
		for _, rules := range []int{1, 32, 128} {
			for _, deny := range []bool{false, true} {
				b.Run(fmt.Sprintf("grants=%d/rules=%d/deny=%v", grants, rules, deny), func(b *testing.B) {
					doc := &Document{Policy: &Config{Mode: "allow-list", DefaultDecision: "deny"}, Tools: []Tool{{Name: "target", RequiredTrust: "low", SideEffect: "read"}}}
					for g := 0; g < grants; g++ {
						grant := Grant{Name: fmt.Sprintf("grant-%04d", g), HumanID: "caller", MaxTrust: "high", AllowedSideEffects: []string{"read"}}
						for r := 0; r < rules-1; r++ {
							grant.ToolRules = append(grant.ToolRules, ToolAccess{Name: ToolName(fmt.Sprintf("other-%d", r)), Decision: "allow"})
						}
						decision := "allow"
						if deny && g == grants-1 {
							decision = "deny"
						}
						grant.ToolRules = append(grant.ToolRules, ToolAccess{Name: "target", Decision: decision})
						doc.Grants = append(doc.Grants, grant)
					}
					compiled, err := Compile(doc)
					if err != nil {
						b.Fatal(err)
					}
					request := Request{Identity: Identity{HumanID: "caller"}, RPCMethod: "tools/call", ToolName: "target"}
					now := time.Now()
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						if got := Authorize(compiled, request, now); got.Allowed == deny || deny && got.Reason != "tool_denied" {
							b.Fatal(got)
						}
					}
				})
			}
		}
	}
}
