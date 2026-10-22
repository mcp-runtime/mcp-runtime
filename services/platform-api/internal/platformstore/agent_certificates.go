package platformstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"mcp-runtime/pkg/internalapi"
)

// AgentCertificates reads recorded issuance independently of directory/session
// data. humanID is an exact visibility constraint, not a search expression.
func (s *Store) AgentCertificates(ctx context.Context, agentID, humanID string) (internalapi.CertificateActivity, error) {
	until := time.Now().UTC()
	out := internalapi.CertificateActivity{Since: until.Add(-7 * 24 * time.Hour), Until: until, Recent: []internalapi.CertificateIssuance{}}
	rows, err := s.db.QueryContext(ctx, agentCertificateQuery, agentID, out.Since, out.Until, humanID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var issuedAt time.Time
		if err := rows.Scan(&raw, &issuedAt, &out.Recorded); err != nil {
			return out, err
		}
		var item internalapi.CertificateIssuance
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return out, fmt.Errorf("invalid recorded certificate metadata")
		}
		item.IssuedAt = issuedAt
		out.Recent = append(out.Recent, item)
	}
	return out, rows.Err()
}

const agentCertificateQuery = `SELECT message, created_at, count(*) OVER ()
FROM audit_logs
WHERE action = 'adapter.certificate.issued' AND status = 'success'
AND agent_id = $1 AND created_at >= $2 AND created_at < $3
AND ($4 = '' OR user_id::text = $4)
ORDER BY created_at DESC, id DESC LIMIT 100`
