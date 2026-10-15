package access

import "fmt"

func platformSessionRequired() error {
	return fmt.Errorf("sessions are managed by the platform API; omit --use-kube and authenticate with mcp-runtime auth login")
}
