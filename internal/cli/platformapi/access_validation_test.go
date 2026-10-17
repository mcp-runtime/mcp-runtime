package platformapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"mcp-runtime/pkg/access"
)

func TestAccessApplyRejectsInvalidManifestsBeforeHTTP(t *testing.T) {
	requests := 0
	client := &PlatformClient{baseURL: "https://platform.invalid", apiPrefix: "/api/v1", http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		t.Fatal("invalid request reached HTTP transport")
		return nil, nil
	})}}
	grant := grantAPIBody{Name: "grant-a", ServerRef: access.ServerReference{Name: "server-a"}, Subject: access.SubjectRef{TeamID: "team-a"}, AllowedSideEffects: []access.ToolSideEffect{"delete"}}
	if err := client.postGrant(context.Background(), grant); err == nil || err.Error() != "allowedSideEffects[0] must be read, write, or destructive" {
		t.Fatalf("CLI validation: %v", err)
	}
	session := sessionAPIBody{Name: "session-a", ServerRef: access.ServerReference{Name: "server-a"}}
	if err := client.postSession(context.Background(), session); err == nil || err.Error() != "one of subject.humanID, subject.agentID, or subject.teamID is required" {
		t.Fatalf("CLI session validation: %v", err)
	}
	if requests != 0 {
		t.Fatalf("HTTP requests=%d", requests)
	}
}

func TestAccessApplyValidationLeavesPayloadAndExplicitStateUnchanged(t *testing.T) {
	no := false
	body := grantAPIBody{Name: " grant-a ", ServerRef: access.ServerReference{Name: " server-a "}, Subject: access.SubjectRef{TeamID: " team-a "}, AllowedSideEffects: []access.ToolSideEffect{" read "}, ToolRules: []access.ToolRule{{Name: " ping ", Decision: " allow "}}, Disabled: &no}
	expected, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &PlatformClient{baseURL: "https://platform.invalid", apiPrefix: "/api/v1", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		actual, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var got, want map[string]any
		if err := json.Unmarshal(actual, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(expected, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("validation rewrote payload: %s,want %s", actual, expected)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	if err := client.postGrant(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(expected) || calls != 1 {
		t.Fatalf("caller payload mutated or not submitted: %s,calls=%d", after, calls)
	}
}
