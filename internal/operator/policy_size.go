package operator

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const maxGatewayPolicyBytes = 700 * 1024

var gatewayPolicyOversizeTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "mcp_runtime_gateway_policy_oversize_total",
	Help: "Policy publications rejected because the rendered document exceeded the size guard.",
})

func init() { metrics.Registry.MustRegister(gatewayPolicyOversizeTotal) }
