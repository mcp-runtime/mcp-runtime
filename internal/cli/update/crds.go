package update

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	"mcp-runtime/pkg/k8sclient"
)

var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// applyReleaseCRDs filters the plan bundle to CustomResourceDefinition
// objects, applies them, then waits until each reports Established.
func applyReleaseCRDs(ctx context.Context, clients *k8sclient.Clients, plan *Plan, timeout time.Duration, progress func(string)) ([]CRDResult, error) {
	if clients == nil {
		return []CRDResult{{Name: "*", Error: "Kubernetes dynamic client is required to apply CustomResourceDefinitions"}}, fmt.Errorf("dynamic client required")
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	progress(fmt.Sprintf("Applying CustomResourceDefinitions (%s)", stringsJoin(plan.CRDNames)))
	applied, err := k8sclient.ApplyManifestYAML(ctx, clients, []byte(plan.crdsYAML), "")
	if err != nil {
		return []CRDResult{{Name: "*", Error: err.Error()}}, err
	}
	results := make([]CRDResult, 0, len(applied))
	for _, a := range applied {
		results = append(results, CRDResult{Name: a.Name, Action: a.Action})
		progress(fmt.Sprintf("CRD %s %s", a.Name, a.Action))
	}
	progress(fmt.Sprintf("Waiting for CustomResourceDefinitions to become Established (timeout %s)", timeout))
	if err := waitForCRDsEstablished(ctx, clients, plan.CRDNames, timeout); err != nil {
		return append(results, CRDResult{Name: "*", Error: err.Error()}), err
	}
	progress("CustomResourceDefinitions are Established")
	return results, nil
}

func waitForCRDsEstablished(ctx context.Context, clients *k8sclient.Clients, names []string, timeout time.Duration) error {
	if clients == nil || clients.Dynamic == nil {
		return fmt.Errorf("dynamic client required to wait for CustomResourceDefinitions")
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
			obj, err := clients.Dynamic.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			established, namesAccepted, condErr := crdReadyConditions(obj.Object)
			if condErr != nil {
				return false, condErr
			}
			if namesAccepted && established {
				return true, nil
			}
			return false, nil
		}); err != nil {
			return fmt.Errorf("CustomResourceDefinition %s did not become Established within %s: %w", name, timeout, err)
		}
	}
	if clients.Discovery != nil {
		// Invalidate cached discovery so later API calls see the new types.
		if invalidator, ok := clients.Discovery.(interface{ Invalidate() }); ok {
			invalidator.Invalidate()
		}
	}
	return nil
}

func crdReadyConditions(obj map[string]any) (established, namesAccepted bool, err error) {
	status, _ := obj["status"].(map[string]any)
	conds, _ := status["conditions"].([]any)
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := c["type"].(string)
		st, _ := c["status"].(string)
		switch typ {
		case "Established":
			established = st == "True"
			if st == "False" {
				return false, namesAccepted, fmt.Errorf("Established=False: %v", c["message"])
			}
		case "NamesAccepted":
			namesAccepted = st == "True"
			if st == "False" {
				return established, false, fmt.Errorf("NamesAccepted=False: %v", c["message"])
			}
		}
	}
	return established, namesAccepted, nil
}
