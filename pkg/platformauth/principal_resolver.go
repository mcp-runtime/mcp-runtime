package platformauth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"mcp-runtime/pkg/serviceutil"
)

// PrincipalForUserID deliberately does not cache positive account state.
func (r *HTTPUserKeyResolver) PrincipalForUserID(ctx context.Context, userID string) (Principal, error) {
	u, err := parseServiceBaseURL(r.BaseURL)
	if err != nil {
		return Principal{}, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/internal/identity/principal"
	u.RawQuery = ""
	u.Fragment = ""
	body, err := json.Marshal(map[string]string{"user_id": userID})
	if err != nil {
		return Principal{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// #nosec G704 -- operator-configured internal service URL validated above.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return Principal{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(r.Token))
	req.Header.Set("Content-Type", "application/json")
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	// #nosec G704 -- same validated internal service endpoint as NewRequest.
	resp, err := serviceutil.DoHTTP(client, req)
	if err != nil {
		return Principal{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Principal{}, sql.ErrNoRows
	}
	if resp.StatusCode != http.StatusOK {
		return Principal{}, errors.New("current principal lookup unavailable")
	}
	var out struct {
		Principal Principal `json:"principal"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return Principal{}, err
	}
	return out.Principal, nil
}
