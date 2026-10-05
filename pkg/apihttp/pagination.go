package apihttp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 200
)

// Meta accompanies list responses.
type Meta struct {
	Limit      int    `json:"limit"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// CursorPayload is the opaque cursor encoding (base64url JSON).
type CursorPayload struct {
	Offset int `json:"offset"`
}

// ParseLimit reads limit with defaults and strict bounds. Invalid values return an *Error.
func ParseLimit(r *http.Request, defaultLimit, maxLimit int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		if defaultLimit <= 0 {
			defaultLimit = DefaultListLimit
		}
		return defaultLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, BadRequest(CodeInvalidQueryParam, "limit must be an integer")
	}
	if maxLimit <= 0 {
		maxLimit = MaxListLimit
	}
	if value < 1 || value > maxLimit {
		return 0, BadRequest(CodeInvalidQueryParam, fmt.Sprintf("limit must be between 1 and %d", maxLimit))
	}
	return value, nil
}

// ParseCursor decodes an opaque cursor or returns offset 0 when absent.
func ParseCursor(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, BadRequest(CodeInvalidQueryParam, "cursor is invalid")
	}
	var payload CursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil || payload.Offset < 0 {
		return 0, BadRequest(CodeInvalidQueryParam, "cursor is invalid")
	}
	return payload.Offset, nil
}

// EncodeCursor returns the next opaque cursor for a list page.
func EncodeCursor(offset int) string {
	if offset < 0 {
		offset = 0
	}
	raw, _ := json.Marshal(CursorPayload{Offset: offset})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// NextLink builds an absolute or path-only next URL with cursor and limit query params.
func NextLink(r *http.Request, nextCursor string, limit int) string {
	if strings.TrimSpace(nextCursor) == "" {
		return ""
	}
	q := url.Values{}
	q.Set("cursor", nextCursor)
	q.Set("limit", strconv.Itoa(limit))
	if r != nil && r.URL != nil {
		path := r.URL.Path
		if path == "" {
			path = "/"
		}
		return path + "?" + q.Encode()
	}
	return "?" + q.Encode()
}

// ListMeta builds pagination metadata for handlers that fetch exactly `limit`
// rows. HasMore is therefore optimistic on exact-fit pages unless the caller
// uses a limit+1 platform query upstream.
func ListMeta(limit, offset, returned int) Meta {
	hasMore := returned >= limit && limit > 0
	meta := Meta{Limit: limit, HasMore: hasMore}
	if hasMore {
		meta.NextCursor = EncodeCursor(offset + returned)
	}
	return meta
}
