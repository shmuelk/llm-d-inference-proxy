package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// OrchestrationProfileHandlerType is the type of the OrchestrationProfileHandler
	OrchestrationProfileHandlerType = "orchestration-profile-handler"
)

// compile-time type assertion
var _ framework.ProfileHandler = &OrchestrationProfileHandler{}

// OrchestrationProfileHandlerFactory defines the factory function for the OrchestrationProfileHandler
func OrchestrationProfileHandlerFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return NewOrchestrationProfileHandler().WithName(name), nil
}

// NewOrchestrationProfileHandler initializes a new OrchestrationProfileHandler and returns its pointer.
func NewOrchestrationProfileHandler() *OrchestrationProfileHandler {
	return &OrchestrationProfileHandler{}
}

// OrchestrationProfileHandler handles scheduler profiles for orchestrations.
type OrchestrationProfileHandler struct {
	typedName plugins.TypedName
}

// TypedName returns the typed name of the plugin.
func (h *OrchestrationProfileHandler) TypedName() plugins.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *OrchestrationProfileHandler) WithName(name string) *OrchestrationProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles, while taking into consideration the request properties and the
// previously executed cycles along with their results.
// For orchestrations, this plugin is not called.
func (h *OrchestrationProfileHandler) Pick(_ context.Context, _ *types.CycleState, _ *types.LLMRequest,
	_ map[string]*framework.SchedulerProfile, _ map[string]*types.ProfileRunResult) map[string]*framework.SchedulerProfile {
	return nil
}

// ProcessResults handles the outcome of the profile runs after the selected profiles ran.
// In case of an error in any of the profiles, the matching entry in the profileResults will contain nil, to indicate there was
// an error while running the profile.
// For orchestrations, all results are copied.
func (h *OrchestrationProfileHandler) ProcessResults(_ context.Context, _ *types.CycleState, _ *types.LLMRequest,
	profileResults map[string]*types.ProfileRunResult) (*types.SchedulingResult, error) {

	return &types.SchedulingResult{ProfileResults: profileResults}, nil
}
