// Package session binds upstream MCP session ids to the authenticated user
// and service.
//
// The gateway returns "<upstream-id>.<tag>" to the client, where tag is an
// HMAC over (issuer, subject, service, upstream-id) under a deployment
// secret. A later request is accepted only if the tag verifies for its own
// verified identity and service, so a session id cannot be replayed by another
// user or against another service. The binding is stateless: it survives
// restarts and works across replicas that share the key.
package credential

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

const maxUpstreamID = 512

// Binder wraps and unwraps session ids.
type Binder struct{ key []byte }

// NewBinder returns a Binder using key.
func NewBinder(key []byte) *Binder { return &Binder{key: key} }

func (b *Binder) tag(issuer, subject, service, id string) string {
	mac := hmac.New(sha256.New, b.key)
	mac.Write([]byte(strings.Join([]string{issuer, subject, service, id}, "\x00")))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:24])
}

// Wrap returns the id to give the client, or "" if the upstream id is unusable.
func (b *Binder) Wrap(issuer, subject, service, upstreamID string) string {
	if upstreamID == "" || len(upstreamID) > maxUpstreamID {
		return ""
	}
	for i := 0; i < len(upstreamID); i++ {
		if upstreamID[i] < 0x21 || upstreamID[i] > 0x7E {
			return ""
		}
	}
	return upstreamID + "." + b.tag(issuer, subject, service, upstreamID)
}

// Unwrap returns the upstream id when presented belongs to this identity and
// service, or "" and false.
func (b *Binder) Unwrap(issuer, subject, service, presented string) (string, bool) {
	i := strings.LastIndexByte(presented, '.')
	if i <= 0 || i == len(presented)-1 {
		return "", false
	}
	id, tag := presented[:i], presented[i+1:]
	if hmac.Equal([]byte(tag), []byte(b.tag(issuer, subject, service, id))) {
		return id, true
	}
	return "", false
}
