package operator

import (
	"fmt"

	"mcp-runtime/pkg/errx"

	"github.com/go-logr/logr"
)

// Platform errors for operator operations.
var (
	// Reconciliation errors.
	ErrReconcileDeployment = fmt.Errorf("failed to reconcile deployment")
	ErrReconcileService    = fmt.Errorf("failed to reconcile service")
	ErrReconcileIngress    = fmt.Errorf("failed to reconcile ingress")
	ErrUpdateStatus        = fmt.Errorf("failed to update status")
	ErrApplyDefaults       = fmt.Errorf("failed to apply defaults")

	// Validation errors.
	ErrMissingIngressHost = fmt.Errorf("missing ingress host")
	ErrMissingIngressPath = fmt.Errorf("missing ingress path")

	// Resource errors.
	ErrInvalidCPURequest    = fmt.Errorf("invalid CPU request")
	ErrInvalidMemoryRequest = fmt.Errorf("invalid memory request")
	ErrInvalidCPULimit      = fmt.Errorf("invalid CPU limit")
	ErrInvalidMemoryLimit   = fmt.Errorf("invalid memory limit")
)

// wrapOperatorError wraps an error with operator category and structured context.
// This provides rich error context for Elasticsearch/log aggregation systems.
// The context map should include relevant fields like:
//   - "mcpServer": mcpServer.Name
//   - "namespace": mcpServer.Namespace
//   - "resource": "deployment" | "service" | "ingress"
//   - "operation": "create" | "update" | "delete"
func wrapOperatorError(err error, msg string, context map[string]any) error {
	if err == nil {
		return nil
	}
	wrapped := errx.WrapOperator(msg, err)
	if len(context) > 0 {
		wrapped = wrapped.WithContextMap(context)
	}
	return wrapped
}

// newOperatorError creates a new operator error with structured context.
func newOperatorError(msg string, context map[string]any) error {
	err := errx.Operator(msg)
	if len(context) > 0 {
		err = err.WithContextMap(context)
	}
	return err
}

// logOperatorError logs an errx.Error with structured fields using controller-runtime's logger.
// This extracts all context from errx.Error and logs it with structured fields for Elasticsearch:
//   - error.code: "73000"
//   - error.category: "Operator"
//   - error.message: "Failed to reconcile Deployment"
//   - error.context.mcpServer: "my-server"
//   - error.context.namespace: "mcp-servers"
//   - error.context.resource: "deployment"
//
// If the error is not an errx.Error, it falls back to standard error logging.
func logOperatorError(logger logr.Logger, err error, msg string) {
	if err == nil {
		return
	}

	if keysAndValues, ok := errx.LogrKV(err); ok {
		logger.Error(err, msg, keysAndValues...)
	} else {
		// Fallback for non-errx errors
		logger.Error(err, msg)
	}
}
