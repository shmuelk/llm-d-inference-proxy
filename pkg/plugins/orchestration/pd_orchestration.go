package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-proxy/pkg/orchestrations"
)

const (
	// PdOrchestrationType is the type of the PdOrchestration
	PdOrchestrationType = "pd-orchestration"

	defaultDecodeProfile  = "decode"
	defaultPrefillProfile = "prefill"

	requestHeaderRequestID = "x-request-id"

	requestFieldKVTransferParams    = "kv_transfer_params"
	requestFieldMaxTokens           = "max_tokens"
	requestFieldMaxCompletionTokens = "max_completion_tokens"
	requestFieldDoRemotePrefill     = "do_remote_prefill"
	requestFieldDoRemoteDecode      = "do_remote_decode"
	requestFieldRemoteBlockIDs      = "remote_block_ids"
	requestFieldRemoteEngineID      = "remote_engine_id"
	requestFieldRemoteHost          = "remote_host"
	requestFieldRemotePort          = "remote_port"
	requestFieldStream              = "stream"
	requestFieldStreamOptions       = "stream_options"
)

type pdOrchestartionParameters struct {
	DeferredDecode bool   `json:"deferredDecode"`
	DecodeProfile  string `json:"decodeProfile"`
	PrefillProfile string `json:"prefillProfile"`
}

// compile-time type assertion
var _ orchestrations.OrchestrationPlugin = &PdOrchestration{}

// PdOrchestrationFactory defines the factory function for the PdOrchestration
func PdOrchestrationFactory(name string, rawParameters json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	parameters := pdOrchestartionParameters{
		DecodeProfile:  defaultDecodeProfile,
		PrefillProfile: defaultPrefillProfile,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' orchestration plugin - %w", PdOrchestrationType, err)
		}
	}
	return NewPdOrchestration(parameters.DeferredDecode, parameters.PrefillProfile, parameters.DecodeProfile).WithName(name), nil
}

// NewPdOrchestration initializes a new OrchestrationProfileHandler and returns its pointer.
func NewPdOrchestration(deferredDecode bool, prefillProfile, decodeProfile string) *PdOrchestration {
	return &PdOrchestration{
		deferredDecode: deferredDecode,
		decodeProfile:  decodeProfile,
		prefillProfile: prefillProfile,
	}
}

// OrchestrationProfileHandler handles scheduler profiles for orchestrations.
type PdOrchestration struct {
	typedName      plugin.TypedName
	deferredDecode bool
	decodeProfile  string
	prefillProfile string
}

// TypedName returns the typed name of the plugin.
func (o *PdOrchestration) TypedName() plugin.TypedName {
	return o.typedName
}

// WithName sets the name of the plugin.
func (o *PdOrchestration) WithName(name string) *PdOrchestration {
	o.typedName.Name = name
	return o
}

func (o *PdOrchestration) Orchestrate(ctx context.Context, orchestration *orchestrations.Orchestration) error {
	if o.deferredDecode {
		return o.deferredDecodePD(ctx, orchestration)
	}
	return o.nonDeferredDecodePD(ctx, orchestration)
}

func (o *PdOrchestration) nonDeferredDecodePD(ctx context.Context, orchestration *orchestrations.Orchestration) error {
	logger := log.FromContext(ctx)
	logger.V(logutil.DEBUG).Info("Running PD with non-deferred decode")

	schedResult, err := orchestration.DetermineTargets(o.prefillProfile, o.decodeProfile)
	if err != nil {
		return err
	}

	decodeResults := schedResult.ProfileResults[o.decodeProfile]
	if decodeResults == nil {
		return errors.New("failed to find available decode workers")
	}

	prefillResults := schedResult.ProfileResults[o.prefillProfile]
	if prefillResults != nil {
		err = o.sendPrefillRequest(prefillResults, orchestration, logger)
		if err != nil {
			return err
		}
	}

	o.sendDecodeRequest(decodeResults, orchestration, logger)

	return nil
}

func (o *PdOrchestration) deferredDecodePD(ctx context.Context, orchestration *orchestrations.Orchestration) error {
	logger := log.FromContext(ctx)
	logger.V(logutil.DEBUG).Info("Running PD with deferred decode")

	schedResult, err := orchestration.DetermineTargets(o.prefillProfile)
	if err != nil {
		return err
	}

	prefillResults := schedResult.ProfileResults[o.prefillProfile]
	if prefillResults != nil {
		err = o.sendPrefillRequest(prefillResults, orchestration, logger)
		if err != nil {
			return err
		}
	}

	schedResult, err = orchestration.DetermineTargets(o.decodeProfile)
	if err != nil {
		return err
	}
	decodeResults := schedResult.ProfileResults[o.decodeProfile]
	if decodeResults == nil {
		return errors.New("failed to find available decode workers")
	}

	o.sendDecodeRequest(decodeResults, orchestration, logger)

	return nil
}

func (o *PdOrchestration) sendPrefillRequest(prefillResults *scheduling.ProfileRunResult, orchestration *orchestrations.Orchestration, logger logr.Logger) error {
	// Generate unique request UUID
	uuid, err := uuid.NewUUID()
	if err != nil {
		logger.Error(err, "failed to send error response to client")
		return err
	}
	uuidStr := uuid.String()
	orchestration.Headers()[requestHeaderRequestID] = uuidStr

	completionRequest := orchestration.Body()
	streamValue, streamOk := completionRequest[requestFieldStream]
	streamOptionsValue, streamOptionsOk := completionRequest[requestFieldStreamOptions]
	maxTokensValue, maxTokensOk := completionRequest[requestFieldMaxTokens]
	maxCompletionTokensValue, maxCompletionTokensOk := completionRequest[requestFieldMaxCompletionTokens]

	completionRequest[requestFieldKVTransferParams] = map[string]any{
		requestFieldDoRemoteDecode:  true,
		requestFieldDoRemotePrefill: false,
		requestFieldRemoteEngineID:  nil,
		requestFieldRemoteBlockIDs:  nil,
		requestFieldRemoteHost:      nil,
		requestFieldRemotePort:      nil,
	}

	completionRequest[requestFieldStream] = false
	delete(completionRequest, requestFieldStreamOptions)
	completionRequest[requestFieldMaxTokens] = 1
	completionRequest[requestFieldMaxCompletionTokens] = 1

	_, prefillResponse, err := orchestration.SendRequest(prefillResults.TargetEndpoints[0])
	if err != nil {
		return err
	}

	pKVTransferParams, ok := prefillResponse[requestFieldKVTransferParams]
	if !ok {
		logger.Info("warning: missing 'kv_transfer_params' field in prefiller response")
	}

	logger.V(5).Info("received prefiller response", requestFieldKVTransferParams, pKVTransferParams)

	orchestration.Headers()[requestHeaderRequestID] = uuidStr

	delete(completionRequest, requestFieldStream)
	if streamOk {
		completionRequest[requestFieldStream] = streamValue
	}
	if streamOptionsOk {
		completionRequest[requestFieldStreamOptions] = streamOptionsValue
	}
	delete(completionRequest, requestFieldMaxTokens)
	if maxTokensOk {
		completionRequest[requestFieldMaxTokens] = maxTokensValue
	}
	delete(completionRequest, requestFieldMaxCompletionTokens)
	if maxCompletionTokensOk {
		completionRequest[requestFieldMaxCompletionTokens] = maxCompletionTokensValue
	}
	completionRequest[requestFieldKVTransferParams] = pKVTransferParams

	return nil
}

func (o *PdOrchestration) sendDecodeRequest(decodeResults *scheduling.ProfileRunResult, orchestration *orchestrations.Orchestration, logger logr.Logger) {
	logger.V(5).Info("sending request to decoder", "body", orchestration.Body())
	targetMetadata := decodeResults.TargetEndpoints[0].GetMetadata()
	logger.V(4).Info("sending request to decoder", "to", net.JoinHostPort(targetMetadata.GetIPAddress(), targetMetadata.GetPort()))

	orchestration.SendRequestAndResponse(decodeResults.TargetEndpoints[0])

}
