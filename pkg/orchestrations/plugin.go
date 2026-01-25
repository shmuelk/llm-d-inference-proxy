package orchestrations

import (
	"context"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
)

type OrchestrationPlugin interface {
	plugins.Plugin
	Orchestrate(ctx context.Context, orchestration *Orchestration) error
}
