// Registry image push and publish-record calls against the platform API.

package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ImagePublishRecord struct {
	ImageRef    string `json:"image_ref"`
	SourceImage string `json:"source_image,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

// PushRegistryImage uploads a docker save tar and asks the platform API to push
// it to the configured registry from inside the cluster.
func (c *PlatformClient) PushRegistryImage(ctx context.Context, tarPath, target, scope string) error {
	tarPath = strings.TrimSpace(tarPath)
	target = strings.TrimSpace(target)
	if tarPath == "" || target == "" {
		return fmt.Errorf("tar path and target are required")
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	rel, err := url.Parse(c.apiPrefix + "/runtime/registry/push")
	if err != nil {
		return err
	}
	joined := u.ResolveReference(rel)

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()

	go func() {
		var copyErr error
		defer func() {
			_ = writer.Close()
			_ = pw.CloseWithError(copyErr)
		}()
		if copyErr = writer.WriteField("target", target); copyErr != nil {
			return
		}
		if scope = strings.TrimSpace(scope); scope != "" {
			if copyErr = writer.WriteField("scope", scope); copyErr != nil {
				return
			}
		}
		file, err := os.Open(tarPath) // #nosec G304 -- tarPath is a local docker save archive produced by the CLI build step.
		if err != nil {
			copyErr = err
			return
		}
		defer file.Close()
		part, err := writer.CreateFormFile("image_tar", filepath.Base(tarPath))
		if err != nil {
			copyErr = err
			return
		}
		if _, copyErr = io.Copy(part, file); copyErr != nil {
			return
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joined.String(), pr)
	if err != nil {
		return err
	}
	c.setAuthHeaders(req)
	req.Header.Set("content-type", contentType)

	// Covers the upload window (runtime API default 20m) plus the in-cluster
	// push that follows it (up to 10m).
	client := &http.Client{Timeout: 30 * time.Minute}
	if c.http != nil && c.http.Transport != nil {
		client.Transport = c.http.Transport
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return registryPushHTTPError(resp.StatusCode, b)
	}
	return nil
}

// registryPushHTTPError keeps the runtime API's JSON error when one reached
// the CLI, and explains gateway failures where an ingress or proxy replaced it
// with a bare "Bad Gateway" page.
func registryPushHTTPError(status int, body []byte) error {
	err := httpAPIError(status, body)
	var m map[string]any
	if json.Unmarshal(body, &m) == nil && (m["message"] != nil || m["error"] != nil) {
		return err
	}
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%w; a proxy between the CLI and mcp-runtime-api closed the image upload before the runtime API answered. "+
			"Check the mcp-runtime-api logs for POST /api/v1/runtime/registry/push, and make sure every hop allows a long upload: "+
			"mcp-runtime-api honors MCP_REGISTRY_PUSH_UPLOAD_TIMEOUT (default 20m) and the ingress controller read timeout must not be shorter", err)
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w; the image archive is larger than the platform upload limit", err)
	}
	return err
}

func (c *PlatformClient) RecordImagePublish(ctx context.Context, record ImagePublishRecord) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/user/activity/image-publish", "", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := readBody(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpAPIError(resp.StatusCode, b)
	}
	return nil
}
