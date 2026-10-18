package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	policypkg "mcp-runtime/pkg/policy"
)

const (
	idempotencyKeyHeader       = "Idempotency-Key"
	idempotencyReplayedHeader  = "Idempotency-Replayed"
	idempotencyKeyMaxLen       = 255
	idempotencyKeyHashAuditLen = 16
)

// idempotencyAudit is the per-exchange idempotency record used for audit
// events. It holds only an outcome enum and a truncated hash, never the key.
type idempotencyAudit struct {
	Outcome string
	KeyHash string
}

// idemStore returns the configured store, creating the bounded in-memory store
// on first use. Tests and future shared stores may set s.idem before serving.
func (s *gatewayServer) idemStore() idempotencyStore {
	s.idemOnce.Do(func() {
		if s.idem == nil {
			s.idem = newIdempotencyStore(defaultIdempotencyLimits, nil)
		}
	})
	return s.idem
}

func validIdempotencyKey(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	key := values[0]
	if key == "" || len(key) > idempotencyKeyMaxLen {
		return "", false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return "", false
		}
	}
	return key, true
}

func hashFields(fields ...string) string {
	h := sha256.New()
	var n [8]byte
	for _, f := range fields {
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalJSON re-encodes JSON with sorted object keys and numbers preserved
// verbatim, so equivalent requests hash identically.
func canonicalJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func (s *gatewayServer) rejectIdempotency(ex *Exchange, status int, reason, outcome string) Result {
	ex.Decision = policypkg.Deny(status, reason, policypkg.ChoosePolicyVersion(ex.Decision.PolicyVersion, s.defaultPolicyVersion))
	ex.Idempotency.Outcome = outcome
	s.metrics.recordIdempotency(s.metricScope(ex.Policy), outcome)
	s.writeDeniedResponse(ex)
	return Reject
}

// idempotentUpstream handles a request that carries an Idempotency-Key header.
// It runs only after authentication and authorization allowed this request, so
// a replay is always preceded by fresh policy, grant and revocation checks.
func (s *gatewayServer) idempotentUpstream(ex *Exchange) Result {
	key, ok := validIdempotencyKey(ex.R.Header.Values(idempotencyKeyHeader))
	if !ok {
		return s.rejectIdempotency(ex, http.StatusBadRequest, "idempotency_key_invalid", "rejected")
	}
	if !ex.Inspection.ToolCall {
		return s.rejectIdempotency(ex, http.StatusBadRequest, "idempotency_unsupported_request", "rejected")
	}
	if ex.Policy == nil || policypkg.UsesDelegatedHeaders(ex.Policy) ||
		(ex.Identity.HumanID == "" && ex.Identity.AgentID == "") {
		return s.rejectIdempotency(ex, http.StatusBadRequest, "idempotency_requires_verified_identity", "rejected")
	}
	uid := strings.TrimSpace(ex.Policy.Server.UID)
	if uid == "" {
		return s.rejectIdempotency(ex, http.StatusServiceUnavailable, "idempotency_unavailable", "rejected")
	}

	scoped := hashFields("idem-scope-v1", ex.Identity.HumanID, ex.Identity.AgentID, ex.Identity.TeamID,
		ex.Identity.SessionID, uid, key)
	reqHash := hashFields("idem-req-v1", key, ex.Inspection.Method, ex.Inspection.ToolName,
		canonicalJSON(ex.Inspection.IDRaw), canonicalJSON(ex.Inspection.ParamsRaw))
	ex.Idempotency.KeyHash = scoped[:idempotencyKeyHashAuditLen]

	store := s.idemStore()
	principal := hashFields("idem-principal-v1", ex.Identity.HumanID, ex.Identity.AgentID, ex.Identity.TeamID)
	entry, status := store.begin(idemScope{
		Principal: principal,
		Session:   hashFields("idem-session-v1", principal, ex.Identity.SessionID),
	}, scoped, reqHash)
	scope := s.metricScope(ex.Policy)
	switch status {
	case idemReplay:
		reply := entry.reply
		ex.Decision = policypkg.Allow("idempotent_replay", policypkg.ChoosePolicyVersion(ex.Decision.PolicyVersion, s.defaultPolicyVersion))
		ex.Idempotency.Outcome = "replay"
		s.metrics.recordIdempotency(scope, "replay")
		if reply.ContentType != "" {
			ex.W.Header().Set("Content-Type", reply.ContentType)
		}
		ex.W.Header().Set(idempotencyReplayedHeader, "true")
		ex.W.WriteHeader(reply.Status)
		_, _ = ex.W.Write(reply.Body)
		return Respond
	case idemConflict:
		return s.rejectIdempotency(ex, http.StatusConflict, "idempotency_key_conflict", "conflict")
	case idemInFlight:
		ex.W.Header().Set("Retry-After", "1")
		return s.rejectIdempotency(ex, http.StatusConflict, "idempotency_in_flight", "in_flight")
	case idemUnknownOutcome:
		return s.rejectIdempotency(ex, http.StatusConflict, "idempotency_outcome_unknown", "unknown_outcome")
	case idemNotReplayable:
		return s.rejectIdempotency(ex, http.StatusConflict, "idempotency_response_not_replayable", "not_replayable")
	case idemCapacity:
		ex.W.Header().Set("Retry-After", "1")
		return s.rejectIdempotency(ex, http.StatusTooManyRequests, "idempotency_capacity", "capacity")
	}

	// This request owns the key. The scoped key is gateway state; upstream
	// never sees it.
	ex.R.Header.Del(idempotencyKeyHeader)
	maxReply := defaultIdempotencyLimits.MaxReply
	if m, ok := store.(interface{ MaxReply() int }); ok {
		maxReply = m.MaxReply()
	}
	capture := &idemCapture{ResponseWriter: ex.W.ResponseWriter, max: maxReply}
	ex.W.ResponseWriter = capture
	finished := false
	defer func() {
		if finished {
			return
		}
		// Abnormal exit (for example the proxy aborting a half-written
		// response): the tool may have run. Never allow re-execution.
		store.tombstone(entry, idemUnknownOutcome)
		ex.Idempotency.Outcome = "unknown_outcome"
		s.metrics.recordIdempotency(scope, "unknown_outcome")
	}()
	result := s.forwardUpstream(ex)
	finished = true

	code := capture.status
	if code == 0 {
		code = http.StatusOK
	}
	switch {
	case !ex.Decision.Allowed:
		// Refused before dispatch (breaker, backpressure, credentials).
		store.release(entry)
		ex.Idempotency.Outcome = "released"
	case code >= 500:
		store.tombstone(entry, idemUnknownOutcome)
		ex.Idempotency.Outcome = "unknown_outcome"
	case code < 200 || code >= 300:
		store.release(entry)
		ex.Idempotency.Outcome = "released"
	case capture.stream || capture.overflow || capture.encoded:
		store.tombstone(entry, idemNotReplayable)
		ex.Idempotency.Outcome = "not_replayable"
	default:
		store.complete(entry, idempotentReply{Status: code, ContentType: capture.header().Get("Content-Type"), Body: capture.body.Bytes()})
		ex.Idempotency.Outcome = "first"
	}
	s.metrics.recordIdempotency(scope, ex.Idempotency.Outcome)
	return result
}

// idemCapture passes the response through to the client unchanged while keeping
// a bounded copy for replay.
type idemCapture struct {
	http.ResponseWriter
	max      int
	status   int
	stream   bool
	encoded  bool
	overflow bool
	body     bytes.Buffer
}

func (c *idemCapture) header() http.Header { return c.ResponseWriter.Header() }

func (c *idemCapture) WriteHeader(code int) {
	if c.status == 0 && code >= 200 {
		c.status = code
		ct := strings.ToLower(strings.TrimSpace(strings.SplitN(c.header().Get("Content-Type"), ";", 2)[0]))
		c.stream = ct == "text/event-stream"
		// A Content-Encoding body is stored compressed and replay would need
		// Vary-aware negotiation; deliver it but do not replay it.
		enc := strings.ToLower(strings.TrimSpace(c.header().Get("Content-Encoding")))
		c.encoded = enc != "" && enc != "identity"
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *idemCapture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	if !c.stream && !c.overflow {
		if c.body.Len()+len(p) > c.max {
			c.overflow = true
			c.body.Reset()
		} else {
			c.body.Write(p)
		}
	}
	return c.ResponseWriter.Write(p)
}

func (c *idemCapture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
