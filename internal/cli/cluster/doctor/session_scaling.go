package doctor

import (
	"encoding/json"
	"fmt"
	"strings"

	"mcp-runtime/internal/cli/core"
)

func checkSessionLocalDeploymentScaling(kubectl core.KubectlRunner) DoctorCheck {
	const checkName = "ui shared session store"
	namespace := componentNamespace("ui")
	if _, err := readKubectlOutput(kubectl, []string{"get", "namespace", namespace, "-o", "jsonpath={.metadata.name}"}); err != nil {
		return DoctorCheck{
			Name:   checkName,
			OK:     true,
			Detail: fmt.Sprintf("namespace %s not installed; skipping UI session store check", namespace),
		}
	}
	raw, err := readKubectlOutput(kubectl, []string{
		"get", "deployment", "mcp-ui",
		"-n", namespace,
		"-o", "json",
	})
	if err != nil || strings.TrimSpace(raw) == "" {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("deployment mcp-ui not found in %s", namespace),
			Remedy: "install the UI from the platform namespace",
		}
	}
	store := uiSessionStore(raw)
	if store != "postgres" {
		return DoctorCheck{
			Name:   checkName,
			OK:     false,
			Detail: fmt.Sprintf("mcp-ui in %s uses session store %q", namespace, store),
			Remedy: "set UI_SESSION_STORE=postgres with UI_SESSION_DATABASE_URL and UI_SESSION_ENCRYPTION_KEY",
		}
	}
	return DoctorCheck{
		Name:   checkName,
		OK:     true,
		Detail: fmt.Sprintf("mcp-ui in %s uses the shared Postgres session store", namespace),
	}
}

func uiSessionStore(deploymentJSON string) string {
	var doc struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(deploymentJSON), &doc); err != nil {
		return ""
	}
	for _, container := range doc.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == "UI_SESSION_STORE" {
				return strings.ToLower(strings.TrimSpace(env.Value))
			}
		}
	}
	return "memory"
}

func parseDoctorReplicaCount(raw string) (int32, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("empty replica count")
	}
	var replicas int32
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid replica count %q", raw)
		}
		replicas = replicas*10 + int32(ch-'0')
	}
	return replicas, nil
}
