package platformapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDeactivateUserCallsSupportedAuthenticatedAccountEndpoint(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusConflict} {
		client := &PlatformClient{baseURL: "https://platform.example.test", token: "fixture-key", apiPrefix: "/api/v1", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/users/user-id/deactivate" || r.Header.Get("x-api-key") != "fixture-key" {
				t.Fatalf("unexpected lifecycle request %s %s", r.Method, r.URL.Path)
			}
			body := `{"user_id":"user-id","status":"deactivated","revoked_sessions":2}`
			if status != http.StatusOK {
				body = `{"error":"forbidden","message":"authority transfer required"}`
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
		out, err := client.DeactivateUser(context.Background(), " user-id ")
		if status == http.StatusOK {
			if err != nil || out.RevokedSessions != 2 {
				t.Fatalf("result=%+v err=%v", out, err)
			}
		} else if err == nil {
			t.Fatalf("status %d treated as success", status)
		}
	}
}
