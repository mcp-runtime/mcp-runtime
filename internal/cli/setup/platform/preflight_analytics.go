package platform

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platforminventory"
	"strings"
)

// checkStaleAnalyticsJob warns when a failed clickhouse-init Job is present.
// Setup calls deleteJobIfExists before re-applying, but surfacing it early lets
// the operator know why a previous analytics deploy may have failed.
func checkStaleAnalyticsJob(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	const jobName = "clickhouse-init"
	failed, err := k8sclient.IsJobFailed(ctx, clients, core.ComponentNamespace("clickhouse"), jobName)
	if err != nil || !failed {
		return nil
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Job %q in namespace %q is in a failed state from a previous run — setup will delete and re-run it.",
			jobName, core.ComponentNamespace("clickhouse"),
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete job -n %s %s", core.ComponentNamespace("clickhouse"), jobName),
		},
	}}
}

// checkPostgresPasswordSync detects when the Postgres pod is running but the
// POSTGRES_DSN password in mcp-postgres-credentials no longer authenticates. This
// happens when setup rotated the secret on a rerun but didn't sync the live DB.
func checkPostgresPasswordSync(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	podName, err := k8sclient.GetFirstReadyPodName(ctx, clients, core.ComponentNamespace("postgres"), "app=mcp-postgres")
	if err != nil || podName == "" {
		return nil // Postgres not running yet
	}

	dsnNamespace, dsnSecret, ok := platforminventory.CredentialPlacement("POSTGRES_DSN")
	if !ok {
		return nil
	}
	dsn, err := k8sclient.SecretStringDataValue(ctx, clients, dsnNamespace, dsnSecret, "POSTGRES_DSN")
	if err != nil || strings.TrimSpace(dsn) == "" {
		return nil // secret not yet created
	}

	// Probe by exec-ing a quick psql connection test inside the pod.
	// Use PGPASSWORD + -h localhost to bypass peer auth and actually test the password.
	kubectl := core.DefaultKubectlClient()
	cmd, err := kubectl.CommandArgs([]string{
		"exec", "-n", core.ComponentNamespace("postgres"), podName, "--",
		"sh", "-c", fmt.Sprintf("PGPASSWORD=%s psql -h localhost -U mcp_runtime -c '\\l' -t -q 2>&1", shellQuote(extractPostgresPassword(dsn))),
	})
	if err != nil {
		return nil
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil // connected successfully — passwords are in sync
	}
	if !strings.Contains(string(out), "password authentication failed") {
		return nil // some other error; don't block setup
	}

	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Postgres pod %q is running but the password in mcp-postgres-credentials does not authenticate.\n"+
				"         Setup will fix this automatically by running ALTER USER on the Postgres pod.",
			podName,
		),
		cleanup: []string{
			"# Setup will run this automatically; or run it manually:",
			fmt.Sprintf("kubectl exec -n %s %s -- sh -c 'PGPASSWORD=<new_pass> psql -h localhost -U mcp_runtime -c \"ALTER USER mcp_runtime PASSWORD '\\''<new_pass>'\\''\"'",
				core.ComponentNamespace("postgres"), podName),
		},
	}}
}

// extractPostgresPassword parses a postgres://user:pass@host/db DSN and returns the password.
func extractPostgresPassword(dsn string) string {
	// Fast path: find the user:pass@ segment.
	// postgres://mcp_runtime:PASSWORD@host/db
	after, ok := strings.CutPrefix(dsn, "postgres://")
	if !ok {
		return ""
	}
	atIdx := strings.LastIndex(after, "@")
	if atIdx < 0 {
		return ""
	}
	userPass := after[:atIdx]
	colonIdx := strings.Index(userPass, ":")
	if colonIdx < 0 {
		return ""
	}
	return userPass[colonIdx+1:]
}

// checkStuckAnalyticsStatefulSets warns when analytics StatefulSet pods are
// stuck in CrashLoopBackOff, Error, or Terminating state. Stuck pods slow the
// rollout-wait that setup runs after applying manifests.
func checkStuckAnalyticsStatefulSets(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	type stsSpec struct {
		name     string
		selector string
	}
	targets := []stsSpec{
		{"clickhouse", "app=clickhouse"},
		{"mcp-postgres", "app=mcp-postgres"},
		{"kafka", "app=kafka"},
		{"tempo", "app=tempo"},
		{"loki", "app=loki"},
	}
	var issues []preflightIssue
	for _, t := range targets {
		namespace := workloadNamespace(t.name)
		pods, err := clients.Clientset.CoreV1().Pods(namespace).List(
			ctx, metav1.ListOptions{LabelSelector: t.selector})
		if err != nil || len(pods.Items) == 0 {
			continue
		}
		var stuck []string
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil {
				stuck = append(stuck, fmt.Sprintf("%s (Terminating)", pod.Name))
				continue
			}
			for _, cs := range pod.Status.ContainerStatuses {
				w := cs.State.Waiting
				if w == nil {
					continue
				}
				if w.Reason == "CrashLoopBackOff" || w.Reason == "Error" {
					stuck = append(stuck, fmt.Sprintf("%s (%s)", pod.Name, w.Reason))
				}
			}
		}
		if len(stuck) == 0 {
			continue
		}
		issues = append(issues, preflightIssue{
			fatal: false,
			message: fmt.Sprintf(
				"Analytics StatefulSet %q has stuck pod(s): %s — this may cause the rollout wait to time out.",
				t.name, strings.Join(stuck, "; "),
			),
			cleanup: []string{
				fmt.Sprintf("kubectl delete pod -n %s -l %s --grace-period=0 --force",
					namespace, t.selector),
			},
		})
	}
	return issues
}
