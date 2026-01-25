package handlers

import (
	"net/http"

	"github.com/google/uuid"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"

	"github.com/llm-d/llm-d-inference-proxy/pkg/orchestrations"
)

const ()

func NewProxy(orchestrator orchestrations.OrchestrationPlugin, ds datastore.Datastore, director *requestcontrol.Director) http.Handler {
	return &inferenceProxy{
		orchestrator: orchestrator,
		ds:           ds,
		director:     director,
	}
}

type inferenceProxy struct {
	orchestrator orchestrations.OrchestrationPlugin
	ds           datastore.Datastore
	director     *requestcontrol.Director
}

func (ip *inferenceProxy) ServeHTTP(rw http.ResponseWriter, origReq *http.Request) {
	ctx := origReq.Context()
	logger := log.FromContext(ctx)

	requestID := origReq.Header.Get(requtil.RequestIdHeaderKey)
	// request ID is a must for maintaining a state per request in plugins that hold internal state and use PluginState.
	// if request id was not supplied as a header, we generate it ourselves.
	if len(requestID) == 0 {
		requestID = uuid.NewString()
		logger.V(logutil.TRACE).Info("RequestID header is not found in the request, generated a request id")
	}
	logger = logger.WithValues(requtil.RequestIdHeaderKey, requestID)
	ctx = log.IntoContext(ctx, logger)

	req := origReq.WithContext(ctx)

	orchestration := orchestrations.NewOrchestration(ip.ds, ip.director, req, requestID, rw)

	err := orchestration.PrepareRequest()
	if err != nil {
		sendError(err, rw)
		return
	}

	if ip.orchestrator == nil {
		// If not using an OrchestrationPlugin, use regular Director and Scheduler functionality
		orchestration.StandardProcessing()
	} else {
		// The Orchestrator will orchestrate things here
		ip.orchestrator.Orchestrate(ctx, orchestration)
	}
}

func sendError(err error, rw http.ResponseWriter) {
	rw.WriteHeader(500)
	rw.Write([]byte(err.Error()))
}
