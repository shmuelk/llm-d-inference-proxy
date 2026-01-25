package orchestrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	giehandlers "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/handlers"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/handlers/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metadata"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	errutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/error"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
)

const (
	// defaultFairnessID is the default fairness ID used when no ID is provided in the request.
	// This ensures that requests without explicit fairness identifiers are still grouped and managed by the Flow Control
	// system.
	defaultFairnessID = "default-flow"

	// SchemeKey is the Envoy Pseudo Header used for the scheme of the request
	SchemeKey = ":scheme"
	// MethodKey is the Envoy Pseudo Header used for the method of the request
	MethodKey = ":method"
	// PathKey is the Envoy Pseudo Header used for the path of the request
	PathKey = ":path"
)

var (
	// EnvoyHeaders are sent by Envoy and are added here. However, as they
	// are invalid header names, they must be deleted here
	EnvoyHeaders = sets.New(
		strings.ToLower(SchemeKey),
		strings.ToLower(MethodKey),
		strings.ToLower(PathKey),
	)
)

type Orchestration struct {
	ds       datastore.Datastore
	director *requestcontrol.Director
	reqCtx   *giehandlers.RequestContext
	req      *http.Request
	origRW   http.ResponseWriter
	proxy    httputil.ReverseProxy
}

func NewOrchestration(ds datastore.Datastore, director *requestcontrol.Director,
	req *http.Request, requestID string, origRW http.ResponseWriter) *Orchestration {
	result := &Orchestration{
		ds:       ds,
		director: director,
		reqCtx: &giehandlers.RequestContext{
			Request: &giehandlers.Request{
				Headers:  make(map[string]string),
				Body:     make(map[string]any),
				Metadata: make(map[string]any),
			},
			Response: &giehandlers.Response{
				Headers: make(map[string]string),
			},
		},
		req:    req,
		origRW: origRW,
		proxy:  httputil.ReverseProxy{},
	}
	// update in headers so director can consume it
	result.reqCtx.Request.Headers[requtil.RequestIdHeaderKey] = requestID

	return result
}

func sendError(err error, rw http.ResponseWriter) {
	rw.WriteHeader(500)
	rw.Write([]byte(err.Error()))
}

func (o *Orchestration) PrepareRequest() error {
	ctx := o.req.Context()
	logger := log.FromContext(ctx)
	loggerTrace := logger.V(logutil.TRACE)
	loggerTrace.Info("Processing")
	for key, values := range o.req.Header {
		o.reqCtx.Request.Headers[key] = values[0]
		switch key {
		case metadata.FlowFairnessIDKey:
			o.reqCtx.FairnessID = o.reqCtx.Request.Headers[key]
		case metadata.ObjectiveKey:
			o.reqCtx.ObjectiveKey = o.reqCtx.Request.Headers[key]
		case metadata.ModelNameRewriteKey:
			o.reqCtx.TargetModelName = o.reqCtx.Request.Headers[key]
		}
	}
	o.reqCtx.Request.Headers[SchemeKey] = o.req.URL.Scheme
	o.reqCtx.Request.Headers[MethodKey] = o.req.Method
	o.reqCtx.Request.Headers[PathKey] = o.req.URL.Path

	if o.reqCtx.FairnessID == "" {
		o.reqCtx.FairnessID = defaultFairnessID
	}

	body, err := io.ReadAll(o.req.Body)
	defer o.req.Body.Close()
	if err != nil {
		if logger.V(logutil.DEBUG).Enabled() {
			logger.Info("Error unmarshaling request body", "body", string(body), "err", err)
		}
		err = errutil.Error{
			Code: errutil.BadRequest,
			Msg:  "Error unmarshaling request body",
		}
	} else {
		loggerTrace.Info("Incoming body", "text", body)

		loggerTrace.Info("decoding")
		if errUnmarshal := json.Unmarshal(body, &o.reqCtx.Request.Body); errUnmarshal != nil {
			if logger.V(logutil.DEBUG).Enabled() {
				logger.Info("Error unmarshaling request body", "body", string(body), "err", errUnmarshal)
			}
			err = errutil.Error{
				Code: errutil.BadRequest,
				Msg:  "Error unmarshaling request body",
			}
		} else {
			// Body stream complete. Capture raw size for flow control.
			o.reqCtx.RequestSize = len(body)
		}
	}
	return err
}

func (o *Orchestration) updateOutgoingRequest(req *http.Request) {
	requestBodyBytes, err := json.Marshal(o.reqCtx.Request.Body)
	if err != nil {
		logger := log.FromContext(req.Context())
		logger.V(logutil.DEFAULT).Error(err, "Error marshalling request body")
	}
	// Update RequestSize to match marshalled body for Content-Length header.
	o.reqCtx.RequestSize = len(requestBodyBytes)
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewBuffer(requestBodyBytes))
	req.ContentLength = int64(o.reqCtx.RequestSize)

	for key, value := range o.reqCtx.Request.Headers {
		if requtil.IsSystemOwnedHeader(key) || EnvoyHeaders.Has(key) {
			req.Header.Del(key)
			continue
		}

		req.Header.Set(key, value)
	}

	req.Header.Set("Content-Length", fmt.Sprintf("%d", o.reqCtx.RequestSize))

	metrics.RecordRequestCounter(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName)
	metrics.RecordRequestSizes(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName, o.reqCtx.RequestSize)
}

// The function is to handle streaming response if the modelServer is streaming.
func (o *Orchestration) HandleResponseBodyModelStreaming(ctx context.Context, responseText string) {
	logger := log.FromContext(ctx)
	_, err := o.director.HandleResponseBodyStreaming(ctx, o.reqCtx)
	if err != nil {
		logger.Error(err, "error in HandleResponseBodyStreaming")
	}

	// Parse usage on EVERY chunk to catch split streams (where usage and [DONE] are in different chunks).
	if resp := giehandlers.ParseRespForUsage(ctx, responseText); resp.Usage.TotalTokens > 0 {
		o.reqCtx.Usage = resp.Usage
	}

	if strings.Contains(responseText, giehandlers.StreamingEndMsg) {
		o.reqCtx.ResponseComplete = true
		metrics.RecordInputTokens(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName, o.reqCtx.Usage.PromptTokens)
		metrics.RecordOutputTokens(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName, o.reqCtx.Usage.CompletionTokens)
		cachedToken := 0
		if o.reqCtx.Usage.PromptTokenDetails != nil {
			cachedToken = o.reqCtx.Usage.PromptTokenDetails.CachedTokens
		}
		metrics.RecordPromptCachedTokens(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName, cachedToken)
	}
}

func (o *Orchestration) handleEndOfResponse(responseWriter orchestrationResponseWriter) {
	logger := log.FromContext(o.req.Context())
	logger.V(logutil.TRACE).Info("stream completed")
	ctx := o.req.Context()

	if o.reqCtx.ModelServerStreaming {
		o.reqCtx.ResponseComplete = true
		if _, err := o.director.HandleResponseBodyComplete(ctx, o.reqCtx); err != nil {
			logger.Error(err, "error in HandleResponseBodyComplete")
		}

		o.reqCtx.ResponseCompleteTimestamp = time.Now()
		metrics.RecordNormalizedTimePerOutputToken(ctx, o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName,
			o.reqCtx.RequestReceivedTimestamp, o.reqCtx.ResponseCompleteTimestamp, o.reqCtx.Usage.CompletionTokens)
	} else {
		var responseBody map[string]any
		// Don't send a 500 on a response error. Just let the message passthrough and log our error for debugging purposes.
		// We assume the body is valid JSON, err messages are not guaranteed to be json, and so capturing and sending a 500 obfuscates the response message.
		// Using the standard 'err' var will send an immediate error response back to the caller.
		var responseErr error
		body := responseWriter.Body()
		responseErr = json.Unmarshal(body, &responseBody)
		if responseErr != nil {
			logger.V(logutil.DEFAULT).Error(responseErr, "Error unmarshalling request body", "body", string(body))
		}
		err := o.HandleResponseBody(ctx, responseBody)
		if err != nil {
			if logger.V(logutil.DEBUG).Enabled() {
				logger.V(logutil.DEBUG).Error(err, "Failed to process response body", "request", o.req)
			} else {
				logger.V(logutil.DEFAULT).Error(err, "Failed to process response body")
			}
			return
		}
		if o.reqCtx.ResponseComplete {
			o.reqCtx.ResponseCompleteTimestamp = time.Now()
			metrics.RecordRequestLatencies(ctx, o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName,
				o.reqCtx.RequestReceivedTimestamp, o.reqCtx.ResponseCompleteTimestamp)
			metrics.RecordInputTokens(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName,
				o.reqCtx.Usage.PromptTokens)
			metrics.RecordOutputTokens(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName,
				o.reqCtx.Usage.CompletionTokens)
			cachedToken := 0
			if o.reqCtx.Usage.PromptTokenDetails != nil {
				cachedToken = o.reqCtx.Usage.PromptTokenDetails.CachedTokens
			}
			metrics.RecordPromptCachedTokens(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName, cachedToken)
		}
	}
	metrics.RecordRequestLatencies(ctx, o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName,
		o.reqCtx.RequestReceivedTimestamp, o.reqCtx.ResponseCompleteTimestamp)
	metrics.RecordResponseSizes(o.reqCtx.IncomingModelName, o.reqCtx.TargetModelName, o.reqCtx.ResponseSize)
}

// HandleResponseBody always returns the requestContext even in the error case, as the request context is used in error handling.
func (o *Orchestration) HandleResponseBody(ctx context.Context, response map[string]any) error {
	logger := log.FromContext(ctx)
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("error marshalling responseBody - %w", err)
	}
	if response["usage"] != nil {
		usg := response["usage"].(map[string]any)
		usage := types.Usage{
			PromptTokens:     int(usg["prompt_tokens"].(float64)),
			CompletionTokens: int(usg["completion_tokens"].(float64)),
			TotalTokens:      int(usg["total_tokens"].(float64)),
		}
		if usg["prompt_token_details"] != nil {
			detailsMap := usg["prompt_token_details"].(map[string]any)
			if cachedTokens, ok := detailsMap["cached_tokens"]; ok {
				usage.PromptTokenDetails = &types.PromptTokenDetails{
					CachedTokens: int(cachedTokens.(float64)),
				}
			}
		}
		o.reqCtx.Usage = usage
		logger.V(logutil.VERBOSE).Info("Response generated", "usage", o.reqCtx.Usage)
	}
	o.reqCtx.ResponseSize = len(responseBytes)
	// ResponseComplete is to indicate the response is complete. In non-streaming
	// case, it will be set to be true once the response is processed; in
	// streaming case, it will be set to be true once the last chunk is processed.
	// TODO(https://github.com/kubernetes-sigs/gateway-api-inference-extension/issues/178)
	// will add the processing for streaming case.
	o.reqCtx.ResponseComplete = true

	_, err = o.director.HandleResponseBodyComplete(ctx, o.reqCtx)
	return err
}
