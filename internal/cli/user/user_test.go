package user

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"mcp-runtime/pkg/platform"
)

func TestDeactivationRequiresValidIDAndExplicitConfirmation(t *testing.T) {
	for _, args := range [][]string{{"deactivate", "not-an-id", "--yes"}, {"deactivate", "8d852676-a996-4cc8-9ad8-206f58f92028"}} {
		called := false
		cmd := newCommand(func(context.Context, string) (platform.UserDeactivationResult, error) {
			called = true
			return platform.UserDeactivationResult{}, nil
		})
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil || called {
			t.Fatalf("args=%v err=%v called=%t", args, err, called)
		}
	}
}

func TestDeactivationPrintsCommittedNonSecretResultAndPropagatesFailure(t *testing.T) {
	id := "8d852676-a996-4cc8-9ad8-206f58f92028"
	for _, fail := range []bool{false, true} {
		cmd := newCommand(func(_ context.Context, got string) (platform.UserDeactivationResult, error) {
			if got != id {
				t.Fatalf("id=%s", got)
			}
			if fail {
				return platform.UserDeactivationResult{}, errors.New("ownership transfer required")
			}
			return platform.UserDeactivationResult{UserID: id, Status: "deactivated", RevokedSessions: 2, RevokedAPIKeys: 3}, nil
		})
		var out bytes.Buffer
		cmd.SilenceUsage = true
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"deactivate", id, "--yes", "--json"})
		err := cmd.Execute()
		if fail {
			if err == nil || out.Len() != 0 {
				t.Fatalf("failure printed success: %s,err=%v", out.String(), err)
			}
		} else if err != nil || !strings.Contains(out.String(), `"revoked_sessions":2`) {
			t.Fatalf("output=%s err=%v", out.String(), err)
		}
	}
}
