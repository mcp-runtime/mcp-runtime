package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"mcp-runtime/internal/cli/core"
	sigsyaml "sigs.k8s.io/yaml"
)

const dependencyRevisionAnnotation = "mcpruntime.org/dependency-revision"

type dependencyReader func(namespace, kind, name string) (map[string][]byte, error)
type dependencyReference struct {
	Kind, Name, Key string
	Optional, All   bool
}

func podDependencyReferences(spec corev1.PodSpec) []dependencyReference {
	var refs []dependencyReference
	add := func(kind, name, key string, optional *bool, all bool) {
		refs = append(refs, dependencyReference{kind, name, key, optional != nil && *optional, all})
	}
	containers := append(append([]corev1.Container(nil), spec.Containers...), spec.InitContainers...)
	for _, c := range containers {
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				continue
			}
			if r := e.ValueFrom.SecretKeyRef; r != nil {
				add("Secret", r.Name, r.Key, r.Optional, false)
			}
			if r := e.ValueFrom.ConfigMapKeyRef; r != nil {
				add("ConfigMap", r.Name, r.Key, r.Optional, false)
			}
		}
		for _, e := range c.EnvFrom {
			if r := e.SecretRef; r != nil {
				add("Secret", r.Name, "", r.Optional, true)
			}
			if r := e.ConfigMapRef; r != nil {
				add("ConfigMap", r.Name, "", r.Optional, true)
			}
		}
	}
	items := func(kind, name string, list []corev1.KeyToPath, optional *bool) {
		if len(list) == 0 {
			add(kind, name, "", optional, true)
		}
		for _, item := range list {
			add(kind, name, item.Key, optional, false)
		}
	}
	for _, v := range spec.Volumes {
		if r := v.Secret; r != nil {
			items("Secret", r.SecretName, r.Items, r.Optional)
		}
		if r := v.ConfigMap; r != nil {
			items("ConfigMap", r.Name, r.Items, r.Optional)
		}
		if v.Projected != nil {
			for _, s := range v.Projected.Sources {
				if r := s.Secret; r != nil {
					items("Secret", r.Name, r.Items, r.Optional)
				}
				if r := s.ConfigMap; r != nil {
					items("ConfigMap", r.Name, r.Items, r.Optional)
				}
			}
		}
	}
	return refs
}

// Hash exact consumed keys. JSON frames identity/value boundaries and sorts map
// keys, avoiding delimiter ambiguity and map iteration dependent rollouts.
func dependencyRevision(namespace string, refs []dependencyReference, read dependencyReader) (string, error) {
	values := map[string]any{}
	for _, ref := range refs {
		data, err := read(namespace, ref.Kind, ref.Name)
		if apierrors.IsNotFound(err) && ref.Optional {
			data = nil
			err = nil
		}
		if err != nil {
			return "", fmt.Errorf("read dependency %s/%s in %s: %w", ref.Kind, ref.Name, namespace, err)
		}
		identity, _ := json.Marshal([]string{namespace, ref.Kind, ref.Name, ref.Key})
		if ref.All {
			values[string(identity)] = data
			continue
		}
		value, present := data[ref.Key]
		if !present && !ref.Optional {
			return "", fmt.Errorf("required dependency key %s/%s/%s missing", ref.Kind, ref.Name, ref.Key)
		}
		values[string(identity)] = struct {
			Present bool
			Value   []byte
		}{present, value}
	}
	serialized, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(serialized)
	return hex.EncodeToString(digest[:]), nil
}

func stampDependencyRevisions(content string, read dependencyReader) (string, error) {
	decoder := yaml.NewDecoder(bytes.NewBufferString(content))
	var docs []map[string]any
	local := map[string]map[string][]byte{}
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if len(doc) == 0 {
			continue
		}
		docs = append(docs, doc)
		kind, _ := doc["kind"].(string)
		if kind != "Secret" && kind != "ConfigMap" {
			continue
		}
		data, err := yaml.Marshal(doc)
		if err != nil {
			return "", err
		}
		if kind == "Secret" {
			var obj corev1.Secret
			if err := sigsyaml.Unmarshal(data, &obj); err != nil {
				return "", err
			}
			values := obj.Data
			if values == nil {
				values = map[string][]byte{}
			}
			for key, value := range obj.StringData {
				values[key] = []byte(value)
			}
			local[dependencyResourceKey(obj.Namespace, kind, obj.Name)] = values
		} else {
			var obj corev1.ConfigMap
			if err := sigsyaml.Unmarshal(data, &obj); err != nil {
				return "", err
			}
			values := obj.BinaryData
			if values == nil {
				values = map[string][]byte{}
			}
			for key, value := range obj.Data {
				values[key] = []byte(value)
			}
			local[dependencyResourceKey(obj.Namespace, kind, obj.Name)] = values
		}
	}
	cache := map[string]map[string][]byte{}
	cachedRead := func(ns, kind, name string) (map[string][]byte, error) {
		key := dependencyResourceKey(ns, kind, name)
		if data, ok := local[key]; ok {
			return data, nil
		}
		if data, ok := cache[key]; ok {
			return data, nil
		}
		data, err := read(ns, kind, name)
		if err == nil {
			cache[key] = data
		}
		return data, err
	}
	for _, doc := range docs {
		kind, _ := doc["kind"].(string)
		switch kind {
		case "Deployment", "StatefulSet", "DaemonSet", "Job":
		default:
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		template, _ := spec["template"].(map[string]any)
		podSpec, ok := template["spec"].(map[string]any)
		if !ok {
			continue
		}
		data, err := yaml.Marshal(podSpec)
		if err != nil {
			return "", err
		}
		var pod corev1.PodSpec
		if err := sigsyaml.Unmarshal(data, &pod); err != nil {
			return "", err
		}
		refs := podDependencyReferences(pod)
		if len(refs) == 0 {
			continue
		}
		metadata, _ := doc["metadata"].(map[string]any)
		namespace, _ := metadata["namespace"].(string)
		if namespace == "" {
			namespace = core.ComponentNamespace("platform-api")
		}
		revision, err := dependencyRevision(namespace, refs, cachedRead)
		if err != nil {
			return "", err
		}
		tm, _ := template["metadata"].(map[string]any)
		if tm == nil {
			tm = map[string]any{}
			template["metadata"] = tm
		}
		annotations, _ := tm["annotations"].(map[string]any)
		if annotations == nil {
			annotations = map[string]any{}
			tm["annotations"] = annotations
		}
		annotations[dependencyRevisionAnnotation] = revision
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	for _, doc := range docs {
		if err := encoder.Encode(doc); err != nil {
			return "", err
		}
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}
func dependencyResourceKey(ns, kind, name string) string {
	if ns == "" {
		ns = core.ComponentNamespace("platform-api")
	}
	data, _ := json.Marshal([]string{ns, kind, name})
	return string(data)
}

func stampDependencyRevisionsClientGo(content string) (string, error) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return "", err
	}
	return stampDependencyRevisions(content, func(ns, kind, name string) (map[string][]byte, error) {
		if kind == "Secret" {
			obj, err := clients.Clientset.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return obj.Data, nil
		}
		obj, err := clients.Clientset.CoreV1().ConfigMaps(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		values := obj.BinaryData
		if values == nil {
			values = map[string][]byte{}
		}
		keys := make([]string, 0, len(obj.Data))
		for key := range obj.Data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			values[key] = []byte(obj.Data[key])
		}
		return values, nil
	})
}
