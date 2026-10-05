package platform

import (
	"fmt"
	"os"
	"strings"
)

// workloadIssuerApprovalAckEnv lets an operator assert that CertificateRequests
// for the workload issuer are gated by an approver this CLI cannot detect (for
// example an enterprise approver or a disposable environment).
const workloadIssuerApprovalAckEnv = "MCP_WORKLOAD_ISSUER_APPROVAL_ACK"

// validateWorkloadIssuerApprovalGate stops setup from enabling adapter
// certificates while any principal able to create a CertificateRequest could
// have the workload issuer auto-approve a CSR with a forged SPIFFE URI.
// cert-manager auto-approves requests to built-in issuers by default, so the
// gate requires explicit operator acknowledgement of the effective approval
// policy. CRD presence alone cannot prove the default auto-approver is disabled
// or that a restrictive policy is bound to the runtime API. Test mode targets
// disposable Kind clusters and is
// exempt. The runtime API's session checks and issued-certificate validation
// remain in force either way; this gate is defense in depth.
func validateWorkloadIssuerApprovalGate() error {
	if !adapterCertificatesEnabled() {
		return nil
	}
	if strings.TrimSpace(os.Getenv("MCP_RUNTIME_TEST_MODE")) != "" {
		return nil
	}
	if ack, ok := parseBoolEnv(workloadIssuerApprovalAckEnv); ok && ack {
		return nil
	}
	return fmt.Errorf("MCP_ADAPTER_CERTIFICATES=true requires an effective CertificateRequest approval policy for the workload issuer. Verify that cert-manager's default auto-approver is disabled, a policy limits the requester to the runtime API service account and the certificate to bounded client-auth session SPIFFE URIs, and forged requests are denied. Then set %s=true. See docs/connect-clients.md", workloadIssuerApprovalAckEnv)
}

func parseBoolEnv(name string) (value bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "t", "yes":
		return true, true
	case "0", "false", "f", "no":
		return false, true
	}
	return false, false
}
