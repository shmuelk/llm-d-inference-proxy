package orchestrations

import (
	"encoding/json"
	"net"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	errutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/error"
)

const (
	orchestrationSelectedProfile = "selectedProfile"
)

func (o *Orchestration) Headers() map[string]string {
	return o.reqCtx.Request.Headers
}

func (o *Orchestration) Body() map[string]any {
	return o.reqCtx.Request.Body
}

func (o *Orchestration) DetermineTargets(profileNames ...string) (*scheduling.SchedulingResult, error) {
	ctx := o.req.Context()
	logger := log.FromContext(ctx)
	results, err := o.director.HandleRequestHelper(ctx, o.reqCtx, profileNames)
	if err != nil {
		logger.V(logutil.DEFAULT).Error(err, "Error handling request")
		sendError(err, o.origRW)
		return nil, err
	}
	return results, nil
}

func (o *Orchestration) SendRequest(target scheduling.Endpoint) (map[string]string, map[string]any, error) {
	logger := log.FromContext(o.req.Context())

	o.commonSendRequest(target)

	o.proxy.Rewrite = o.standardRewrite

	responseWriter := NewCollectingResponseWriter()
	o.proxy.ServeHTTP(responseWriter, o.req)

	headers := map[string]string{}
	for key, values := range responseWriter.Header() {
		headers[key] = values[0]
	}

	body := map[string]any{}
	if err := json.Unmarshal(responseWriter.Body(), &body); err != nil {
		if logger.V(logutil.DEBUG).Enabled() {
			logger.Info("Error unmarshaling response body", "body", string(responseWriter.Body()), "err", err)
		}
		err = errutil.Error{
			Code: errutil.BadRequest,
			Msg:  "Error unmarshaling respone body",
		}
		return nil, nil, err
	}
	return headers, body, nil
}

func (o *Orchestration) SendRequestAndResponse(target scheduling.Endpoint) {
	logger := log.FromContext(o.req.Context())

	o.commonSendRequest(target)

	o.proxy.Rewrite = o.standardRewrite

	responseWriter := NewCopyingResponseWriter(o.origRW, o.standardResponseHeaderHandler,
		o.standardStreamingHandler, logger)
	o.proxy.ServeHTTP(responseWriter, o.req)

	o.handleEndOfResponse(responseWriter)
}

func (o *Orchestration) commonSendRequest(target scheduling.Endpoint) {
	ctx := o.req.Context()
	logger := log.FromContext(ctx)

	metadata := target.GetMetadata()
	endpointString := net.JoinHostPort(metadata.GetIPAddress(), metadata.GetPort())
	logger.V(logutil.VERBOSE).Info("Request handled", "objectiveKey", o.reqCtx.ObjectiveKey, "incomingModelName",
		o.reqCtx.IncomingModelName, "targetModel", o.reqCtx.TargetModelName, "endpoint", endpointString)

	o.reqCtx.TargetPod = metadata
	o.reqCtx.TargetEndpoint = endpointString

	schedulingResult := &scheduling.SchedulingResult{
		ProfileResults: map[string]*scheduling.ProfileRunResult{
			orchestrationSelectedProfile: {
				TargetEndpoints: []scheduling.Endpoint{target},
			},
		},
		PrimaryProfileName: orchestrationSelectedProfile,
	}

	o.director.RunPreRequestPlugins(ctx, o.reqCtx.SchedulingRequest, schedulingResult)
}
