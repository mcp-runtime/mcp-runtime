package platform

import (
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
)

// UI sessions are stored in Postgres, so setup does not pin replica counts.
func ensureSessionLocalDeploymentReplicasClientGo(logger *zap.Logger) error {
	return nil
}

func ensureSessionLocalDeploymentReplicas(kubectl core.KubectlRunner, logger *zap.Logger) error {
	return nil
}

func replicaCount(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}
	return *replicas
}

func parseReplicaCount(raw string) (int32, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("negative replica count %d", value)
	}
	return int32(value), nil
}
