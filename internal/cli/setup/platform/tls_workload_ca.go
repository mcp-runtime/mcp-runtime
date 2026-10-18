package platform

import (
	"context"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"os"
	"time"
)

// ensureManagedWorkloadCA gates the bundled mcp-runtime-ca root.
//
// Test mode keeps the convenience of generating a missing CA. Production never
// silently mints a new root (a lost Secret would otherwise re-key trust without
// an overlap window); it requires an existing, validated CA restored from
// backup or supplied by the operator. Invalid or expired CAs always fail;
// near-expiry fails in production and warns in test mode.
func ensureManagedWorkloadCA(plan setupplan.Plan, now time.Time) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	secret, err := clients.Clientset.CoreV1().Secrets(certmanager.CertManagerNamespace).Get(context.Background(), certmanager.CertCASecretName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return core.WrapWithBase(core.ErrCASecretNotFound, err, "read managed workload CA Secret")
		}
		if !plan.TestMode {
			return core.NewWithBase(core.ErrCASecretNotFound, fmt.Sprintf(
				"managed workload CA Secret %s/%s is missing. Production setup will not generate a new root. Restore it from your encrypted backup, create it from your CA (kubectl create secret tls %s --cert=ca.crt --key=ca.key -n %s), or use an enterprise issuer via --mtls-cluster-issuer; see docs/cli-reference.md (Bundled workload CA lifecycle)",
				certmanager.CertManagerNamespace, certmanager.CertCASecretName, certmanager.CertCASecretName, certmanager.CertManagerNamespace))
		}
		if _, err := ensureCASecretClientGo(); err != nil {
			return core.WrapWithBase(core.ErrCASecretNotFound, err, "create managed workload CA")
		}
		return nil
	}
	health, err := certmanager.ValidateCAKeyPair(secret.Data["tls.crt"], secret.Data["tls.key"], now)
	if err != nil {
		return core.WrapWithBase(core.ErrCASecretInvalid, err, fmt.Sprintf("managed workload CA %s/%s is not usable: %v. Restore a valid CA from backup or rotate it per docs/cli-reference.md (Bundled workload CA lifecycle)", certmanager.CertManagerNamespace, certmanager.CertCASecretName, err))
	}
	days := int(health.Remaining.Hours() / 24)
	if health.NearExpiry {
		msg := fmt.Sprintf("managed workload CA expires %s (%d days remaining, minimum %d); rotate with dual trust per docs/cli-reference.md (Bundled workload CA lifecycle)", health.NotAfter.UTC().Format(time.RFC3339), days, int(certmanager.MinCARemainingLifetime.Hours()/24))
		if !plan.TestMode {
			return core.NewWithBase(core.ErrCANearExpiry, msg)
		}
		core.Warn(msg)
		return nil
	}
	core.Info(fmt.Sprintf("Managed workload CA healthy: %d days remaining", days))
	return nil
}

func checkCASecretClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if _, err := clients.Clientset.CoreV1().Secrets("cert-manager").Get(context.Background(), "mcp-runtime-ca", metav1.GetOptions{}); err != nil {
		return err
	}
	return nil
}

func ensureCASecretClientGo() (bool, error) {
	if err := checkCASecretClientGo(); err == nil {
		return false, nil
	} else if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("read existing bundled CA: %w", err)
	}
	manifest, err := certmanager.RenderGeneratedCASecretManifest(time.Now())
	if err != nil {
		return false, err
	}
	if err := applyManifestYAML(manifest, "", os.Stdout); err != nil {
		return false, err
	}
	return true, nil
}
