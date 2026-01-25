package plugins

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"

	"github.com/llm-d/llm-d-inference-proxy/pkg/plugins/filter"
	"github.com/llm-d/llm-d-inference-proxy/pkg/plugins/orchestration"
	prerequest "github.com/llm-d/llm-d-inference-proxy/pkg/plugins/pre-request"
	"github.com/llm-d/llm-d-inference-proxy/pkg/plugins/profile"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugin.Register(filter.ByLabelType, filter.ByLabelFactory)
	plugin.Register(filter.ByLabelSelectorType, filter.ByLabelSelectorFactory)
	plugin.Register(filter.DecodeRoleType, filter.DecodeRoleFactory)
	plugin.Register(filter.PrefillRoleType, filter.PrefillRoleFactory)
	plugin.Register(prerequest.PrefillHeaderHandlerType, prerequest.PrefillHeaderHandlerFactory)
	plugin.Register(profile.PdProfileHandlerType, profile.PdProfileHandlerFactory)

	plugin.Register(orchestration.PdOrchestrationType, orchestration.PdOrchestrationFactory)
	plugin.Register(profile.OrchestrationProfileHandlerType, profile.OrchestrationProfileHandlerFactory)
}
