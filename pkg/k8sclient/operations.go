package k8sclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

var (
	crdGVR           = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	certificateGVR   = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	clusterIssuerGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}
)

// ApplyManifestFile applies a single manifest file through the Kubernetes API.
func ApplyManifestFile(ctx context.Context, clients *Clients, path, namespace string) ([]ApplyResult, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- callers pass repo-controlled setup paths or already-validated CLI paths.
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes manifest %s: %w", path, err)
	}
	return ApplyManifestYAML(ctx, clients, data, namespace)
}

// ApplyManifestDir applies all .yaml/.yml files in a directory in lexical order.
func ApplyManifestDir(ctx context.Context, clients *Clients, dir, namespace string) ([]ApplyResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes manifest directory %s: %w", dir, err)
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	sort.Strings(paths)
	var all []ApplyResult
	for _, path := range paths {
		results, err := ApplyManifestFile(ctx, clients, path, namespace)
		if err != nil {
			return all, err
		}
		all = append(all, results...)
	}
	return all, nil
}

// EnsureNamespace creates or updates a namespace, preserving existing labels
// unless the same label key is supplied.
func EnsureNamespace(ctx context.Context, clients *Clients, name string, labels map[string]string) error {
	if clients == nil || clients.Clientset == nil {
		return fmt.Errorf("kubernetes clientset cannot be nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("namespace name cannot be empty")
	}
	nsClient := clients.Clientset.CoreV1().Namespaces()
	_, err := nsClient.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = nsClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create namespace %s: %w", name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get namespace %s: %w", name, err)
	}
	if len(labels) == 0 {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := nsClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		for key, value := range labels {
			current.Labels[key] = value
		}
		_, err = nsClient.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

// ConfigMapData returns a ConfigMap's data, or an empty map when it does not exist.
func ConfigMapData(ctx context.Context, clients *Clients, namespace, name string) (map[string]string, error) {
	cm, err := clients.Clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get configmap %s/%s: %w", namespace, name, err)
	}
	if cm.Data == nil {
		return map[string]string{}, nil
	}
	return cloneStringMap(cm.Data), nil
}

// SecretStringDataValue returns one decoded Secret data value, or empty string
// when either the Secret or key does not exist.
func SecretStringDataValue(ctx context.Context, clients *Clients, namespace, name, key string) (string, error) {
	secret, err := clients.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	value := secret.Data[key]
	if len(value) == 0 {
		return "", nil
	}
	return string(value), nil
}

// UpsertOpaqueSecretStringData creates or updates an Opaque Secret from string data.
func UpsertOpaqueSecretStringData(ctx context.Context, clients *Clients, namespace, name string, data map[string]string) error {
	byteData := make(map[string][]byte, len(data))
	for key, value := range data {
		byteData[key] = []byte(value)
	}
	return upsertSecret(ctx, clients, namespace, name, corev1.SecretTypeOpaque, byteData)
}

// UpsertDockerConfigSecret creates or updates a dockerconfigjson image pull Secret.
func UpsertDockerConfigSecret(ctx context.Context, clients *Clients, namespace, name, registry, username, password string) error {
	dockerCfg := map[string]any{
		"auths": map[string]any{
			registry: map[string]string{
				"username": username,
				"password": password,
				"auth":     base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", username, password))),
			},
		},
	}
	dockerCfgJSON, err := json.Marshal(dockerCfg)
	if err != nil {
		return fmt.Errorf("marshal docker config: %w", err)
	}
	return upsertSecret(ctx, clients, namespace, name, corev1.SecretTypeDockerConfigJson, map[string][]byte{
		corev1.DockerConfigJsonKey: dockerCfgJSON,
	})
}

func upsertSecret(ctx context.Context, clients *Clients, namespace, name string, secretType corev1.SecretType, data map[string][]byte) error {
	secretClient := clients.Clientset.CoreV1().Secrets(namespace)
	_, err := secretClient.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secretClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       secretType,
			Data:       data,
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create secret %s/%s: %w", namespace, name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := secretClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		current.Type = secretType
		if current.Data == nil {
			current.Data = map[string][]byte{}
		}
		for key, value := range data {
			current.Data[key] = value
		}
		_, err = secretClient.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

// NodeArchitectures returns sorted unique node CPU architectures.
func NodeArchitectures(ctx context.Context, clients *Clients) ([]string, error) {
	nodes, err := clients.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	seen := map[string]struct{}{}
	for _, node := range nodes.Items {
		arch := strings.TrimSpace(node.Status.NodeInfo.Architecture)
		if arch != "" {
			seen[arch] = struct{}{}
		}
	}
	archs := make([]string, 0, len(seen))
	for arch := range seen {
		archs = append(archs, arch)
	}
	sort.Strings(archs)
	return archs, nil
}

// PersistentVolumeClaimStorage returns the requested storage size for a PVC.
func PersistentVolumeClaimStorage(ctx context.Context, clients *Clients, namespace, name string) (string, error) {
	pvc, err := clients.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get pvc %s/%s: %w", namespace, name, err)
	}
	if pvc.Spec.Resources.Requests == nil {
		return "", nil
	}
	return pvc.Spec.Resources.Requests.Storage().String(), nil
}

// UpdatePersistentVolumeClaimStorage updates the requested storage size on a PVC.
func UpdatePersistentVolumeClaimStorage(ctx context.Context, clients *Clients, namespace, name, storageSize string) error {
	quantity, err := resource.ParseQuantity(storageSize)
	if err != nil {
		return fmt.Errorf("parse pvc storage size %q: %w", storageSize, err)
	}
	pvcClient := clients.Clientset.CoreV1().PersistentVolumeClaims(namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pvc, err := pvcClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pvc.Spec.Resources.Requests == nil {
			pvc.Spec.Resources.Requests = corev1.ResourceList{}
		}
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = quantity
		_, err = pvcClient.Update(ctx, pvc, metav1.UpdateOptions{})
		return err
	})
}

// CheckCRDExists verifies that a CRD exists.
func CheckCRDExists(ctx context.Context, clients *Clients, name string) error {
	if _, err := clients.Dynamic.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("get crd %s: %w", name, err)
	}
	return nil
}

// SetDeploymentEnv updates env vars on the first container in a Deployment.
func SetDeploymentEnv(ctx context.Context, clients *Clients, namespace, name string, literals map[string]string, secretName string, secretKeys []string) error {
	deployClient := clients.Clientset.AppsV1().Deployments(namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deploy, err := deployClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(deploy.Spec.Template.Spec.Containers) == 0 {
			return fmt.Errorf("deployment %s/%s has no containers", namespace, name)
		}
		env := deploy.Spec.Template.Spec.Containers[0].Env
		for key, value := range literals {
			env = upsertEnvVar(env, corev1.EnvVar{Name: key, Value: value})
		}
		for _, key := range secretKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			env = upsertEnvVar(env, corev1.EnvVar{
				Name: key,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  key,
				}},
			})
		}
		deploy.Spec.Template.Spec.Containers[0].Env = env
		_, err = deployClient.Update(ctx, deploy, metav1.UpdateOptions{})
		return err
	})
}

func upsertEnvVar(env []corev1.EnvVar, next corev1.EnvVar) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == next.Name {
			env[i] = next
			return env
		}
	}
	return append(env, next)
}

// RestartDeployment triggers a Deployment rollout by updating the standard restart annotation.
func RestartDeployment(ctx context.Context, clients *Clients, namespace, name string, now time.Time) error {
	deployClient := clients.Clientset.AppsV1().Deployments(namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deploy, err := deployClient.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = now.Format(time.RFC3339)
		_, err = deployClient.Update(ctx, deploy, metav1.UpdateOptions{})
		return err
	})
}

// WaitForDeploymentAvailable waits until a Deployment reports at least one available replica.
func WaitForDeploymentAvailable(ctx context.Context, clients *Clients, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := clients.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || IsTransient(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return deploy.Status.AvailableReplicas > 0, nil
	})
}

// WaitForDeploymentRolledOut waits until the Deployment's current generation
// has fully rolled out.
func WaitForDeploymentRolledOut(ctx context.Context, clients *Clients, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		deploy, err := clients.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || IsTransient(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		for _, condition := range deploy.Status.Conditions {
			if condition.Type == appsv1.DeploymentProgressing &&
				condition.Status == corev1.ConditionFalse &&
				condition.Reason == "ProgressDeadlineExceeded" {
				return false, fmt.Errorf("deployment %s/%s rollout exceeded progress deadline", namespace, name)
			}
		}
		desired := int32(1)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		if deploy.Status.ObservedGeneration < deploy.Generation {
			return false, nil
		}
		if deploy.Status.UpdatedReplicas < desired {
			return false, nil
		}
		if deploy.Status.Replicas > deploy.Status.UpdatedReplicas {
			return false, nil
		}
		if deploy.Status.AvailableReplicas < desired {
			return false, nil
		}
		return true, nil
	})
}

// WaitForStatefulSetReady waits until a StatefulSet reports all desired replicas ready.
func WaitForStatefulSetReady(ctx context.Context, clients *Clients, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		statefulSet, err := clients.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || IsTransient(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		desired := int32(1)
		if statefulSet.Spec.Replicas != nil {
			desired = *statefulSet.Spec.Replicas
		}
		return statefulSet.Status.ReadyReplicas >= desired &&
			statefulSet.Status.UpdatedReplicas >= desired &&
			statefulSet.Status.ObservedGeneration >= statefulSet.Generation, nil
	})
}

// WaitForDaemonSetReady waits until a DaemonSet reports all scheduled replicas ready.
func WaitForDaemonSetReady(ctx context.Context, clients *Clients, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		daemonSet, err := clients.Clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || IsTransient(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		desired := daemonSet.Status.DesiredNumberScheduled
		if desired == 0 {
			return false, nil
		}
		return daemonSet.Status.NumberReady >= desired &&
			daemonSet.Status.UpdatedNumberScheduled >= desired &&
			daemonSet.Status.ObservedGeneration >= daemonSet.Generation, nil
	})
}

// WaitForWorkloadRollout waits until a supported workload kind is ready.
func WaitForWorkloadRollout(ctx context.Context, clients *Clients, namespace, kind, name string, timeout time.Duration) error {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "deploy", "deployments":
		return WaitForDeploymentRolledOut(ctx, clients, namespace, name, timeout)
	case "statefulset", "statefulsets", "sts":
		return WaitForStatefulSetReady(ctx, clients, namespace, name, timeout)
	case "daemonset", "daemonsets", "ds":
		return WaitForDaemonSetReady(ctx, clients, namespace, name, timeout)
	default:
		return fmt.Errorf("unsupported workload kind %q", kind)
	}
}

// DeleteJob deletes a Job and waits briefly for it to disappear.
func DeleteJob(ctx context.Context, clients *Clients, namespace, name string, timeout time.Duration) error {
	jobs := clients.Clientset.BatchV1().Jobs(namespace)
	deletePolicy := metav1.DeletePropagationBackground
	err := jobs.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &deletePolicy})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete job %s/%s: %w", namespace, name, err)
	}
	return wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := jobs.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

// WaitForJobComplete waits until a Job completes or fails.
func WaitForJobComplete(ctx context.Context, clients *Clients, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		job, err := clients.Clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		for _, condition := range job.Status.Conditions {
			if condition.Type == "Complete" && condition.Status == corev1.ConditionTrue {
				return true, nil
			}
			if condition.Type == "Failed" && condition.Status == corev1.ConditionTrue {
				return false, fmt.Errorf("job %s/%s failed", namespace, name)
			}
		}
		return false, nil
	})
}

// ListDeploymentNamespacesByName returns namespaces containing a Deployment with the given name.
func ListDeploymentNamespacesByName(ctx context.Context, clients *Clients, name string) ([]string, error) {
	deployments, err := clients.Clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	seen := map[string]struct{}{}
	for _, deploy := range deployments.Items {
		if deploy.Name == name {
			seen[deploy.Namespace] = struct{}{}
		}
	}
	namespaces := make([]string, 0, len(seen))
	for namespace := range seen {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

func GetDeployment(ctx context.Context, clients *Clients, namespace, name string) (*appsv1.Deployment, error) {
	return clients.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
}

func PatchDeploymentJSON(ctx context.Context, clients *Clients, namespace, name string, patch []byte) error {
	_, err := clients.Clientset.AppsV1().Deployments(namespace).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}

func RemoveIngressAnnotation(ctx context.Context, clients *Clients, namespace, name, key string) error {
	return updateIngress(ctx, clients, namespace, name, func(ing *networkingv1.Ingress) {
		delete(ing.Annotations, key)
	})
}

func SetIngressAnnotation(ctx context.Context, clients *Clients, namespace, name, key, value string) error {
	return updateIngress(ctx, clients, namespace, name, func(ing *networkingv1.Ingress) {
		if ing.Annotations == nil {
			ing.Annotations = map[string]string{}
		}
		ing.Annotations[key] = value
	})
}

func updateIngress(ctx context.Context, clients *Clients, namespace, name string, mutate func(*networkingv1.Ingress)) error {
	ingClient := clients.Clientset.NetworkingV1().Ingresses(namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		ing, err := ingClient.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		mutate(ing)
		_, err = ingClient.Update(ctx, ing, metav1.UpdateOptions{})
		return err
	})
}

func ClusterIssuerUsesACME(ctx context.Context, clients *Clients, name string) (bool, error) {
	issuer, err := clients.Dynamic.Resource(clusterIssuerGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("get clusterissuer %s: %w", name, err)
	}
	server, _, _ := unstructured.NestedString(issuer.Object, "spec", "acme", "server")
	return strings.TrimSpace(server) != "", nil
}

func CheckClusterIssuer(ctx context.Context, clients *Clients, name string) error {
	_, err := clients.Dynamic.Resource(clusterIssuerGVR).Get(ctx, name, metav1.GetOptions{})
	return err
}

func CheckCertificate(ctx context.Context, clients *Clients, namespace, name string) error {
	_, err := clients.Dynamic.Resource(certificateGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	return err
}

// GetCertificateDNSNames returns the spec.dnsNames from a cert-manager Certificate.
// Returns nil, nil when the Certificate does not exist or cert-manager CRDs are not installed.
func GetCertificateDNSNames(ctx context.Context, clients *Clients, namespace, name string) ([]string, error) {
	cert, err := clients.Dynamic.Resource(certificateGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil // not found or CRDs absent — nothing to check
	}
	names, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	return names, nil
}

var certificateRequestGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificaterequests"}

// IsNamespaceTerminating returns true when the namespace exists and is stuck in
// Terminating phase (deletionTimestamp set). Returns false when the namespace
// does not exist or is healthy.
func IsNamespaceTerminating(ctx context.Context, clients *Clients, name string) (bool, error) {
	ns, err := clients.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ns.DeletionTimestamp != nil, nil
}

// IsCRDTerminating returns true when the named CRD exists and has a
// deletionTimestamp (stuck in Terminating). Returns false when not found.
func IsCRDTerminating(ctx context.Context, clients *Clients, name string) (bool, error) {
	obj, err := clients.Dynamic.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return obj.GetDeletionTimestamp() != nil, nil
}

// SecretExists returns true when the named Secret exists in namespace.
func SecretExists(ctx context.Context, clients *Clients, namespace, name string) (bool, error) {
	_, err := clients.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetFirstReadyPodName returns the name of the first Ready pod matching
// labelSelector in namespace, or "" when none is found.
func GetFirstReadyPodName(ctx context.Context, clients *Clients, namespace, labelSelector string) (string, error) {
	pods, err := clients.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", err
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				return pod.Name, nil
			}
		}
	}
	return "", nil
}

// DeploymentExists returns true when the named Deployment exists in namespace.
func DeploymentExists(ctx context.Context, clients *Clients, namespace, name string) (bool, error) {
	_, err := clients.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// IsJobFailed returns true when the named Job exists and has at least one
// failed condition, meaning it will not self-recover. Returns false when the
// Job does not exist or its status cannot be determined.
func IsJobFailed(ctx context.Context, clients *Clients, namespace, name string) (bool, error) {
	job, err := clients.Clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, c := range job.Status.Conditions {
		if string(c.Type) == "Failed" && c.Status == "True" {
			return true, nil
		}
	}
	// Also treat a job that has exhausted backoffLimit (Failed pods > 0, no active) as failed.
	return job.Status.Failed > 0 && job.Status.Active == 0 && job.Status.Succeeded == 0, nil
}

// ListFailedCertificateRequestNames returns names of CertificateRequests in namespace
// whose Ready condition is False. Returns nil when the namespace or CRDs don't exist.
func ListFailedCertificateRequestNames(ctx context.Context, clients *Clients, namespace string) ([]string, error) {
	list, err := clients.Dynamic.Resource(certificateRequestGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil // namespace absent or CRDs not installed
	}
	var failed []string
	for _, item := range list.Items {
		conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
		for _, raw := range conditions {
			cond, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if cond["type"] == "Ready" && cond["status"] == "False" {
				failed = append(failed, item.GetName())
				break
			}
		}
	}
	return failed, nil
}

func CertificateOwnersForSecret(ctx context.Context, clients *Clients, namespace, secretName string) ([]string, error) {
	list, err := clients.Dynamic.Resource(certificateGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var owners []string
	for _, item := range list.Items {
		gotSecret, _, _ := unstructured.NestedString(item.Object, "spec", "secretName")
		if gotSecret == secretName {
			owners = append(owners, item.GetName())
		}
	}
	sort.Strings(owners)
	return owners, nil
}

func WaitForCertificateReady(ctx context.Context, clients *Clients, namespace, name string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		cert, err := clients.Dynamic.Resource(certificateGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		conditions, _, _ := unstructured.NestedSlice(cert.Object, "status", "conditions")
		for _, raw := range conditions {
			condition, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if condition["type"] == "Ready" && condition["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	})
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// PruneTerminatedPods deletes pods in a namespace that are terminal leftovers
// of eviction or restart churn: pods in the Failed phase (Evicted, Error,
// ContainerStatusUnknown, ...) and Succeeded pods that no Job owns. Pods
// owned by a Job in the Succeeded phase are left to the Job's own lifecycle.
// Running and Pending pods are never touched. It returns the deleted names.
func PruneTerminatedPods(ctx context.Context, clients *Clients, namespace string) ([]string, error) {
	pods := clients.Clientset.CoreV1().Pods(namespace)
	list, err := pods.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
	}
	var deleted []string
	for _, pod := range list.Items {
		if !isPrunableTerminatedPod(&pod) {
			continue
		}
		err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return deleted, fmt.Errorf("delete terminated pod %s/%s: %w", namespace, pod.Name, err)
		}
		deleted = append(deleted, pod.Name)
	}
	sort.Strings(deleted)
	return deleted, nil
}

func isPrunableTerminatedPod(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	switch pod.Status.Phase {
	case corev1.PodFailed:
		return true
	case corev1.PodSucceeded:
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "Job" {
				return false
			}
		}
		return true
	}
	return false
}
