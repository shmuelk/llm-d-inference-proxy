package plugins

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"

	"github.com/llm-d/llm-d-inference-proxy/pkg/plugins/filter"
	"github.com/llm-d/llm-d-inference-proxy/pkg/plugins/orchestration"
	prerequest "github.com/llm-d/llm-d-inference-proxy/pkg/plugins/pre-request"
	"github.com/llm-d/llm-d-inference-proxy/pkg/plugins/profile"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugins.Register(filter.ByLabelType, filter.ByLabelFactory)
	plugins.Register(filter.ByLabelSelectorType, filter.ByLabelSelectorFactory)
	plugins.Register(filter.DecodeRoleType, filter.DecodeRoleFactory)
	plugins.Register(filter.PrefillRoleType, filter.PrefillRoleFactory)
	plugins.Register(prerequest.PrefillHeaderHandlerType, prerequest.PrefillHeaderHandlerFactory)
	plugins.Register(profile.PdProfileHandlerType, profile.PdProfileHandlerFactory)

	plugins.Register(orchestration.PdOrchestrationType, orchestration.PdOrchestrationFactory)
	plugins.Register(profile.OrchestrationProfileHandlerType, profile.OrchestrationProfileHandlerFactory)
}
