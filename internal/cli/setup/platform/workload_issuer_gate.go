package platform

import (
	"context"
	"fmt"
	"os"
	"strings"

	"mcp-runtime/pkg/k8sclient"
)

// workloadIssuerApprovalAckEnv lets an operator assert that CertificateRequests
// for the workload issuer are gated by an approver this CLI cannot detect (for
// example an enterprise approver or a disposable environment).
const workloadIssuerApprovalAckEnv = "MCP_WORKLOAD_ISSUER_APPROVAL_ACK"

// certificateRequestPolicyCRD is installed by cert-manager approver-policy.
const certificateRequestPolicyCRD = "certificaterequestpolicies.policy.cert-manager.io"

// workloadIssuerApproverPolicyInstalled is a variable so tests can stub cluster
// access.
var workloadIssuerApproverPolicyInstalled = workloadIssuerApproverPolicyInstalledClientGo

func workloadIssuerApproverPolicyInstalledClientGo() bool {
	clients, err := platformKubernetesClients()
	if err != nil {
		return false
	}
	return k8sclient.CheckCRDExists(context.Background(), clients, certificateRequestPolicyCRD) == nil
}

// validateWorkloadIssuerApprovalGate stops setup from enabling adapter
// certificates while any principal able to create a CertificateRequest could
// have the workload issuer auto-approve a CSR with a forged SPIFFE URI.
// cert-manager auto-approves requests to built-in issuers by default, so the
// gate requires either approver-policy (detected by its CRD) or an explicit
// operator acknowledgement. Test mode targets disposable Kind clusters and is
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
	if workloadIssuerApproverPolicyInstalled() {
		return nil
	}
	return fmt.Errorf("MCP_ADAPTER_CERTIFICATES=true requires a CertificateRequest approval policy for the workload issuer: cert-manager approver-policy (CRD %s) was not found, so cert-manager's default auto-approver could sign a CSR with any SPIFFE URI requested by another principal. Install approver-policy with a policy limited to the runtime API service account, client-auth usage, and the session SPIFFE URI shape, then retry. If another approver already gates the issuer (or this is a disposable environment), set %s=true. See docs/agent-adapters.md", certificateRequestPolicyCRD, workloadIssuerApprovalAckEnv)
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
