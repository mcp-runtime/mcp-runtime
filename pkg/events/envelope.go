package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrMissingFields = errors.New("missing_fields")

// Envelope is the shared platform event transport contract.
type Envelope struct {
	Timestamp string          `json:"timestamp"`
	TraceID   string          `json:"trace_id,omitempty"`
	Source    string          `json:"source"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

// NewEnvelope builds a transport envelope from a structured payload.
func NewEnvelope(source, eventType string, payload any, now time.Time) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	envelope := Envelope{
		Source:    source,
		EventType: eventType,
		Payload:   raw,
	}
	envelope.EnsureTimestamp(now)
	return envelope, nil
}

// EnsureTimestamp fills an empty timestamp with the provided time.
func (e *Envelope) EnsureTimestamp(now time.Time) {
	if e.Timestamp != "" {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	e.Timestamp = now.UTC().Format(time.RFC3339Nano)
}

// SetTraceID records the trace ID that can be used to link the stored event row
// back to the distributed trace.
func (e *Envelope) SetTraceID(traceID string) {
	if traceID = strings.TrimSpace(traceID); traceID != "" {
		e.TraceID = traceID
	}
}

// Validate checks the required event envelope fields.
func (e Envelope) Validate() error {
	if e.Source == "" || e.EventType == "" || len(e.Payload) == 0 {
		return ErrMissingFields
	}
	if bytes.Equal(bytes.TrimSpace(e.Payload), []byte("null")) {
		return ErrMissingFields
	}
	return nil
}

// TimestampTime parses the transport timestamp, falling back to now on malformed input.
func (e Envelope) TimestampTime(now time.Time) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
		return parsed
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC()
}
