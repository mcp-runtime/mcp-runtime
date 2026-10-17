package platformstore

import (
	"context"
	"mcp-runtime/pkg/internalapi"
)

// OverviewCounts reads all totals in one database snapshot. Deleted teams/users,
// retired server sessions, expired sessions and sessions of inactive agents do
// not count as active resources.
func (s *Store) OverviewCounts(ctx context.Context) (internalapi.OverviewCounts, error) {
	var out internalapi.OverviewCounts
	err := s.db.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM teams WHERE deleted_at IS NULL),
 (SELECT COUNT(*) FROM users WHERE deleted_at IS NULL),
 (SELECT COUNT(*) FROM agents a JOIN teams t ON t.id=a.team_id WHERE t.deleted_at IS NULL),
 (SELECT COUNT(*) FROM sessions s WHERE NOT s.revoked
  AND (s.expires_at IS NULL OR s.expires_at > now())
  AND NOT EXISTS (SELECT 1 FROM session_retired_servers r WHERE r.server_uid=s.server_uid)
  AND (s.agent_id='' OR EXISTS (SELECT 1 FROM agents a JOIN teams t ON t.id=a.team_id
    WHERE a.id=s.agent_id AND a.status='active' AND t.deleted_at IS NULL)))`).Scan(&out.Teams, &out.Users, &out.Agents, &out.ActiveSessions)
	return out, err
}
