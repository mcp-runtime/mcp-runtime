package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	platformassets "mcp-runtime/k8s"
	"sigs.k8s.io/yaml"

	rbacassets "mcp-runtime/config/rbac"
	"mcp-runtime/internal/cli/core"
)

// Check effective permissions, not just ClusterRole contents: a missing binding
// leaves a perfect-looking role unusable. SAR is an authorization query, not a
// persistent cluster mutation, and preserves the selected kubectl connection.
func checkOperatorEffectivePermissions(kubectl core.KubectlRunner) DoctorCheck {
	check := DoctorCheck{Name: "operator effective permissions", Remedy: "run `mcp-runtime update` for the target release without --only; inspect operator RoleBindings and ClusterRoleBindings"}
	sources := []struct {
		data      []byte
		namespace string
	}{
		{rbacassets.OperatorRole, ""},
		{rbacassets.ManagedSecrets, doctorMCPServersNamespace},
		{rbacassets.ManagedWorkloads, doctorMCPServersNamespace},
	}
	// Optional issuer permissions are required only when its Deployment exists.
	auth, err := readKubectlOutput(kubectl, []string{"get", "deployment", "mcp-auth-server", "-n", "mcp-platform", "--ignore-not-found", "-o", "name"})
	if err != nil {
		check.Detail = fmt.Sprintf("read optional MCP Auth deployment: %v", err)
		return check
	}
	if strings.TrimSpace(auth) != "" {
		decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(platformassets.OperatorBundledAuth), 4096)
		for {
			var role rbacv1.Role
			err := decoder.Decode(&role)
			if err == io.EOF {
				break
			}
			if err != nil {
				check.Detail = fmt.Sprintf("decode bundled auth rules: %v", err)
				return check
			}
			if role.Kind != "Role" {
				continue
			}
			raw, err := yaml.Marshal(role)
			if err != nil {
				check.Detail = err.Error()
				return check
			}
			sources = append(sources, struct {
				data      []byte
				namespace string
			}{raw, role.Namespace})
		}
	}
	var missing []string
	checked := 0
	for _, source := range sources {
		var role rbacv1.Role
		if err := yaml.UnmarshalStrict(source.data, &role); err != nil {
			check.Detail = fmt.Sprintf("decode bundled operator rules: %v", err)
			return check
		}
		for _, rule := range role.Rules {
			names := rule.ResourceNames
			if len(names) == 0 {
				names = []string{""}
			}
			for _, group := range rule.APIGroups {
				for _, resource := range rule.Resources {
					for _, verb := range rule.Verbs {
						for _, name := range names {
							parts := strings.SplitN(resource, "/", 2)
							attrs := &authorizationv1.ResourceAttributes{Namespace: source.namespace, Group: group, Resource: parts[0], Verb: verb, Name: name}
							if len(parts) == 2 {
								attrs.Subresource = parts[1]
							}
							allowed, err := reviewOperatorPermission(kubectl, attrs)
							if err != nil {
								check.Detail = fmt.Sprintf("verify %s %s.%s namespace=%q: %v", verb, resource, group, source.namespace, err)
								return check
							}
							checked++
							if !allowed {
								missing = append(missing, fmt.Sprintf("%s %s.%s namespace=%q", verb, resource, group, source.namespace))
							}
						}
					}
				}
			}
		}
	}
	if len(missing) > 0 {
		check.Detail = "operator ServiceAccount missing: " + strings.Join(missing, "; ")
		return check
	}
	check.OK = true
	check.Detail = fmt.Sprintf("operator ServiceAccount authorized for all %d bundled permission checks", checked)
	return check
}

func reviewOperatorPermission(kubectl core.KubectlRunner, attrs *authorizationv1.ResourceAttributes) (bool, error) {
	review := authorizationv1.SubjectAccessReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"},
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:               "system:serviceaccount:mcp-runtime:mcp-runtime-operator-controller-manager",
			Groups:             []string{"system:serviceaccounts", "system:serviceaccounts:mcp-runtime", "system:authenticated"},
			ResourceAttributes: attrs,
		},
	}
	data, err := json.Marshal(review)
	if err != nil {
		return false, err
	}
	cmd, err := kubectl.CommandArgs([]string{"create", "--raw=/apis/authorization.k8s.io/v1/subjectaccessreviews", "-f", "-"})
	if err != nil {
		return false, err
	}
	cmd.SetStdin(bytes.NewReader(data))
	out, err := runKubectlBytes(cmd)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(out, &review); err != nil {
		return false, err
	}
	if review.Status.EvaluationError != "" {
		return false, fmt.Errorf("authorization evaluation: %s", review.Status.EvaluationError)
	}
	return review.Status.Allowed && !review.Status.Denied, nil
}
