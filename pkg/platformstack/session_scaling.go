package platformstack

// SessionLocalMaxReplicas is the maximum safe replica count for platform
// deployments that rely on the UI's in-memory session store until shared
// session storage is implemented.
const SessionLocalMaxReplicas int32 = 1

// SessionLocalDeploymentNames lists platform Deployments that must stay at
// SessionLocalMaxReplicas. Scaling UI or gateway beyond one replica breaks
// login and /auth/admin-check when requests hit different pods.
var SessionLocalDeploymentNames = []string{
	"mcp-ui",
	"mcp-platform-gateway",
}
