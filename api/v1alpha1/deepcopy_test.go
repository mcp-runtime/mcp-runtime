package v1alpha1

import (
	"testing"
)

// TestMCPServerSpecResourcesDeepCopy verifies that MCPServerSpec.Resources performs a deep copy
// instead of a shallow copy by modifying the original and ensuring the copy is unaffected.
func TestMCPServerSpecResourcesDeepCopy(t *testing.T) {
	// Create original spec with Resources containing Limits and Requests
	original := MCPServerSpec{
		Image: "test-image",
		Resources: ResourceRequirements{
			Limits: &ResourceList{
				CPU:    "100m",
				Memory: "128Mi",
			},
			Requests: &ResourceList{
				CPU:    "50m",
				Memory: "64Mi",
			},
		},
	}

	// Perform deep copy
	copied := original.DeepCopy()

	// Verify initial values are equal
	if copied.Resources.Limits.CPU != original.Resources.Limits.CPU {
		t.Errorf("Initial copy failed: Limits.CPU = %q, want %q", copied.Resources.Limits.CPU, original.Resources.Limits.CPU)
	}
	if copied.Resources.Limits.Memory != original.Resources.Limits.Memory {
		t.Errorf("Initial copy failed: Limits.Memory = %q, want %q", copied.Resources.Limits.Memory, original.Resources.Limits.Memory)
	}
	if copied.Resources.Requests.CPU != original.Resources.Requests.CPU {
		t.Errorf("Initial copy failed: Requests.CPU = %q, want %q", copied.Resources.Requests.CPU, original.Resources.Requests.CPU)
	}
	if copied.Resources.Requests.Memory != original.Resources.Requests.Memory {
		t.Errorf("Initial copy failed: Requests.Memory = %q, want %q", copied.Resources.Requests.Memory, original.Resources.Requests.Memory)
	}

	// Modify the original Resources.Limits
	original.Resources.Limits.CPU = "200m"
	original.Resources.Limits.Memory = "256Mi"

	// Verify that copied values remain unchanged (proving deep copy)
	if copied.Resources.Limits.CPU != "100m" {
		t.Errorf("Deep copy failed: copied Limits.CPU was modified to %q, expected it to remain \"100m\"", copied.Resources.Limits.CPU)
	}
	if copied.Resources.Limits.Memory != "128Mi" {
		t.Errorf("Deep copy failed: copied Limits.Memory was modified to %q, expected it to remain \"128Mi\"", copied.Resources.Limits.Memory)
	}

	// Modify the original Resources.Requests
	original.Resources.Requests.CPU = "100m"
	original.Resources.Requests.Memory = "128Mi"

	// Verify that copied values remain unchanged (proving deep copy)
	if copied.Resources.Requests.CPU != "50m" {
		t.Errorf("Deep copy failed: copied Requests.CPU was modified to %q, expected it to remain \"50m\"", copied.Resources.Requests.CPU)
	}
	if copied.Resources.Requests.Memory != "64Mi" {
		t.Errorf("Deep copy failed: copied Requests.Memory was modified to %q, expected it to remain \"64Mi\"", copied.Resources.Requests.Memory)
	}
}

// TestResourceRequirementsLimitsDeepCopy verifies that ResourceRequirements.Limits performs a deep copy
// instead of a shallow copy by modifying the original and ensuring the copy is unaffected.
func TestResourceRequirementsLimitsDeepCopy(t *testing.T) {
	// Create original ResourceRequirements with Limits
	original := ResourceRequirements{
		Limits: &ResourceList{
			CPU:    "200m",
			Memory: "256Mi",
		},
	}

	// Perform deep copy
	copied := original.DeepCopy()

	// Verify initial values are equal
	if copied.Limits.CPU != original.Limits.CPU {
		t.Errorf("Initial copy failed: Limits.CPU = %q, want %q", copied.Limits.CPU, original.Limits.CPU)
	}
	if copied.Limits.Memory != original.Limits.Memory {
		t.Errorf("Initial copy failed: Limits.Memory = %q, want %q", copied.Limits.Memory, original.Limits.Memory)
	}

	// Modify the original Limits
	original.Limits.CPU = "400m"
	original.Limits.Memory = "512Mi"

	// Verify that copied values remain unchanged (proving deep copy)
	if copied.Limits.CPU != "200m" {
		t.Errorf("Deep copy failed: copied Limits.CPU was modified to %q, expected it to remain \"200m\"", copied.Limits.CPU)
	}
	if copied.Limits.Memory != "256Mi" {
		t.Errorf("Deep copy failed: copied Limits.Memory was modified to %q, expected it to remain \"256Mi\"", copied.Limits.Memory)
	}

	// Verify pointer independence: change the entire Limits pointer in original
	newLimits := &ResourceList{
		CPU:    "1000m",
		Memory: "1Gi",
	}
	original.Limits = newLimits

	// Copied Limits should still point to the old values
	if copied.Limits.CPU != "200m" {
		t.Errorf("Deep copy failed: copied Limits.CPU was affected by pointer reassignment, got %q, want \"200m\"", copied.Limits.CPU)
	}
	if copied.Limits.Memory != "256Mi" {
		t.Errorf("Deep copy failed: copied Limits.Memory was affected by pointer reassignment, got %q, want \"256Mi\"", copied.Limits.Memory)
	}
}

// TestResourceRequirementsRequestsDeepCopy verifies that ResourceRequirements.Requests performs a deep copy
// instead of a shallow copy by modifying the original and ensuring the copy is unaffected.
func TestResourceRequirementsRequestsDeepCopy(t *testing.T) {
	// Create original ResourceRequirements with Requests
	original := ResourceRequirements{
		Requests: &ResourceList{
			CPU:    "100m",
			Memory: "128Mi",
		},
	}

	// Perform deep copy
	copied := original.DeepCopy()

	// Verify initial values are equal
	if copied.Requests.CPU != original.Requests.CPU {
		t.Errorf("Initial copy failed: Requests.CPU = %q, want %q", copied.Requests.CPU, original.Requests.CPU)
	}
	if copied.Requests.Memory != original.Requests.Memory {
		t.Errorf("Initial copy failed: Requests.Memory = %q, want %q", copied.Requests.Memory, original.Requests.Memory)
	}

	// Modify the original Requests
	original.Requests.CPU = "200m"
	original.Requests.Memory = "256Mi"

	// Verify that copied values remain unchanged (proving deep copy)
	if copied.Requests.CPU != "100m" {
		t.Errorf("Deep copy failed: copied Requests.CPU was modified to %q, expected it to remain \"100m\"", copied.Requests.CPU)
	}
	if copied.Requests.Memory != "128Mi" {
		t.Errorf("Deep copy failed: copied Requests.Memory was modified to %q, expected it to remain \"128Mi\"", copied.Requests.Memory)
	}

	// Verify pointer independence: change the entire Requests pointer in original
	newRequests := &ResourceList{
		CPU:    "500m",
		Memory: "512Mi",
	}
	original.Requests = newRequests

	// Copied Requests should still point to the old values
	if copied.Requests.CPU != "100m" {
		t.Errorf("Deep copy failed: copied Requests.CPU was affected by pointer reassignment, got %q, want \"100m\"", copied.Requests.CPU)
	}
	if copied.Requests.Memory != "128Mi" {
		t.Errorf("Deep copy failed: copied Requests.Memory was affected by pointer reassignment, got %q, want \"128Mi\"", copied.Requests.Memory)
	}
}

// TestResourceRequirementsDeepCopyWithNilPointers tests deep copy behavior when Limits and Requests are nil
func TestResourceRequirementsDeepCopyWithNilPointers(t *testing.T) {
	// Test with nil Limits
	original := ResourceRequirements{
		Limits: nil,
		Requests: &ResourceList{
			CPU:    "50m",
			Memory: "64Mi",
		},
	}

	copied := original.DeepCopy()

	if copied.Limits != nil {
		t.Errorf("Deep copy failed: copied Limits should be nil, got %v", copied.Limits)
	}

	// Test with nil Requests
	original2 := ResourceRequirements{
		Limits: &ResourceList{
			CPU:    "100m",
			Memory: "128Mi",
		},
		Requests: nil,
	}

	copied2 := original2.DeepCopy()

	if copied2.Requests != nil {
		t.Errorf("Deep copy failed: copied Requests should be nil, got %v", copied2.Requests)
	}

	// Test with both nil
	original3 := ResourceRequirements{
		Limits:   nil,
		Requests: nil,
	}

	copied3 := original3.DeepCopy()

	if copied3.Limits != nil {
		t.Errorf("Deep copy failed: copied Limits should be nil, got %v", copied3.Limits)
	}
	if copied3.Requests != nil {
		t.Errorf("Deep copy failed: copied Requests should be nil, got %v", copied3.Requests)
	}
}

// TestMCPServerSpecDeepCopy tests that MCPServerSpec.DeepCopy() correctly creates an independent copy
// of all fields, including nested Resources.
func TestMCPServerSpecDeepCopy(t *testing.T) {
	replicas := int32(3)
	original := MCPServerSpec{
		Description:            "Test server for deepcopy coverage.",
		Image:                  "test-image",
		ImageTag:               "v1.0.0",
		RegistryOverride:       "registry.example.com",
		UseProvisionedRegistry: true,
		ImagePullSecrets:       []string{"secret1", "secret2"},
		Replicas:               &replicas,
		Port:                   8088,
		ServicePort:            80,
		IngressPath:            "/test/mcp",
		IngressHost:            "test.example.com",
		PublicPathPrefix:       "test-public",
		IngressClass:           "traefik",
		IngressAnnotations: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		Resources: ResourceRequirements{
			Limits: &ResourceList{
				CPU:    "500m",
				Memory: "512Mi",
			},
			Requests: &ResourceList{
				CPU:    "250m",
				Memory: "256Mi",
			},
		},
		EnvVars: []EnvVar{
			{Name: "ENV1", Value: "value1"},
			{Name: "ENV2", Value: "value2"},
		},
		Gateway: &GatewayConfig{
			Enabled:     BoolPtr(true),
			Image:       "example.com/mcp-gateway:latest",
			Port:        8091,
			UpstreamURL: "http://127.0.0.1:8088",
			StripPrefix: "/test-server",
		},
		Analytics: &AnalyticsConfig{
			IngestURL: "http://analytics.default.svc/api/events",
			Source:    "test-server",
			EventType: "mcp.request",
			APIKeySecretRef: &SecretKeyRef{
				Name: "analytics-creds",
				Key:  "api-key",
			},
		},
	}

	// Perform deep copy
	copied := original.DeepCopy()

	assertSpecSimpleFields(t, copied, &original)
	assertReplicasDeepCopy(t, copied, &original)
	assertImagePullSecretsDeepCopy(t, copied, &original)
	assertIngressAnnotationsDeepCopy(t, copied, &original)
	assertResourcesDeepCopy(t, copied, &original)
	assertEnvVarsDeepCopy(t, copied, &original)
	assertGatewayDeepCopy(t, copied, &original)
	assertAnalyticsDeepCopy(t, copied, &original)
}

// TestMCPServerSpecDeepCopyNilFields tests that MCPServerSpec.DeepCopy() handles nil pointer fields correctly
func TestMCPServerSpecDeepCopyNilFields(t *testing.T) {
	original := MCPServerSpec{
		Image:    "test-image",
		Replicas: nil, // nil pointer
		Resources: ResourceRequirements{
			Limits:   nil,
			Requests: nil,
		},
	}

	// Perform deep copy
	copied := original.DeepCopy()

	// Verify nil fields remain nil
	if copied.Replicas != nil {
		t.Errorf("Deep copy failed: copied Replicas should be nil, got %v", copied.Replicas)
	}
	if copied.Resources.Limits != nil {
		t.Errorf("Deep copy failed: copied Resources.Limits should be nil, got %v", copied.Resources.Limits)
	}
	if copied.Resources.Requests != nil {
		t.Errorf("Deep copy failed: copied Resources.Requests should be nil, got %v", copied.Resources.Requests)
	}
	if copied.Gateway != nil {
		t.Errorf("Deep copy failed: copied Gateway should be nil, got %v", copied.Gateway)
	}
	if copied.Analytics != nil {
		t.Errorf("Deep copy failed: copied Analytics should be nil, got %v", copied.Analytics)
	}
}

func assertSpecSimpleFields(t *testing.T, copied, original *MCPServerSpec) {
	t.Helper()
	if copied == nil || original == nil {
		t.Fatal("MCPServerSpec is nil")
	}
	if copied.Description != original.Description {
		t.Errorf("Description = %q, want %q", copied.Description, original.Description)
	}
	if copied.Image != original.Image {
		t.Errorf("Image = %q, want %q", copied.Image, original.Image)
	}
	if copied.ImageTag != original.ImageTag {
		t.Errorf("ImageTag = %q, want %q", copied.ImageTag, original.ImageTag)
	}
	if copied.RegistryOverride != original.RegistryOverride {
		t.Errorf("RegistryOverride = %q, want %q", copied.RegistryOverride, original.RegistryOverride)
	}
	if copied.UseProvisionedRegistry != original.UseProvisionedRegistry {
		t.Errorf("UseProvisionedRegistry = %v, want %v", copied.UseProvisionedRegistry, original.UseProvisionedRegistry)
	}
	if copied.PublicPathPrefix != original.PublicPathPrefix {
		t.Errorf("PublicPathPrefix = %q, want %q", copied.PublicPathPrefix, original.PublicPathPrefix)
	}
}

func assertReplicasDeepCopy(t *testing.T, copied *MCPServerSpec, original *MCPServerSpec) {
	t.Helper()
	if copied.Replicas == nil || *copied.Replicas != *original.Replicas {
		t.Errorf("Replicas = %v, want %v", copied.Replicas, original.Replicas)
	}
	*original.Replicas = 5
	if *copied.Replicas != 3 {
		t.Errorf("Deep copy failed: copied Replicas was modified to %d, expected it to remain 3", *copied.Replicas)
	}
}

func assertImagePullSecretsDeepCopy(t *testing.T, copied *MCPServerSpec, original *MCPServerSpec) {
	t.Helper()
	if len(copied.ImagePullSecrets) != len(original.ImagePullSecrets) {
		t.Errorf("ImagePullSecrets length = %d, want %d", len(copied.ImagePullSecrets), len(original.ImagePullSecrets))
	}
	original.ImagePullSecrets[0] = "modified-secret"
	if copied.ImagePullSecrets[0] != "secret1" {
		t.Errorf("Deep copy failed: copied ImagePullSecrets[0] was modified to %q, expected it to remain \"secret1\"", copied.ImagePullSecrets[0])
	}
}

func assertIngressAnnotationsDeepCopy(t *testing.T, copied *MCPServerSpec, original *MCPServerSpec) {
	t.Helper()
	if len(copied.IngressAnnotations) != len(original.IngressAnnotations) {
		t.Errorf("IngressAnnotations length = %d, want %d", len(copied.IngressAnnotations), len(original.IngressAnnotations))
	}
	original.IngressAnnotations["key1"] = "modified-value"
	if copied.IngressAnnotations["key1"] != "value1" {
		t.Errorf("Deep copy failed: copied IngressAnnotations[\"key1\"] was modified to %q, expected it to remain \"value1\"", copied.IngressAnnotations["key1"])
	}
}

func assertResourcesDeepCopy(t *testing.T, copied *MCPServerSpec, original *MCPServerSpec) {
	t.Helper()
	if copied.Resources.Limits == nil || copied.Resources.Limits.CPU != "500m" {
		t.Errorf("Resources.Limits.CPU = %q, want \"500m\"", copied.Resources.Limits.CPU)
	}
	original.Resources.Limits.CPU = "1000m"
	if copied.Resources.Limits.CPU != "500m" {
		t.Errorf("Deep copy failed: copied Resources.Limits.CPU was modified to %q, expected it to remain \"500m\"", copied.Resources.Limits.CPU)
	}
}

func assertEnvVarsDeepCopy(t *testing.T, copied *MCPServerSpec, original *MCPServerSpec) {
	t.Helper()
	if len(copied.EnvVars) != len(original.EnvVars) {
		t.Errorf("EnvVars length = %d, want %d", len(copied.EnvVars), len(original.EnvVars))
	}
	original.EnvVars[0].Value = "modified-value"
	if copied.EnvVars[0].Value != "value1" {
		t.Errorf("Deep copy failed: copied EnvVars[0].Value was modified to %q, expected it to remain \"value1\"", copied.EnvVars[0].Value)
	}
}

func assertGatewayDeepCopy(t *testing.T, copied *MCPServerSpec, original *MCPServerSpec) {
	t.Helper()
	if copied.Gateway == nil {
		t.Fatal("Gateway should not be nil")
	}
	if copied.Gateway.Image != original.Gateway.Image {
		t.Errorf("Gateway.Image = %q, want %q", copied.Gateway.Image, original.Gateway.Image)
	}
	if copied.Gateway.UpstreamURL != original.Gateway.UpstreamURL {
		t.Errorf("Gateway.UpstreamURL = %q, want %q", copied.Gateway.UpstreamURL, original.Gateway.UpstreamURL)
	}
	original.Gateway.Image = "example.com/mcp-gateway:v2"
	original.Gateway.UpstreamURL = "http://127.0.0.1:9090"
	if copied.Gateway.Image != "example.com/mcp-gateway:latest" {
		t.Errorf("Deep copy failed: copied Gateway.Image was modified to %q", copied.Gateway.Image)
	}
	if copied.Gateway.UpstreamURL != "http://127.0.0.1:8088" {
		t.Errorf("Deep copy failed: copied Gateway.UpstreamURL was modified to %q", copied.Gateway.UpstreamURL)
	}
}

func assertAnalyticsDeepCopy(t *testing.T, copied *MCPServerSpec, original *MCPServerSpec) {
	t.Helper()
	if copied.Analytics == nil {
		t.Fatal("Analytics should not be nil")
	}
	if copied.Analytics.IngestURL != original.Analytics.IngestURL {
		t.Errorf("Analytics.IngestURL = %q, want %q", copied.Analytics.IngestURL, original.Analytics.IngestURL)
	}
	if copied.Analytics.APIKeySecretRef == nil {
		t.Fatal("Analytics.APIKeySecretRef should not be nil")
	}
	original.Analytics.IngestURL = "http://analytics.default.svc/api/v2/events"
	original.Analytics.APIKeySecretRef.Name = "updated-creds"
	if copied.Analytics.IngestURL != "http://analytics.default.svc/api/events" {
		t.Errorf("Deep copy failed: copied Analytics.IngestURL was modified to %q", copied.Analytics.IngestURL)
	}
	if copied.Analytics.APIKeySecretRef.Name != "analytics-creds" {
		t.Errorf("Deep copy failed: copied Analytics.APIKeySecretRef.Name was modified to %q", copied.Analytics.APIKeySecretRef.Name)
	}
}
