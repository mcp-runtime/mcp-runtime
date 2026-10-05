package clickhouse

import (
	"context"
	"fmt"
	"time"

	"mcp-runtime/pkg/events"
)

// InsertEvents stores a batch of platform event envelopes.
func (c *Client) InsertEvents(ctx context.Context, batch []events.Envelope) error {
	if len(batch) == 0 {
		return nil
	}
	insert, err := c.Conn.PrepareBatch(ctx, "INSERT INTO "+c.DBName+".events (timestamp, trace_id, source, event_type, payload)")
	if err != nil {
		return fmt.Errorf("prepare event batch: %w", err)
	}

	now := time.Now().UTC()
	for _, event := range batch {
		if err := insert.Append(event.TimestampTime(now), event.TraceID, event.Source, event.EventType, string(event.Payload)); err != nil {
			return fmt.Errorf("append event: %w", err)
		}
	}
	if err := insert.Send(); err != nil {
		return fmt.Errorf("send event batch: %w", err)
	}
	return nil
}
