package platformclient

import (
	"context"
	"fmt"
	"mcp-runtime/pkg/internalapi"
	"net/http"
)

func (c *Client) OverviewCounts(ctx context.Context) (internalapi.OverviewCounts, error) {
	var out internalapi.OverviewCounts
	status, err := c.authorizedJSON(ctx, http.MethodGet, "/internal/operations/counts", nil, &out)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, fmt.Errorf("overview counts: status %d", status)
	}
	return out, nil
}
