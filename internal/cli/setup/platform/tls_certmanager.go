package platform

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"io"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"net/http"
	"os"
	"strings"
	"time"
)

func acmeServerURLForClientGo(staging bool) string {
	if staging {
		return "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	return "https://acme-v02.api.letsencrypt.org/directory"
}

func ensureCertManagerInstalledClientGo(logger *zap.Logger) error {
	if err := checkCertManagerInstalledClientGo(); err == nil {
		// CRDs exist but pods may not be running (e.g. after a k3s restart).
		// Verify deployments are available; if they aren't, fall through to reinstall.
		if podErr := waitForCertManagerDeploymentsClientGo(logger, 2*time.Minute, false); podErr == nil {
			core.Info("cert-manager already installed")
			return nil
		}
		core.Warn("cert-manager CRDs present but deployments not ready — reinstalling")
	}
	core.Info("Installing cert-manager " + certmanager.CertManagerRelease())
	warnMsg := "If this fails (no network), install cert-manager manually, then re-run setup with --skip-cert-manager-install"
	core.Warn(warnMsg)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Get(certmanager.CertManagerInstallManifestURL()) // #nosec G107 -- fixed cert-manager release URL.
	if err != nil {
		wrapped := core.WrapWithBase(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("cert-manager install failed: %v. %s", err, warnMsg))
		core.Error("cert-manager install failed")
		if logger != nil {
			core.LogStructuredError(logger, wrapped, "cert-manager install failed")
		}
		return wrapped
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err := core.NewWithBase(core.ErrCertManagerInstallFailed, fmt.Sprintf("cert-manager install manifest download failed: HTTP %d. %s", resp.StatusCode, warnMsg))
		core.Error("cert-manager install failed")
		if logger != nil {
			core.LogStructuredError(logger, err, "cert-manager install failed")
		}
		return err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return core.WrapWithBase(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("read cert-manager install manifest: %v", err))
	}
	if err := applyManifestYAML(string(body), "", os.Stdout); err != nil {
		wrapped := core.WrapWithBase(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("cert-manager install failed: %v. %s", err, warnMsg))
		core.Error("cert-manager install failed")
		if logger != nil {
			core.LogStructuredError(logger, wrapped, "cert-manager install failed")
		}
		return wrapped
	}
	return waitForCertManagerDeploymentsClientGo(logger, 5*time.Minute, true)
}

// waitForCertManagerDeploymentsClientGo waits for all three cert-manager
// deployments to be available within timeout. When logErrors is false errors
// are returned silently (used for pre-flight readiness probes).
func waitForCertManagerDeploymentsClientGo(logger *zap.Logger, timeout time.Duration, logErrors bool) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	start := time.Now()
	if logErrors {
		core.Info(fmt.Sprintf("Waiting for cert-manager deployments (combined timeout %s across three deployments)", timeout))
	}
	for _, dep := range []string{"cert-manager", "cert-manager-cainjector", "cert-manager-webhook"} {
		remaining := time.Until(start.Add(timeout))
		if remaining <= 0 {
			err := core.NewWithBase(core.ErrCertManagerInstallFailed, fmt.Sprintf("timed out waiting for cert-manager before deployment/%s", dep))
			if logErrors {
				core.Error("cert-manager did not become ready")
				if logger != nil {
					core.LogStructuredError(logger, err, "cert-manager did not become ready")
				}
			}
			return err
		}
		if err := k8sclient.WaitForDeploymentAvailable(context.Background(), clients, "cert-manager", dep, remaining.Round(time.Second)); err != nil {
			wrapped := core.WrapWithBase(core.ErrCertManagerInstallFailed, err, fmt.Sprintf("cert-manager component %s not ready: %v", dep, err))
			if logErrors {
				core.Error("cert-manager did not become ready")
				if logger != nil {
					core.LogStructuredError(logger, wrapped, "cert-manager did not become ready")
				}
			}
			return wrapped
		}
	}
	if logErrors {
		core.Info("cert-manager is ready")
	}
	return nil
}

func checkCertManagerInstalledClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.CheckCRDExists(context.Background(), clients, core.CertManagerCRDName); err != nil {
		return core.ErrCertManagerNotInstalled
	}
	return nil
}

func waitForTraefikDeploymentForACMEClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if _, err := k8sclient.GetDeployment(context.Background(), clients, "traefik", "traefik"); err != nil {
		if apierrors.IsNotFound(err) {
			core.Warn("No traefik/traefik deployment found; skipping Traefik wait. cert-manager still needs the Traefik ingress class to serve HTTP-01, with port 80 on your public hostnames")
			return nil
		}
		return err
	}
	core.Info("Waiting for traefik/traefik (ingress must be up before the ACME request)")
	if err := k8sclient.WaitForDeploymentAvailable(context.Background(), clients, "traefik", "traefik", 3*time.Minute); err != nil {
		return core.WrapWithBase(core.ErrCertTraefikNotReady, err, fmt.Sprintf("traefik not ready: %v", err))
	}
	core.Info("traefik/traefik is available")
	return nil
}

func checkNamedClusterIssuerClientGo(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return core.NewWithBase(core.ErrClusterIssuerNotFound, "ClusterIssuer name is empty (set --tls-cluster-issuer or MCP_TLS_CLUSTER_ISSUER)")
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.CheckClusterIssuer(context.Background(), clients, name); err != nil {
		return core.WrapWithBase(core.ErrClusterIssuerNotFound, err, fmt.Sprintf("ClusterIssuer %q not found. Install your org issuer first (cert-manager) or fix --tls-cluster-issuer / MCP_TLS_CLUSTER_ISSUER: %v", name, err))
	}
	return nil
}
