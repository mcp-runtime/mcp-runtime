package resourcehistory

import corev1 "k8s.io/api/core/v1"

const (
	ScrapeAudience  = "mcp-resource-history"
	TokenVolumeName = "resource-history-token"
	TokenMountPath  = "/var/run/secrets/mcp-resource-history"
)

// PrometheusTokenVolume and PrometheusTokenMount are the only token mount a
// resource-history release declaration can request. No caller controls its
// audience, lifetime, name, path, source, permissions or target container.
func PrometheusTokenVolume() corev1.Volume {
	mode := int32(0444)
	expires := int64(600)
	return corev1.Volume{Name: TokenVolumeName, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: &mode, Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: ScrapeAudience, ExpirationSeconds: &expires, Path: "token"}}}}}}
}
func PrometheusTokenMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: TokenVolumeName, MountPath: TokenMountPath, ReadOnly: true}
}
