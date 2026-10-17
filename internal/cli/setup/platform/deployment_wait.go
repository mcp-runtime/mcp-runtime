package platform

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
)

// Deployment, CRD, rollout and Job wait helpers with debug diagnostics.
// Split from deploy.go (#268); operator manifest rendering/apply stays there.

func restartDeployment(name, namespace string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.RestartDeployment(context.Background(), clients, namespace, name, time.Now())
}

//lint:ignore U1000 retained as the legacy kubectl implementation for focused tests and fallback patches.
func restartDeploymentWithKubectl(kubectl core.KubectlRunner, name, namespace string) error {
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	return kubectl.RunWithOutput([]string{"rollout", "restart", "deployment/" + name, "-n", namespace}, os.Stdout, os.Stderr)
}

func checkCRDInstalled(name string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.CheckCRDExists(context.Background(), clients, name)
}

func checkCRDInstalledWithKubectl(kubectl core.KubectlRunner, name string) error {
	// #nosec G204 -- name is hardcoded CRD identifier from internal code.
	return kubectl.RunWithOutput([]string{"get", "crd", name}, os.Stdout, os.Stderr)
}

// waitForDeploymentAvailable polls a deployment until it has at least one available replica or times out.
func waitForDeploymentAvailable(logger *zap.Logger, name, namespace, selector string, timeout time.Duration) error {
	return waitForDeploymentReady(logger, name, namespace, selector, timeout, false)
}

// waitForDeploymentRolledOut requires the current revision to replace every old
// replica, including when the old revision remains available during a failure.
func waitForDeploymentRolledOut(logger *zap.Logger, name, namespace, selector string, timeout time.Duration) error {
	return waitForDeploymentReady(logger, name, namespace, selector, timeout, true)
}

func waitForDeploymentReady(logger *zap.Logger, name, namespace, selector string, timeout time.Duration, rollout bool) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	waitForDeployment := k8sclient.WaitForDeploymentAvailable
	if rollout {
		waitForDeployment = k8sclient.WaitForDeploymentRolledOut
	}
	if err := waitForDeployment(context.Background(), clients, namespace, name, timeout); err != nil {
		msg := fmt.Sprintf("timed out waiting for deployment %s in namespace %s", name, namespace)
		cause := core.NewWithBase(core.ErrSetupDeploymentReadinessDeadlineExceeded, "deployment readiness deadline exceeded")
		ctx := map[string]any{
			"deployment": name,
			"namespace":  namespace,
			"selector":   selector,
			"component":  "deployment-wait",
			"cause":      err.Error(),
		}
		mergeDeploymentDebugDiagnosticsIfNeeded(core.DefaultKubectlClient(), ctx, name, namespace, selector)
		wrappedErr := core.WrapWithBaseAndContext(core.ErrDeploymentTimeout, cause, msg, ctx)
		core.Error("Deployment timeout")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Deployment timeout")
		}
		return wrappedErr
	}
	return nil
}

// waitForDeploymentAvailableWithKubectl polls a deployment until it has at least one available replica or times out.
func waitForDeploymentAvailableWithKubectl(kubectl core.KubectlRunner, logger *zap.Logger, name, namespace, selector string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastLog := time.Time{}
	for {
		// #nosec G204 -- name/namespace from internal setup logic, not direct user input.
		cmd, err := kubectl.CommandArgs([]string{"get", "deployment", name, "-n", namespace, "-o", "jsonpath={.status.availableReplicas}"})
		if err == nil {
			out, execErr := cmd.Output()
			if execErr == nil {
				val := strings.TrimSpace(string(out))
				if val == "" {
					val = "0"
				}
				if n, convErr := strconv.Atoi(val); convErr == nil && n > 0 {
					return nil
				}
			}
		}
		if time.Since(lastLog) > 10*time.Second {
			core.Info(fmt.Sprintf("Still waiting for deployment/%s in %s (selector %s, timeout %s)", name, namespace, selector, timeout.Round(time.Second)))
			lastLog = time.Now()
		}
		if time.Now().After(deadline) {
			msg := fmt.Sprintf("timed out waiting for deployment %s in namespace %s", name, namespace)
			cause := core.NewWithBase(core.ErrSetupDeploymentReadinessDeadlineExceeded, "deployment readiness deadline exceeded")
			ctx := map[string]any{
				"deployment": name,
				"namespace":  namespace,
				"selector":   selector,
				"component":  "deployment-wait",
			}
			mergeDeploymentDebugDiagnosticsIfNeeded(kubectl, ctx, name, namespace, selector)
			wrappedErr := core.WrapWithBaseAndContext(core.ErrDeploymentTimeout, cause, msg, ctx)
			core.Error("Deployment timeout")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Deployment timeout")
			}
			return wrappedErr
		}
		time.Sleep(5 * time.Second)
	}
}

// printDeploymentDiagnostics prints a quick status of pods for a deployment selector to help users triage readiness issues.
func printDeploymentDiagnostics(deploy, namespace, selector string) {
	printDeploymentDiagnosticsWithKubectl(core.DefaultKubectlClient(), deploy, namespace, selector)
}

// printDeploymentDiagnosticsWithKubectl prints a quick status of pods for a deployment selector.
func printDeploymentDiagnosticsWithKubectl(kubectl core.KubectlRunner, deploy, namespace, selector string) {
	core.Warn(fmt.Sprintf("Deployment %s in %s is not ready. Showing pod statuses:", deploy, namespace))
	// #nosec G204 -- namespace/selector from internal diagnostics, not user input.
	_ = kubectl.RunWithOutput([]string{"get", "pods", "-n", namespace, "-l", selector, "-o", "wide"}, os.Stdout, os.Stderr)
}

// mergeDeploymentDebugDiagnosticsIfNeeded fetches describe/events/pods from the API when --debug is set
// and attaches a bounded blob to the errx context (cluster-backed failures, not local validation).
func mergeDeploymentDebugDiagnosticsIfNeeded(kubectl core.KubectlRunner, m map[string]any, deployName, namespace, selector string) {
	if !core.IsDebugMode() {
		return
	}
	if d := buildDeploymentWaitDebugDetail(kubectl, deployName, namespace, selector); d != "" {
		m["diagnostics"] = trimDiagnosticsString(d)
	}
}

// buildDeploymentWaitDebugDetail returns kubectl text for a stuck or timed-out deployment wait.
func buildDeploymentWaitDebugDetail(kubectl core.KubectlRunner, deployName, namespace, selector string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("---- describe deployment %s\n", deployName))
	// #nosec G204 -- deploy/namespace/selector are internal setup identifiers, not user shell input.
	if out, err := kubectlText(kubectl, []string{
		"describe", "deployment", deployName, "-n", namespace, "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get pods (selector)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "pods", "-n", namespace, "-l", selector, "-o", "wide", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get events (sorted)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "events", "-n", namespace, "--sort-by", ".lastTimestamp", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	return b.String()
}

// buildNamespacedResourceDebugDetail returns describe, pods, and events for a namespaced object (e.g. StatefulSet, Job).
func buildNamespacedResourceDebugDetail(kubectl core.KubectlRunner, kind, name, namespace string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("---- describe %s %s\n", kind, name))
	// #nosec G204 -- kind/name/namespace are internal resource identifiers, not user shell input.
	if out, err := kubectlText(kubectl, []string{
		"describe", kind, name, "-n", namespace, "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get pods (namespace)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "pods", "-n", namespace, "-o", "wide", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get events (sorted)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "events", "-n", namespace, "--sort-by", ".lastTimestamp", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	return b.String()
}

// buildCRDCheckDebugDetail returns CRD and api-resources text when a CRD presence check fails.
func buildCRDCheckDebugDetail(kubectl core.KubectlRunner, crdName string) string {
	var b strings.Builder
	b.WriteString("---- get crd\n")
	// #nosec G204 -- crdName is a hardcoded internal API identity.
	if out, err := kubectlText(kubectl, []string{
		"get", "crd", crdName, "-o", "wide", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("get crd: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- api-resources (group mcpruntime.org)\n")
	if out, err := kubectlText(kubectl, []string{
		"api-resources", "--api-group=mcpruntime.org", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	return b.String()
}

func mergeCRDCheckDebugDiagnosticsIfNeeded(kubectl core.KubectlRunner, m map[string]any, crdName string) {
	if !core.IsDebugMode() {
		return
	}
	if d := buildCRDCheckDebugDetail(kubectl, crdName); d != "" {
		m["diagnostics"] = trimDiagnosticsString(d)
	}
}

// mcpPlatformDependencyRolloutFailed wraps early platform storage/messaging rollouts; diagnostics are attached only in --debug.
func mcpPlatformDependencyRolloutFailed(kubectl core.KubectlRunner, err error, kind, name, namespace, phase string) error {
	ctx := map[string]any{
		"component": "platform",
		"phase":     phase,
		"resource":  fmt.Sprintf("%s/%s", kind, name),
		"namespace": namespace,
	}
	if core.IsDebugMode() {
		if diag := buildNamespacedResourceDebugDetail(kubectl, kind, name, namespace); diag != "" {
			ctx["diagnostics"] = trimDiagnosticsString(diag)
		}
	}
	return core.WrapWithBaseAndContext(core.ErrOperatorDeploymentFailed, err,
		fmt.Sprintf("platform %s: %s/%s: %v", phase, kind, name, err), ctx)
}

// mcpPlatformDependencyJobFailed wraps the clickhouse init job; diagnostics are attached only in --debug.
func mcpPlatformDependencyJobFailed(kubectl core.KubectlRunner, err error, name, namespace, phase string) error {
	ctx := map[string]any{
		"component": "platform",
		"phase":     phase,
		"resource":  "job/" + name,
		"namespace": namespace,
	}
	if core.IsDebugMode() {
		if diag := buildNamespacedResourceDebugDetail(kubectl, "job", name, namespace); diag != "" {
			ctx["diagnostics"] = trimDiagnosticsString(diag)
		}
	}
	return core.WrapWithBaseAndContext(core.ErrOperatorDeploymentFailed, err,
		fmt.Sprintf("platform %s: job/%s: %v", phase, name, err), ctx)
}

// runRolloutWithOptionalDebugCapture runs kubectl rollout status, teeing output to a buffer
// in --debug mode so it can be attached to the structured error.
func runRolloutWithOptionalDebugCapture(kubectl core.KubectlRunner, kind, name, namespace, timeout string) (capture string, err error) {
	args := []string{
		"rollout", "status",
		fmt.Sprintf("%s/%s", kind, name),
		"-n", namespace, "--timeout=" + timeout,
	}
	if !core.IsDebugMode() {
		return "", kubectl.RunWithOutput(args, os.Stdout, os.Stderr)
	}
	var buf bytes.Buffer
	w := io.MultiWriter(os.Stdout, &buf)
	err = kubectl.RunWithOutput(args, w, w)
	return buf.String(), err
}

func runRolloutWithOptionalDebugCaptureClientGo(kind, name, namespace string, timeout time.Duration) (capture string, err error) {
	return "", waitForRolloutStatusWithClientGo(kind, name, namespace, timeout)
}

func kubectlText(kubectl core.KubectlRunner, args []string) (string, error) {
	cmd, err := kubectl.CommandArgs(args)
	if err != nil {
		return "", err
	}
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func waitForRolloutStatusWithKubectl(kubectl core.KubectlRunner, kind, name, namespace, timeout string) error {
	return kubectl.RunWithOutput([]string{"rollout", "status", fmt.Sprintf("%s/%s", kind, name), "-n", namespace, "--timeout=" + timeout}, os.Stdout, os.Stderr)
}

func waitForRolloutStatusWithClientGo(kind, name, namespace string, timeout time.Duration) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.WaitForWorkloadRollout(context.Background(), clients, namespace, kind, name, timeout)
}

func waitForJobCompletionWithKubectl(kubectl core.KubectlRunner, name, namespace, timeout string) error {
	return kubectl.RunWithOutput([]string{"wait", "--for=condition=complete", "job/" + name, "-n", namespace, "--timeout=" + timeout}, os.Stdout, os.Stderr)
}

func waitForJobCompletionClientGo(name, namespace string, timeout time.Duration) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.WaitForJobComplete(context.Background(), clients, namespace, name, timeout)
}

func deleteJobIfExistsWithKubectl(kubectl core.KubectlRunner, name, namespace string) error {
	return kubectl.RunWithOutput([]string{"delete", "job/" + name, "-n", namespace, "--ignore-not-found=true", "--wait=true", "--timeout=60s"}, os.Stdout, os.Stderr)
}

func deleteJobIfExistsClientGo(name, namespace string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.DeleteJob(context.Background(), clients, namespace, name, 60*time.Second)
}
