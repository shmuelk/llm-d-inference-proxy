package orchestrations

import (
	"context"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
)

type OrchestrationPlugin interface {
	plugin.Plugin
	Orchestrate(ctx context.Context, orchestration *Orchestration) error
}
