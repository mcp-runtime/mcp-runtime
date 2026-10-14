package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

// gatewaySessionSync holds a complete server-scoped view. Readers copy one
// binding; reloads never mutate an Exchange's authorization inputs.
type gatewaySessionSync struct {
	mu          sync.RWMutex
	server      sessionstate.Server
	baseURL     string
	token       *tokenFileSource
	client      *http.Client
	epoch       string
	sequence    int64
	sessions    map[string]sessionstate.Session
	lastSync    time.Time
	staleWindow time.Duration
}

var errSessionSyncCatchingUp = errors.New("session sync catching up")
var errSessionSyncIdentityInvalid = errors.New("gateway session identity rejected")

func (c *gatewaySessionSync) invalidate() {
	c.mu.Lock()
	c.epoch = ""
	c.lastSync = time.Time{}
	c.sessions = nil
	c.mu.Unlock()
}

func (c *gatewaySessionSync) fresh() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.lastSync.IsZero() && time.Since(c.lastSync) <= c.staleWindow
}

func (c *gatewaySessionSync) getJSON(ctx context.Context, path string, out any) error {
	token, _ := c.token.Token()
	if token == "" {
		return errors.New("gateway session credential is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		c.invalidate()
		return errSessionSyncIdentityInvalid
	}
	if resp.StatusCode == http.StatusGone {
		return sessionstate.ErrReset
	}
	if resp.StatusCode == http.StatusNotFound {
		return sessionstate.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway session sync returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

func (c *gatewaySessionSync) owns(row sessionstate.Session) bool {
	return row.ServerUID == c.server.UID && row.ServerNamespace == c.server.Namespace && row.ServerName == c.server.Name &&
		row.ID == row.Namespace+"/"+string(row.Binding.Name) && string(row.Binding.Namespace) == row.Namespace
}

func (c *gatewaySessionSync) sync(ctx context.Context) error {
	c.mu.RLock()
	epoch, sequence := c.epoch, c.sequence
	c.mu.RUnlock()
	if epoch == "" {
		var snapshot sessionstate.Snapshot
		if err := c.getJSON(ctx, sessionstate.GatewayPath, &snapshot); err != nil {
			return err
		}
		if snapshot.Epoch == "" || snapshot.Sequence < 0 {
			return errors.New("invalid session snapshot cursor")
		}
		sessions := make(map[string]sessionstate.Session, len(snapshot.Sessions))
		for _, row := range snapshot.Sessions {
			if !c.owns(row) {
				return errors.New("session snapshot contains a different server identity")
			}
			sessions[row.ID] = row
		}
		c.mu.Lock()
		c.sessions = sessions
		c.epoch = snapshot.Epoch
		c.sequence = snapshot.Sequence
		c.lastSync = time.Now()
		c.mu.Unlock()
		return nil
	}
	var changes sessionstate.Changes
	path := sessionstate.GatewayPath + "/changes?after=" + strconv.FormatInt(sequence, 10) + "&epoch=" + url.QueryEscape(epoch)
	if err := c.getJSON(ctx, path, &changes); err != nil {
		if errors.Is(err, sessionstate.ErrReset) {
			c.mu.Lock()
			c.epoch = ""
			c.lastSync = time.Time{}
			c.sessions = nil
			c.mu.Unlock()
		}
		return err
	}
	if changes.Epoch != epoch || changes.Sequence < sequence {
		c.mu.Lock()
		c.epoch = ""
		c.lastSync = time.Time{}
		c.sessions = nil
		c.mu.Unlock()
		return sessionstate.ErrReset
	}
	previous := sequence
	for _, change := range changes.Changes {
		if change.Kind != "session" || change.Sequence <= previous || change.Sequence > changes.Sequence {
			return errors.New("invalid session change sequence")
		}
		previous = change.Sequence
		if change.Operation != "upsert" && change.Operation != "delete" {
			return errors.New("invalid session change operation")
		}
		if change.Operation == "upsert" && (change.Session == nil || !c.owns(*change.Session) || change.ID != change.Session.ID) {
			return errors.New("invalid session change identity")
		}
	}
	c.mu.Lock()
	for _, change := range changes.Changes {
		if change.Operation == "delete" {
			delete(c.sessions, change.ID)
		} else {
			c.sessions[change.ID] = *change.Session
		}
	}
	c.sequence = changes.Sequence
	// A partially drained change log must not renew freshness: revocations may
	// still be queued. Drain immediately before the next regular poll.
	if !changes.More {
		c.lastSync = time.Now()
	}
	c.mu.Unlock()
	if changes.More {
		return errSessionSyncCatchingUp
	}
	return nil
}

func (c *gatewaySessionSync) run(ctx context.Context) {
	backoff := sessionstate.PollInterval
	for {
		err := c.sync(ctx)
		wait := sessionstate.PollInterval
		if errors.Is(err, errSessionSyncCatchingUp) {
			wait = 0
		} else if err != nil {
			wait = backoff + time.Duration(rand.Int64N(int64(backoff/4+1)))
			if backoff < 10*time.Second {
				backoff *= 2
			}
		} else {
			backoff = sessionstate.PollInterval
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *gatewaySessionSync) binding(ctx context.Context, namespace, name string) (*policy.Binding, string) {
	if c == nil {
		return nil, "policy_unavailable"
	}
	c.mu.RLock()
	fresh := !c.lastSync.IsZero() && time.Since(c.lastSync) <= c.staleWindow
	row, found := c.sessions[namespace+"/"+name]
	c.mu.RUnlock()
	if !fresh {
		return nil, "policy_unavailable"
	}
	if !found {
		// Enrollment can complete between polls. A scoped one-off lookup avoids
		// waiting for the next update; ordinary calls stay entirely in-process.
		path := sessionstate.GatewayPath + "/" + url.PathEscape(namespace) + "/" + url.PathEscape(name)
		if err := c.getJSON(ctx, path, &row); err != nil {
			if errors.Is(err, sessionstate.ErrNotFound) {
				return nil, "session_not_found"
			}
			return nil, "policy_unavailable"
		}
		if !c.owns(row) {
			return nil, "session_not_found"
		}
	}
	binding := row.Binding
	return &binding, ""
}
