package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	cb "mcp-runtime/pkg/credentialbroker"
)

func (c *PlatformClient) CredentialMetadata(ctx context.Context, method, namespace, kind, name string, body json.RawMessage, revision int64) (json.RawMessage, error) {
	if !cb.NameValid(namespace) || (kind != "sources" && kind != "bindings") || (name != "" && !cb.NameValid(name)) {
		return nil, fmt.Errorf("valid credential namespace, kind and name required")
	}
	path := "/credentials/" + url.PathEscape(namespace) + "/" + kind
	if name != "" {
		path += "/" + url.PathEscape(name)
	}
	query := ""
	if method == http.MethodDelete {
		query = "revision=" + strconv.FormatInt(revision, 10)
	}
	resp, err := c.do(ctx, method, path, query, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpAPIError(resp.StatusCode, raw)
	}
	if resp.StatusCode == http.StatusNoContent {
		return json.RawMessage(`{"deleted":true}`), nil
	}
	return raw, nil
}
