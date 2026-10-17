package platformauth

import (
	"crypto/rand"
	"fmt"
	"testing"
	"time"
)

func BenchmarkVerifyPlatformBearer(b *testing.B) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		b.Fatal(err)
	}
	for _, teams := range []int{0, 10, 100} {
		principal := Principal{Subject: "benchmark-user", Role: RoleUser}
		for i := 0; i < teams; i++ {
			principal.Teams = append(principal.Teams, TeamClaim{ID: fmt.Sprintf("team-%d", i), Namespace: fmt.Sprintf("tenant-%d", i), Role: "member"})
		}
		token, err := Sign(secret, principal, time.Hour, []string{AudienceRuntime})
		if err != nil {
			b.Fatal(err)
		}
		for _, audience := range []string{AudienceRuntime, AudienceAnalytics} {
			b.Run(fmt.Sprintf("teams=%d/audience=%s", teams, audience), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					claims, err := Verify(secret, token, audience)
					if audience == AudienceRuntime && (err != nil || claims.Subject != principal.Subject) || audience != AudienceRuntime && err == nil {
						b.Fatalf("incorrect bearer verification: %v", err)
					}
				}
			})
		}
	}
}
