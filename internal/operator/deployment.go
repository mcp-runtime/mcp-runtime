package operator

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/kubeworkload"
)

func (r *MCPServerReconciler) reconcileDeployment(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	logger := log.FromContext(ctx)

	image, err := r.resolveImage(ctx, mcpServer)
	if err != nil {
		return err
	}
	if err := r.ensureWorkloadServiceAccount(ctx, mcpServer.Namespace); err != nil {
		return err
	}
	if len(mcpServer.Spec.ImagePullSecrets) == 0 {
		if err := r.ensureRegistryPullSecret(ctx, mcpServer.Namespace); err != nil {
			return err
		}
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcpServer.Name,
			Namespace: mcpServer.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		selectorLabels := map[string]string{
			"app":                          mcpServer.Name,
			"mcpruntime.org/rollout-track": "stable",
		}
		templateLabels := map[string]string{
			"app":                          mcpServer.Name,
			"app.kubernetes.io/managed-by": "mcp-runtime",
			"mcpruntime.org/rollout-track": "stable",
		}
		replicas := desiredStableReplicas(mcpServer)

		deployment.Labels = map[string]string{
			"app":                          mcpServer.Name,
			"app.kubernetes.io/managed-by": "mcp-runtime",
			"mcpruntime.org/rollout-track": "stable",
		}

		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Strategy: deploymentStrategy(mcpServer),
		}
		deployment.Spec.Template.ObjectMeta.Labels = templateLabels

		containers, volumes, err := r.buildDeploymentContainers(mcpServer, image)
		if err != nil {
			return err
		}
		deployment.Spec.Template.Spec = corev1.PodSpec{
			ImagePullSecrets: r.buildImagePullSecrets(mcpServer),
			Containers:       containers,
			Volumes:          volumes,
		}
		kubeworkload.ApplyRestrictedPodDefaults(&deployment.Spec.Template.Spec)

		if err := ctrl.SetControllerReference(mcpServer, deployment, r.Scheme); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return err
	}

	if op != controllerutil.OperationResultNone {
		logger.Info("Deployment reconciled", "operation", op, "name", deployment.Name)
	}

	return nil
}

func (r *MCPServerReconciler) reconcileCanaryDeployment(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	if !canaryEnabled(mcpServer) {
		existing := &appsv1.Deployment{}
		err := r.Get(ctx, types.NamespacedName{Name: canaryDeploymentName(mcpServer.Name), Namespace: mcpServer.Namespace}, existing)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.Delete(ctx, existing)
	}

	logger := log.FromContext(ctx)
	image, err := r.resolveImage(ctx, mcpServer)
	if err != nil {
		return err
	}
	if err := r.ensureWorkloadServiceAccount(ctx, mcpServer.Namespace); err != nil {
		return err
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      canaryDeploymentName(mcpServer.Name),
			Namespace: mcpServer.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		replicas := int32(0)
		if mcpServer.Spec.Rollout != nil && mcpServer.Spec.Rollout.CanaryReplicas != nil {
			replicas = *mcpServer.Spec.Rollout.CanaryReplicas
		}
		selectorLabels := map[string]string{
			"app":                          mcpServer.Name,
			"mcpruntime.org/rollout-track": "canary",
		}
		deployment.Labels = map[string]string{
			"app":                          mcpServer.Name,
			"app.kubernetes.io/managed-by": "mcp-runtime",
			"mcpruntime.org/rollout-track": "canary",
		}
		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Strategy: deploymentStrategy(mcpServer),
		}
		deployment.Spec.Template.ObjectMeta.Labels = map[string]string{
			"app":                          mcpServer.Name,
			"app.kubernetes.io/managed-by": "mcp-runtime",
			"mcpruntime.org/rollout-track": "canary",
		}
		containers, volumes, err := r.buildDeploymentContainers(mcpServer, image)
		if err != nil {
			return err
		}
		deployment.Spec.Template.Spec = corev1.PodSpec{
			ImagePullSecrets: r.buildImagePullSecrets(mcpServer),
			Containers:       containers,
			Volumes:          volumes,
		}
		kubeworkload.ApplyRestrictedPodDefaults(&deployment.Spec.Template.Spec)
		if err := ctrl.SetControllerReference(mcpServer, deployment, r.Scheme); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if op != controllerutil.OperationResultNone {
		logger.Info("Canary deployment reconciled", "operation", op, "name", deployment.Name)
	}
	return nil
}

func (r *MCPServerReconciler) buildDeploymentContainers(mcpServer *mcpv1alpha1.MCPServer, image string) ([]corev1.Container, []corev1.Volume, error) {
	container := corev1.Container{
		Name:            mcpServer.Name,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: mcpServer.Spec.Port,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env:             r.buildServerEnvVars(mcpServer),
		SecurityContext: kubeworkload.RestrictedContainerSecurityContext(),
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(mcpServer.Spec.Port)},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(mcpServer.Spec.Port)},
			},
			InitialDelaySeconds: 3,
			PeriodSeconds:       5,
		},
	}

	if err := applyContainerResources(&container, mcpServer.Spec.Resources); err != nil {
		return nil, nil, err
	}

	containers := []corev1.Container{container}
	var volumes []corev1.Volume
	if gatewayEnabled(mcpServer) {
		gatewayContainer, err := r.buildGatewayContainer(mcpServer)
		if err != nil {
			return nil, nil, err
		}
		containers = append(containers, gatewayContainer)
		volumes = append(volumes, corev1.Volume{
			Name: gatewayPolicyVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: gatewayPolicyConfigMapName(mcpServer.Name)},
				},
			},
		})
		if r.usesAdapterCertificates(mcpServer) {
			volumes = append(volumes, corev1.Volume{
				Name: gatewayTLSVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: gatewayTLSSecretName(mcpServer),
					},
				},
			})
		}
	}

	return containers, volumes, nil
}

// applyContainerResources sets container resource requests and limits.
// It applies defaults first, then overrides with user-specified values.
func applyContainerResources(container *corev1.Container, resources mcpv1alpha1.ResourceRequirements) error {
	// Initialize maps
	if container.Resources.Requests == nil {
		container.Resources.Requests = corev1.ResourceList{}
	}
	if container.Resources.Limits == nil {
		container.Resources.Limits = corev1.ResourceList{}
	}

	// Apply defaults
	container.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(defaultRequestCPU)
	container.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(defaultRequestMemory)
	container.Resources.Limits[corev1.ResourceCPU] = resource.MustParse(defaultLimitCPU)
	container.Resources.Limits[corev1.ResourceMemory] = resource.MustParse(defaultLimitMemory)

	// Override with user-specified values
	if resources.Requests != nil {
		if resources.Requests.CPU != "" {
			cpu, err := resource.ParseQuantity(resources.Requests.CPU)
			if err != nil {
				contextMap := map[string]any{
					"resource": "cpu",
					"type":     "request",
					"value":    resources.Requests.CPU,
				}
				return wrapOperatorError(err, fmt.Sprintf("invalid CPU request %q", resources.Requests.CPU), contextMap)
			}
			container.Resources.Requests[corev1.ResourceCPU] = cpu
		}
		if resources.Requests.Memory != "" {
			mem, err := resource.ParseQuantity(resources.Requests.Memory)
			if err != nil {
				contextMap := map[string]any{
					"resource": "memory",
					"type":     "request",
					"value":    resources.Requests.Memory,
				}
				return wrapOperatorError(err, fmt.Sprintf("invalid memory request %q", resources.Requests.Memory), contextMap)
			}
			container.Resources.Requests[corev1.ResourceMemory] = mem
		}
	}

	if resources.Limits != nil {
		if resources.Limits.CPU != "" {
			cpu, err := resource.ParseQuantity(resources.Limits.CPU)
			if err != nil {
				contextMap := map[string]any{
					"resource": "cpu",
					"type":     "limit",
					"value":    resources.Limits.CPU,
				}
				return wrapOperatorError(err, fmt.Sprintf("invalid CPU limit %q", resources.Limits.CPU), contextMap)
			}
			container.Resources.Limits[corev1.ResourceCPU] = cpu
		}
		if resources.Limits.Memory != "" {
			mem, err := resource.ParseQuantity(resources.Limits.Memory)
			if err != nil {
				contextMap := map[string]any{
					"resource": "memory",
					"type":     "limit",
					"value":    resources.Limits.Memory,
				}
				return wrapOperatorError(err, fmt.Sprintf("invalid memory limit %q", resources.Limits.Memory), contextMap)
			}
			container.Resources.Limits[corev1.ResourceMemory] = mem
		}
	}

	return nil
}

// ensureRegistryPullSecret creates or updates the provisioned registry pull
// secret in the MCPServer's namespace so that the operator-injected
// mcp-gateway sidecar (and the user image) can be pulled even when the
// namespace was created after setup. It is a no-op when ProvisionedRegistry
// is not configured or when the spec provides user-defined pull secrets.
func (r *MCPServerReconciler) ensureRegistryPullSecret(ctx context.Context, namespace string) error {
	if r.ProvisionedRegistry == nil || r.ProvisionedRegistry.URL == "" ||
		r.ProvisionedRegistry.Username == "" || r.ProvisionedRegistry.Password == "" {
		return nil
	}
	secretName := r.ProvisionedRegistry.SecretName
	if secretName == "" {
		secretName = DefaultRegistrySecretName
	}
	auth := base64.StdEncoding.EncodeToString(
		[]byte(r.ProvisionedRegistry.Username + ":" + r.ProvisionedRegistry.Password),
	)
	dockerconfig := fmt.Sprintf(
		`{"auths":{%q:{"username":%q,"password":%q,"auth":%q}}}`,
		r.ProvisionedRegistry.URL,
		r.ProvisionedRegistry.Username,
		r.ProvisionedRegistry.Password,
		auth,
	)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Type = corev1.SecretTypeDockerConfigJson
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}
		secret.Data[corev1.DockerConfigJsonKey] = []byte(dockerconfig)
		return nil
	})
	return err
}

func (r *MCPServerReconciler) ensureWorkloadServiceAccount(ctx context.Context, namespace string) error {
	desiredSA := kubeworkload.ServiceAccount(namespace)
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desiredSA.Name,
			Namespace: desiredSA.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.AutomountServiceAccountToken = desiredSA.AutomountServiceAccountToken
		return nil
	})
	return err
}

func (r *MCPServerReconciler) resolveImage(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (string, error) {
	logger := log.FromContext(ctx)

	image := mcpServer.Spec.Image
	// Append tag only if the image does not already include a tag or digest.
	if mcpServer.Spec.ImageTag != "" && !imageHasTagOrDigest(image) {
		image = fmt.Sprintf("%s:%s", image, mcpServer.Spec.ImageTag)
	}

	regOverride := mcpServer.Spec.RegistryOverride
	if mcpServer.Spec.UseProvisionedRegistry {
		if r.ProvisionedRegistry != nil && r.ProvisionedRegistry.URL != "" {
			regOverride = r.ProvisionedRegistry.URL
		} else if regOverride == "" {
			// Fall back to the pullable registry host rather than the backend service endpoint.
			regOverride = DefaultOperatorConfig.RegistryPullHost
			logger.Info("useProvisionedRegistry set without ProvisionedRegistry config; falling back to registry pull host", "mcpServer", mcpServer.Name, "registry", regOverride)
		}
	}
	if regOverride != "" {
		image = rewriteRegistry(image, regOverride)
	}

	return image, nil
}

func (r *MCPServerReconciler) resolveGatewayImage(mcpServer *mcpv1alpha1.MCPServer) (string, error) {
	if !gatewayEnabled(mcpServer) {
		return "", nil
	}

	image := strings.TrimSpace(mcpServer.Spec.Gateway.Image)
	if image == "" {
		image = strings.TrimSpace(r.GatewayProxyImage)
	}
	if image != "" {
		return image, nil
	}

	contextMap := map[string]any{
		"mcpServer": mcpServer.Name,
		"namespace": mcpServer.Namespace,
	}
	return "", newOperatorError("gateway.image is required when gateway.enabled is true (set spec.gateway.image or MCP_GATEWAY_PROXY_IMAGE on the operator)", contextMap)
}

// gatewayExternalBaseURL builds the public base URL the gateway advertises in
// its protected-resource metadata and WWW-Authenticate challenges. The gateway
// runs behind the ingress and never terminates TLS itself, so it cannot infer
// the scheme from the request: hardcoding http:// there makes the advertised
// canonical resource disagree with the https:// audience a token is issued for,
// and every authenticated call fails closed with 401.
func (r *MCPServerReconciler) gatewayExternalBaseURL(mcpServer *mcpv1alpha1.MCPServer) string {
	return mcpServer.PublicBaseURL(r.publicURLOptions())
}

func (r *MCPServerReconciler) publicURLOptions() mcpv1alpha1.PublicURLOptions {
	return mcpv1alpha1.PublicURLOptions{
		DefaultIngressHost: r.DefaultIngressHost,
		DefaultIngressTLS:  r.DefaultIngressTLS,
	}
}

func (r *MCPServerReconciler) buildGatewayContainer(mcpServer *mcpv1alpha1.MCPServer) (corev1.Container, error) {
	image, err := r.resolveGatewayImage(mcpServer)
	if err != nil {
		return corev1.Container{}, err
	}

	port := mcpServer.Spec.Gateway.Port
	metricsPort := int32(DefaultGatewayMetricsPort)
	envVars := []corev1.EnvVar{
		{Name: "PORT", Value: strconv.Itoa(int(port))},
		{Name: "METRICS_PORT", Value: strconv.Itoa(int(metricsPort))},
		{Name: "UPSTREAM_URL", Value: mcpServer.Spec.Gateway.UpstreamURL},
		{Name: "POLICY_FILE", Value: gatewayPolicyFilePath},
		{Name: "MCP_SERVER_NAME", Value: mcpServer.Name},
		{Name: "MCP_SERVER_NAMESPACE", Value: mcpServer.Namespace},
		{Name: "MCP_CLUSTER_NAME", Value: strings.TrimSpace(r.ClusterName)},
		// The public issuer is retained in the policy for JWT validation, while
		// this in-cluster URL keeps gateway JWKS discovery off the workstation
		// port-forward used by local clients.
		{Name: "OAUTH_INTERNAL_ISSUER_URL", Value: r.oauthInternalIssuerURL()},
	}
	if externalBaseURL := r.gatewayExternalBaseURL(mcpServer); externalBaseURL != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "EXTERNAL_BASE_URL", Value: externalBaseURL})
	}
	if endpoint := strings.TrimSpace(r.GatewayOTLPEndpoint); endpoint != "" {
		envVars = append(envVars,
			corev1.EnvVar{Name: "OTEL_SERVICE_NAME", Value: mcpServer.Name + "-gateway"},
			corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: endpoint},
		)
	}
	if mcpServer.Spec.Policy != nil {
		envVars = append(envVars,
			corev1.EnvVar{Name: "POLICY_MODE", Value: string(mcpServer.Spec.Policy.Mode)},
			corev1.EnvVar{Name: "POLICY_DEFAULT_DECISION", Value: string(mcpServer.Spec.Policy.DefaultDecision)},
			corev1.EnvVar{Name: "POLICY_VERSION", Value: mcpServer.Spec.Policy.PolicyVersion},
		)
	}
	if r.usesAdapterCertificates(mcpServer) {
		envVars = append(envVars,
			corev1.EnvVar{Name: "TLS_CERT_FILE", Value: gatewayTLSMountDir + "/tls.crt"},
			corev1.EnvVar{Name: "TLS_KEY_FILE", Value: gatewayTLSMountDir + "/tls.key"},
			corev1.EnvVar{Name: "TLS_CLIENT_CA_FILE", Value: gatewayTLSMountDir + "/ca.crt"},
		)
		// Pin the ingress identity so only the Traefik client certificate (not
		// any other identity-CA-signed cert) is accepted over the re-encrypted hop.
		if proxyID := r.traefikProxySPIFFEID(mcpServer); proxyID != "" {
			envVars = append(envVars, corev1.EnvVar{Name: "TRUSTED_PROXY_SPIFFE_ID", Value: proxyID})
		}
		envVars = append(envVars, corev1.EnvVar{Name: "VERIFIED_SPIFFE_HEADER", Value: verifiedSPIFFEHeader})
	}
	if mcpServer.Spec.Gateway.StripPrefix != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "STRIP_PREFIX", Value: mcpServer.Spec.Gateway.StripPrefix})
	}
	if r.analyticsEnabled(mcpServer) {
		analytics := mcpServer.Spec.Analytics
		ingestURL := ""
		source := mcpServer.Name + defaultAnalyticsSourceSuffix
		eventType := defaultAnalyticsEventType
		var apiKeyRef *mcpv1alpha1.SecretKeyRef
		if analytics != nil {
			if v := strings.TrimSpace(analytics.IngestURL); v != "" {
				ingestURL = v
			}
			if v := strings.TrimSpace(analytics.Source); v != "" {
				source = v
			}
			if v := strings.TrimSpace(analytics.EventType); v != "" {
				eventType = v
			}
			apiKeyRef = analytics.APIKeySecretRef
		}
		if ingestURL == "" {
			ingestURL = strings.TrimSpace(r.DefaultAnalyticsIngestURL)
		}
		envVars = append(envVars,
			corev1.EnvVar{Name: "ANALYTICS_INGEST_URL", Value: ingestURL},
			corev1.EnvVar{Name: "ANALYTICS_SOURCE", Value: source},
			corev1.EnvVar{Name: "ANALYTICS_EVENT_TYPE", Value: eventType},
		)
		if apiKeyRef != nil {
			envVars = append(envVars, corev1.EnvVar{
				Name: "ANALYTICS_API_KEY",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: apiKeyRef.Name},
						Key:                  apiKeyRef.Key,
					},
				},
			})
		}
	}

	container := corev1.Container{
		Name:            "mcp-gateway",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{
				Name:          "gateway",
				ContainerPort: port,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "metrics",
				ContainerPort: metricsPort,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env:             envVars,
		SecurityContext: kubeworkload.RestrictedReadOnlyContainerSecurityContext(),
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      gatewayPolicyVolumeName,
				MountPath: gatewayPolicyMountDir,
				ReadOnly:  true,
			},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/ready",
					Port: intstr.FromInt32(port),
				},
			},
			InitialDelaySeconds: 3,
			PeriodSeconds:       5,
		},
	}
	if r.usesAdapterCertificates(mcpServer) {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      gatewayTLSVolumeName,
			MountPath: gatewayTLSMountDir,
			ReadOnly:  true,
		})
		container.ReadinessProbe.ProbeHandler = corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
		}
	}
	gatewayResources := mcpv1alpha1.ResourceRequirements{}
	if mcpServer.Spec.Gateway != nil && mcpServer.Spec.Gateway.Resources != nil {
		gatewayResources = *mcpServer.Spec.Gateway.Resources
	}
	if err := applyContainerResources(&container, gatewayResources); err != nil {
		return corev1.Container{}, err
	}
	return container, nil
}
func desiredStableReplicas(mcpServer *mcpv1alpha1.MCPServer) int32 {
	if mcpServer.Spec.Replicas == nil {
		return 1
	}
	replicas := *mcpServer.Spec.Replicas
	if canaryEnabled(mcpServer) && mcpServer.Spec.Rollout != nil && mcpServer.Spec.Rollout.CanaryReplicas != nil {
		replicas -= *mcpServer.Spec.Rollout.CanaryReplicas
	}
	if replicas < 0 {
		return 0
	}
	return replicas
}

func deploymentStrategy(mcpServer *mcpv1alpha1.MCPServer) appsv1.DeploymentStrategy {
	if mcpServer.Spec.Rollout == nil || mcpServer.Spec.Rollout.Strategy == "" || mcpServer.Spec.Rollout.Strategy == mcpv1alpha1.RolloutStrategyRollingUpdate || mcpServer.Spec.Rollout.Strategy == mcpv1alpha1.RolloutStrategyCanary {
		maxUnavailable := intstr.FromString("25%")
		maxSurge := intstr.FromString("25%")
		if mcpServer.Spec.Rollout != nil {
			if mcpServer.Spec.Rollout.MaxUnavailable != "" {
				maxUnavailable = intstr.Parse(mcpServer.Spec.Rollout.MaxUnavailable)
			}
			if mcpServer.Spec.Rollout.MaxSurge != "" {
				maxSurge = intstr.Parse(mcpServer.Spec.Rollout.MaxSurge)
			}
		}
		return appsv1.DeploymentStrategy{
			Type: appsv1.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxUnavailable: &maxUnavailable,
				MaxSurge:       &maxSurge,
			},
		}
	}
	return appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
}

func canaryDeploymentName(serverName string) string {
	return serverName + "-canary"
}

func canaryEnabled(mcpServer *mcpv1alpha1.MCPServer) bool {
	return mcpServer != nil &&
		mcpServer.Spec.Rollout != nil &&
		mcpServer.Spec.Rollout.Strategy == mcpv1alpha1.RolloutStrategyCanary
}
func rewriteRegistry(image, registry string) string {
	if registry == "" {
		return image
	}
	parts := strings.Split(image, "/")
	if len(parts) == 1 {
		return fmt.Sprintf("%s/%s", registry, image)
	}

	// If first part looks like a registry (contains . or : or is localhost), drop it.
	first := parts[0]
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		parts = parts[1:]
	}
	return fmt.Sprintf("%s/%s", registry, strings.Join(parts, "/"))
}

func imageHasTagOrDigest(image string) bool {
	if strings.Contains(image, "@") {
		return true
	}

	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	return lastColon > lastSlash
}

func (r *MCPServerReconciler) buildImagePullSecrets(mcpServer *mcpv1alpha1.MCPServer) []corev1.LocalObjectReference {
	// If user specified pull secrets, honor them.
	if len(mcpServer.Spec.ImagePullSecrets) > 0 {
		out := make([]corev1.LocalObjectReference, 0, len(mcpServer.Spec.ImagePullSecrets))
		for _, s := range mcpServer.Spec.ImagePullSecrets {
			if s == "" {
				continue
			}
			out = append(out, corev1.LocalObjectReference{Name: s})
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}

	// Otherwise, use the provisioned registry secret if configured.
	// The secret is created during setup (mcp-runtime setup), not during reconciliation.
	if r.ProvisionedRegistry == nil || r.ProvisionedRegistry.URL == "" ||
		r.ProvisionedRegistry.Username == "" || r.ProvisionedRegistry.Password == "" {
		return nil
	}

	secretName := r.ProvisionedRegistry.SecretName
	if secretName == "" {
		secretName = DefaultRegistrySecretName
	}

	return []corev1.LocalObjectReference{{Name: secretName}}
}

// buildServerEnvVars returns the MCP server container env: the spec's env,
// then the OAuth resource settings a standalone resource server needs. With the
// gateway disabled the server itself answers OAuth challenges and publishes
// protected-resource metadata, so it must advertise exactly the audience the
// authorization server mints tokens for. Those values are derived from the
// public URL here rather than hand-copied into envVars, where a stale host
// makes MCP clients reject the metadata before OAuth even starts. Reconciled
// path and OAuth values replace matching spec entries so they cannot drift.
func (r *MCPServerReconciler) buildServerEnvVars(mcpServer *mcpv1alpha1.MCPServer) []corev1.EnvVar {
	result := r.buildEnvVars(mcpServer.Spec.EnvVars, mcpServer.Spec.SecretEnvVars)
	// MCP_PATH is the path the server receives requests on and belongs to
	// reconciliation, so CLI and API clients cannot persist a stale copy in
	// spec.envVars.
	if upstreamPath := upstreamMCPPath(mcpServer); upstreamPath != "" {
		result = setEnvVarValue(result, "MCP_PATH", upstreamPath)
	}
	if gatewayEnabled(mcpServer) || !serverUsesOAuth(mcpServer) {
		return result
	}
	resource := strings.TrimSpace(mcpServer.Spec.Auth.Audience)
	for _, derived := range []corev1.EnvVar{
		{Name: "MCP_AUTH_RESOURCE", Value: resource},
		{Name: "MCP_AUTH_RESOURCE_METADATA_URL", Value: mcpv1alpha1.ProtectedResourceMetadataURL(resource)},
		{Name: "MCP_AUTH_ISSUER", Value: strings.TrimSpace(mcpServer.Spec.Auth.IssuerURL)},
	} {
		if derived.Value == "" {
			continue
		}
		result = setEnvVarValue(result, derived.Name, derived.Value)
	}
	return result
}

// upstreamMCPPath is the path the MCP server container receives: the public
// path, minus the gateway's stripPrefix when the gateway strips one before
// forwarding. It mirrors the gateway's trimRequestPathPrefix so the server
// listens exactly where the gateway sends requests.
func upstreamMCPPath(mcpServer *mcpv1alpha1.MCPServer) string {
	publicPath := strings.TrimSpace(mcpServer.EffectivePublicPath())
	if publicPath == "" || !gatewayEnabled(mcpServer) {
		return publicPath
	}
	prefix := strings.TrimRight(strings.TrimSpace(mcpServer.Spec.Gateway.StripPrefix), "/")
	if prefix == "" || (publicPath != prefix && !strings.HasPrefix(publicPath, prefix+"/")) {
		return publicPath
	}
	if trimmed := strings.TrimPrefix(publicPath, prefix); trimmed != "" {
		return trimmed
	}
	return "/"
}

func setEnvVarValue(envVars []corev1.EnvVar, name, value string) []corev1.EnvVar {
	for i := range envVars {
		if envVars[i].Name == name {
			envVars[i].Value = value
			envVars[i].ValueFrom = nil
			return envVars
		}
	}
	envVars = append(envVars, corev1.EnvVar{Name: name, Value: value})
	return envVars
}

func (r *MCPServerReconciler) buildEnvVars(envVars []mcpv1alpha1.EnvVar, secretEnvVars []mcpv1alpha1.SecretEnvVar) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(envVars)+len(secretEnvVars))
	for _, ev := range envVars {
		result = append(result, corev1.EnvVar{
			Name:  ev.Name,
			Value: ev.Value,
		})
	}
	for _, ev := range secretEnvVars {
		if ev.SecretKeyRef == nil {
			continue
		}
		result = append(result, corev1.EnvVar{
			Name: ev.Name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ev.SecretKeyRef.Name},
					Key:                  ev.SecretKeyRef.Key,
				},
			},
		})
	}
	return result
}
func gatewayEnabled(mcpServer *mcpv1alpha1.MCPServer) bool {
	return mcpServer != nil && mcpServer.Spec.Gateway != nil && mcpServer.Spec.Gateway.Enabled
}

func serverUsesOAuth(mcpServer *mcpv1alpha1.MCPServer) bool {
	return mcpServer != nil && mcpServer.Spec.Auth != nil
}

func (r *MCPServerReconciler) oauthInternalIssuerURL() string {
	return strings.TrimSpace(r.OAuthInternalIssuerURL)
}

// analyticsEnabled reports whether the gateway sidecar should emit analytics
// for this MCPServer. Analytics is opt-in per server; the operator-level
// default only supplies the endpoint after the server requests analytics.
func (r *MCPServerReconciler) analyticsEnabled(mcpServer *mcpv1alpha1.MCPServer) bool {
	if mcpServer == nil {
		return false
	}
	if mcpServer.Spec.Analytics == nil || mcpServer.Spec.Analytics.Disabled {
		return false
	}
	url := strings.TrimSpace(mcpServer.Spec.Analytics.IngestURL)
	if url == "" {
		url = strings.TrimSpace(r.DefaultAnalyticsIngestURL)
	}
	return url != ""
}
