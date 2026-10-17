package internalapi

// OverviewCounts contains complete platform directory totals, never page lengths.
type OverviewCounts struct {
	Teams          int64 `json:"teams"`
	Users          int64 `json:"users"`
	Agents         int64 `json:"agents"`
	ActiveSessions int64 `json:"active_sessions"`
}
