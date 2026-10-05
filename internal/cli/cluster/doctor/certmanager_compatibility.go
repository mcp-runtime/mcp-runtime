package doctor

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/version"
	"mcp-runtime/internal/cli/core"
)

// Support matrix reviewed 2026-10-03 against cert-manager.io/docs/releases/.
// Fail closed for unreviewed minors instead of inferring future compatibility.
func certManagerVersionCompatible(image, kubernetesVersion string) (string, error) {
	image = strings.SplitN(image, "@", 2)[0]
	colon := strings.LastIndex(image, ":")
	if colon <= strings.LastIndex(image, "/") {
		return "", fmt.Errorf("cert-manager image has no verifiable release tag")
	}
	tag := image[colon+1:]
	cm, err := version.ParseSemantic(tag)
	if err != nil {
		return tag, fmt.Errorf("unrecognized cert-manager release %q", tag)
	}
	k, err := version.ParseSemantic(kubernetesVersion)
	if err != nil {
		return tag, fmt.Errorf("unrecognized Kubernetes version %q", kubernetesVersion)
	}
	if cm.Major() != 1 || cm.PreRelease() != "" {
		return tag, fmt.Errorf("cert-manager %s is not a reviewed stable release", tag)
	}
	var low, high uint
	switch cm.Minor() {
	case 20:
		low, high = 32, 35
	case 21:
		low, high = 33, 36
	default:
		return tag, fmt.Errorf("cert-manager %s is retired or outside the reviewed support matrix", tag)
	}
	// Only major.minor is compared: managed distributions report vendor
	// suffixes (v1.33.5-eks-113cf36, v1.33.5-gke.1200000) that parse as
	// prereleases but are supported releases.
	if k.Major() != 1 || k.Minor() < low || k.Minor() > high {
		return tag, fmt.Errorf("cert-manager %s supports Kubernetes 1.%d–1.%d; cluster is %s", tag, low, high, kubernetesVersion)
	}
	return cm.String(), nil
}

func checkCertManagerCompatibility(kubectl core.KubectlRunner) DoctorCheck {
	check := DoctorCheck{Name: "cert-manager compatibility", Remedy: "back up cert-manager resources and Secrets; rehearse one-minor-at-a-time upgrades using docs/reference-deployment.md#upgrading-an-existing-cert-manager-installation"}
	if !doctorTLSPreflightRequested() {
		check.OK = true
		check.Detail = "TLS preflight not requested; skipping cert-manager compatibility"
		check.Remedy = ""
		return check
	}
	read := func(args []string, into any) error {
		cmd, err := kubectl.CommandArgs(args)
		if err != nil {
			return err
		}
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		return json.Unmarshal(out, into)
	}
	var cluster struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := read([]string{"version", "-o", "json"}, &cluster); err != nil {
		check.Detail = "cannot read Kubernetes server version: " + err.Error()
		return check
	}
	var observed string
	var details []string
	for _, name := range []string{"cert-manager", "cert-manager-webhook", "cert-manager-cainjector"} {
		var deployment struct {
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if err := read([]string{"get", "deploy", name, "-n", "cert-manager", "-o", "json"}, &deployment); err != nil {
			check.Detail = "cannot read " + name + " version: " + err.Error()
			return check
		}
		if len(deployment.Spec.Template.Spec.Containers) == 0 {
			check.Detail = name + " has no controller container image"
			return check
		}
		tag, err := certManagerVersionCompatible(deployment.Spec.Template.Spec.Containers[0].Image, cluster.ServerVersion.GitVersion)
		if err != nil {
			check.Detail = name + ": " + err.Error()
			return check
		}
		if observed != "" && observed != tag {
			check.Detail = "cert-manager component releases differ: " + observed + " and " + tag
			return check
		}
		observed = tag
		details = append(details, name+"="+tag)
	}
	check.OK = true
	check.Remedy = ""
	check.Detail = strings.Join(details, "; ") + "; Kubernetes " + cluster.ServerVersion.GitVersion
	return check
}
