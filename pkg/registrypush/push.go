package registrypush

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"mcp-runtime/pkg/kubeworkload"
)

const defaultImageTarPath = "/tmp/image.tar"

// TransferTokenHeader is the HTTP header carrying one-time registry push tar tokens.
const TransferTokenHeader = "X-Registry-Push-Transfer-Token" // #nosec G101 -- HTTP header name, not credential material.

var (
	waitPodReadyHook     = waitPodReady
	waitPodSucceededHook = waitPodSucceeded
	copyFileToPodHook    = copyFileToPod
	execInPodHook        = execInPod
)

// Config controls in-cluster registry push helper behavior.
type Config struct {
	HelperNamespace string
	SkopeoImage     string
	HelperTimeout   time.Duration
	Hosts           Hosts
	// TarFetchURL, when set, skips exec-based tar copy and runs a one-shot helper
	// pod that downloads the archive from this URL before invoking skopeo.
	TarFetchURL string
	// TarFetchToken is sent as X-Registry-Push-Transfer-Token when fetching TarFetchURL.
	TarFetchToken string
}

// PushDockerArchive pushes a docker save tar to target using a short-lived
// skopeo helper pod inside the cluster.
func PushDockerArchive(ctx context.Context, client kubernetes.Interface, restConfig *rest.Config, tarPath, target string, cfg Config) (retErr error) {
	if client == nil || restConfig == nil {
		return fmt.Errorf("kubernetes client is required")
	}
	tarPath = strings.TrimSpace(tarPath)
	target = strings.TrimSpace(target)
	if tarPath == "" || target == "" {
		return fmt.Errorf("tar path and target are required")
	}
	helperNS := strings.TrimSpace(cfg.HelperNamespace)
	if helperNS == "" {
		helperNS = "registry"
	}
	skopeoImage := strings.TrimSpace(cfg.SkopeoImage)
	if skopeoImage == "" {
		skopeoImage = "quay.io/skopeo/stable:v1.14"
	}
	timeout := cfg.HelperTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	helperName := newHelperName()
	containerName := helperName
	pushTarget := RewritePushTarget(target, cfg.Hosts)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: helperName, Namespace: helperNS},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: boolPtr(false),
			SecurityContext:              kubeworkload.RestrictedPodSecurityContext(),
			Volumes: []corev1.Volume{{
				Name: "tmp",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}},
			Containers: []corev1.Container{{
				Name:            containerName,
				Image:           skopeoImage,
				Command:         helperCommand(cfg, pushTarget),
				SecurityContext: kubeworkload.RestrictedContainerSecurityContext(),
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "tmp",
					MountPath: "/tmp",
				}},
			}},
		},
	}
	if _, err := client.CoreV1().Pods(helperNS).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("start registry push helper pod: %w", err)
	}
	var pushErr error
	defer func() {
		if retErr != nil {
			if detail := helperPodDiagnostics(context.Background(), client, helperNS, helperName, containerName); detail != "" {
				retErr = fmt.Errorf("%w; helper diagnostics: %s", retErr, detail)
			}
		}
		_ = client.CoreV1().Pods(helperNS).Delete(context.Background(), helperName, metav1.DeleteOptions{})
	}()

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if strings.TrimSpace(cfg.TarFetchURL) != "" {
		if pushErr = waitPodSucceededHook(waitCtx, client, helperNS, helperName); pushErr != nil {
			return pushErr
		}
		return nil
	}

	if pushErr = waitPodReadyHook(waitCtx, client, helperNS, helperName); pushErr != nil {
		return pushErr
	}
	if pushErr = copyFileToPodHook(ctx, client, restConfig, helperNS, helperName, containerName, tarPath, defaultImageTarPath); pushErr != nil {
		return fmt.Errorf("copy image tar to helper pod %s/%s: %w", helperNS, helperName, pushErr)
	}

	skopeoArgs := []string{"skopeo", "copy", "--dest-tls-verify=false", "docker-archive:" + defaultImageTarPath, "docker://" + pushTarget}
	if pushErr = execInPodHook(ctx, client, restConfig, helperNS, helperName, containerName, skopeoArgs); pushErr != nil {
		return fmt.Errorf("push image from helper pod %s/%s to %s (requested %s): %w", helperNS, helperName, pushTarget, target, pushErr)
	}
	return nil
}

func helperCommand(cfg Config, pushTarget string) []string {
	if url := strings.TrimSpace(cfg.TarFetchURL); url != "" {
		token := strings.TrimSpace(cfg.TarFetchToken)
		script := fmt.Sprintf(
			"set -eu; curl -fsSL -H %q -o %q %q; skopeo copy --dest-tls-verify=false docker-archive:%q docker://%s",
			TransferTokenHeader+": "+token,
			defaultImageTarPath,
			url,
			defaultImageTarPath,
			pushTarget,
		)
		return []string{"sh", "-c", script}
	}
	return []string{"sh", "-c", "while true; do sleep 3600; done"}
}

func newHelperName() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("registry-pusher-%d", time.Now().UnixNano())
	}
	return "registry-pusher-" + hex.EncodeToString(buf)
}

func waitPodSucceeded(ctx context.Context, client kubernetes.Interface, namespace, name string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("wait for helper pod completion: %w", err)
		}
		if pod.Status.Phase == corev1.PodSucceeded {
			return nil
		}
		if pod.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("helper pod %s/%s failed", namespace, name)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("helper pod %s/%s did not complete: %w", namespace, name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitPodReady(ctx context.Context, client kubernetes.Interface, namespace, name string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("wait for helper pod readiness: %w", err)
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return nil
			}
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("helper pod %s/%s entered terminal phase %s", namespace, name, pod.Status.Phase)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("helper pod %s/%s not ready: %w", namespace, name, ctx.Err())
		case <-ticker.C:
		}
	}
}

func copyFileToPod(ctx context.Context, client kubernetes.Interface, restConfig *rest.Config, namespace, podName, containerName, srcPath, destPath string) error {
	file, err := os.Open(srcPath) // #nosec G304 -- srcPath is a trusted docker save tar from the platform API upload temp dir.
	if err != nil {
		return err
	}
	defer file.Close()

	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: containerName,
		Command:   []string{"sh", "-c", "cat > " + destPath},
		Stdin:     true,
	}, scheme.ParameterCodec)

	fallbackExec, err := newRemoteExecutor(restConfig, req.URL())
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	if err := fallbackExec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  file,
		Stderr: &stderr,
	}); err != nil {
		return execStreamError(err, stderr.String(), "")
	}
	return nil
}

func helperPodDiagnostics(ctx context.Context, client kubernetes.Interface, namespace, podName, containerName string) string {
	if client == nil {
		return ""
	}
	var parts []string
	if pod, err := client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{}); err == nil {
		parts = append(parts, fmt.Sprintf("phase=%s reason=%q message=%q", pod.Status.Phase, pod.Status.Reason, pod.Status.Message))
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name != containerName {
				continue
			}
			if waiting := cs.State.Waiting; waiting != nil {
				parts = append(parts, fmt.Sprintf("waiting reason=%q message=%q", waiting.Reason, waiting.Message))
			}
			if terminated := cs.State.Terminated; terminated != nil {
				parts = append(parts, fmt.Sprintf("terminated exit=%d reason=%q message=%q", terminated.ExitCode, terminated.Reason, terminated.Message))
			}
		}
	}
	req := client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: containerName})
	if logs, err := req.DoRaw(ctx); err == nil {
		if trimmed := strings.TrimSpace(string(logs)); trimmed != "" {
			if len(trimmed) > 2048 {
				trimmed = trimmed[len(trimmed)-2048:]
			}
			parts = append(parts, "logs="+trimmed)
		}
	}
	return strings.Join(parts, "; ")
}

func execStreamError(err error, stderr, stdout string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func execInPod(ctx context.Context, client kubernetes.Interface, restConfig *rest.Config, namespace, podName, containerName string, command []string) error {
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)

	exec, err := newRemoteExecutor(restConfig, req.URL())
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return execStreamError(err, stderr.String(), stdout.String())
	}
	return nil
}

func newRemoteExecutor(restConfig *rest.Config, targetURL *url.URL) (remotecommand.Executor, error) {
	websocketExec, wsErr := remotecommand.NewWebSocketExecutor(restConfig, "GET", targetURL.String())
	spdyExec, spdyErr := remotecommand.NewSPDYExecutor(restConfig, "POST", targetURL)
	if wsErr == nil && spdyErr == nil {
		return remotecommand.NewFallbackExecutor(websocketExec, spdyExec, func(error) bool { return true })
	}
	if wsErr == nil {
		return websocketExec, nil
	}
	if spdyErr == nil {
		return spdyExec, nil
	}
	return nil, fmt.Errorf("create exec transport: websocket: %w; spdy: %v", wsErr, spdyErr)
}

// EnsureHelperNamespace verifies the helper namespace exists.
func EnsureHelperNamespace(ctx context.Context, client kubernetes.Interface, namespace string) error {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return fmt.Errorf("helper namespace is required")
	}
	_, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("helper namespace %q not found", namespace)
	}
	return err
}

func boolPtr(v bool) *bool {
	return &v
}

// CopyReaderToTempFile writes r to a temporary docker archive file and returns its path.
func CopyReaderToTempFile(r io.Reader) (string, error) {
	tmp, err := os.CreateTemp("", "mcp-registry-push-*.tar")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
