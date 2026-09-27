package update

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"mcp-runtime/pkg/k8sclient"
)

const (
	CRDActionSkipped = "skipped"
)

var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

type crdDocument struct {
	Name   string
	YAML   string
	Object map[string]any
}

// decodeCRDDocuments splits a filtered CRD bundle into per-object documents.
func decodeCRDDocuments(crdsYAML string) ([]crdDocument, error) {
	crdsYAML = strings.TrimSpace(crdsYAML)
	if crdsYAML == "" {
		return nil, nil
	}
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(crdsYAML), 4096)
	var docs []crdDocument
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode CRD bundle: %w", err)
		}
		if len(obj) == 0 {
			continue
		}
		kind, _ := obj["kind"].(string)
		if kind != "CustomResourceDefinition" {
			return nil, fmt.Errorf("CRD bundle may only contain CustomResourceDefinition objects; found %q", kind)
		}
		meta, _ := obj["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("CustomResourceDefinition missing metadata.name")
		}
		raw, err := yaml.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("encode CustomResourceDefinition %s: %w", name, err)
		}
		docs = append(docs, crdDocument{Name: name, YAML: string(raw), Object: obj})
	}
	return docs, nil
}

// refineCRDPlan GETs each planned CRD and drops those whose live spec already
// matches the release document so dry-run and apply stay maximally targeted.
func refineCRDPlan(ctx context.Context, clients *k8sclient.Clients, plan *Plan) ([]CRDResult, error) {
	if plan == nil || !plan.ApplyCRDs {
		return nil, nil
	}
	if clients == nil || clients.Dynamic == nil {
		return nil, fmt.Errorf("dynamic client required to compare CustomResourceDefinitions")
	}
	docs, err := decodeCRDDocuments(plan.crdsYAML)
	if err != nil {
		return nil, err
	}
	var (
		needed   []crdDocument
		names    []string
		preview  []CRDResult
		yamlDocs []string
	)
	for _, doc := range docs {
		live, err := clients.Dynamic.Resource(crdGVR).Get(ctx, doc.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			needed = append(needed, doc)
			names = append(names, doc.Name)
			yamlDocs = append(yamlDocs, strings.TrimSpace(doc.YAML))
			preview = append(preview, CRDResult{Name: doc.Name, Action: "create"})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get CustomResourceDefinition %s: %w", doc.Name, err)
		}
		if crdSpecEqual(doc.Object, live.Object) {
			preview = append(preview, CRDResult{Name: doc.Name, Action: CRDActionSkipped})
			continue
		}
		needed = append(needed, doc)
		names = append(names, doc.Name)
		yamlDocs = append(yamlDocs, strings.TrimSpace(doc.YAML))
		preview = append(preview, CRDResult{Name: doc.Name, Action: "update"})
	}
	if len(needed) == 0 {
		plan.ApplyCRDs = false
		plan.CRDNames = nil
		plan.crdsYAML = ""
		plan.Warnings = filterWarnings(plan.Warnings, "applies CustomResourceDefinitions")
		plan.Warnings = append(plan.Warnings, "CustomResourceDefinitions already match the release; CRD apply skipped")
		return preview, nil
	}
	plan.CRDNames = names
	plan.crdsYAML = strings.Join(yamlDocs, "\n---\n") + "\n"
	return preview, nil
}

func filterWarnings(warnings []string, dropSubstring string) []string {
	if dropSubstring == "" {
		return warnings
	}
	out := warnings[:0]
	for _, w := range warnings {
		if strings.Contains(w, dropSubstring) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func crdSpecEqual(desired, live map[string]any) bool {
	return reflect.DeepEqual(desired["spec"], live["spec"])
}

// applyReleaseCRDs applies only CRDs that still need updating (after refine),
// then waits Established only for those written in this run.
func applyReleaseCRDs(ctx context.Context, clients *k8sclient.Clients, plan *Plan, timeout time.Duration, progress func(string)) ([]CRDResult, error) {
	if clients == nil {
		return []CRDResult{{Name: "*", Error: "Kubernetes dynamic client is required to apply CustomResourceDefinitions"}}, fmt.Errorf("dynamic client required")
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	docs, err := decodeCRDDocuments(plan.crdsYAML)
	if err != nil {
		return []CRDResult{{Name: "*", Error: err.Error()}}, err
	}
	if len(docs) == 0 {
		return nil, nil
	}

	progress(fmt.Sprintf("Applying CustomResourceDefinitions (%s)", stringsJoin(plan.CRDNames)))
	results := make([]CRDResult, 0, len(docs))
	appliedNames := make([]string, 0, len(docs))
	for _, doc := range docs {
		live, getErr := clients.Dynamic.Resource(crdGVR).Get(ctx, doc.Name, metav1.GetOptions{})
		if getErr == nil && crdSpecEqual(doc.Object, live.Object) {
			results = append(results, CRDResult{Name: doc.Name, Action: CRDActionSkipped})
			progress(fmt.Sprintf("CRD %s skipped (spec matches release)", doc.Name))
			continue
		}
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			results = append(results, CRDResult{Name: doc.Name, Error: getErr.Error()})
			return results, getErr
		}
		applied, err := k8sclient.ApplyManifestYAML(ctx, clients, []byte(doc.YAML), "")
		if err != nil {
			results = append(results, CRDResult{Name: doc.Name, Error: err.Error()})
			return results, err
		}
		action := "configured"
		if len(applied) > 0 {
			action = applied[0].Action
		}
		results = append(results, CRDResult{Name: doc.Name, Action: action})
		appliedNames = append(appliedNames, doc.Name)
		progress(fmt.Sprintf("CRD %s %s", doc.Name, action))
	}

	if len(appliedNames) == 0 {
		progress("All CustomResourceDefinitions already matched the release")
		return results, nil
	}
	progress(fmt.Sprintf("Waiting for CustomResourceDefinitions to become Established (timeout %s)", timeout))
	if err := waitForCRDsEstablished(ctx, clients, appliedNames, timeout); err != nil {
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
			// Established=False is normal while the API server finishes
			// installing; keep polling until timeout instead of failing fast.
			established = st == "True"
		case "NamesAccepted":
			namesAccepted = st == "True"
			if st == "False" {
				return established, false, fmt.Errorf("NamesAccepted=False: %v", c["message"])
			}
		}
	}
	return established, namesAccepted, nil
}
