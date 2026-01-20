package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	giehandlers "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/handlers"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	errutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/error"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
)

type inferenceProxyHelper struct {
	ds         datastore.Datastore
	director   *requestcontrol.Director
	reqCtx     *giehandlers.RequestContext
	req        *http.Request
	origRW     http.ResponseWriter
	statusCode int
	body       []byte
}

func newInferenceProxyHelper(ds datastore.Datastore, director *requestcontrol.Director,
	req *http.Request, origRW http.ResponseWriter) *inferenceProxyHelper {
	return &inferenceProxyHelper{
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
		body:   []byte{},
	}
}

// rewrite is called by the ReverseProxy to rewite/redirect the request as needed
func (ip *inferenceProxyHelper) rewrite(pr *httputil.ProxyRequest) {
	targetURL, _ := url.Parse("http://" + ip.reqCtx.TargetEndpoint)
	pr.SetURL(targetURL)
	ip.updateOutgoingRequest(pr.Out)
	pr.SetXForwarded()
}

func (ip *inferenceProxyHelper) updateOutgoingRequest(req *http.Request) {
	requestBodyBytes, err := json.Marshal(ip.reqCtx.Request.Body)
	if err != nil {
		logger := log.FromContext(req.Context())
		logger.V(logutil.DEFAULT).Error(err, "Error marshalling request body")
	}
	// Update RequestSize to match marshalled body for Content-Length header.
	ip.reqCtx.RequestSize = len(requestBodyBytes)
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewBuffer(requestBodyBytes))
	req.ContentLength = int64(ip.reqCtx.RequestSize)

	for key, value := range ip.reqCtx.Request.Headers {
		if requtil.IsSystemOwnedHeader(key) || EnvoyHeaders.Has(key) {
			req.Header.Del(key)
			continue
		}

		req.Header.Set(key, value)
	}

	req.Header.Set("Content-Length", fmt.Sprintf("%d", ip.reqCtx.RequestSize))

	metrics.RecordRequestCounter(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName)
	metrics.RecordRequestSizes(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName, ip.reqCtx.RequestSize)
}

func (ip *inferenceProxyHelper) Header() http.Header {
	return ip.origRW.Header()
}

func (ip *inferenceProxyHelper) Write(data []byte) (int, error) {
	if ip.reqCtx.ModelServerStreaming {
		// Currently we punt on response parsing if the modelServer is streaming, and we just passthrough.
		responseText := string(data)
		ip.HandleResponseBodyModelStreaming(ip.req.Context(), responseText)
	} else {
		ip.body = append(ip.body, data...)
	}
	return ip.origRW.Write(data)
}

func (ip *inferenceProxyHelper) WriteHeader(statusCode int) {
	logger := log.FromContext(ip.req.Context())
	if statusCode != 200 {
		ip.reqCtx.ResponseStatusCode = errutil.ModelServerError
	}
	ip.origRW.WriteHeader(statusCode)
	ip.statusCode = statusCode
	for key, value := range ip.origRW.Header() {
		ip.reqCtx.Response.Headers[key] = value[0]
		if key == "content-type" && strings.Contains(value[0], "text/event-stream") {
			ip.reqCtx.ModelServerStreaming = true
			logger.V(logutil.TRACE).Info("model server is streaming response")
		}
	}

	var err error
	ip.reqCtx, err = ip.director.HandleResponseReceived(ip.req.Context(), ip.reqCtx)
	if err != nil {
		if logger.V(logutil.DEBUG).Enabled() {
			logger.V(logutil.DEBUG).Error(err, "Failed to process response headers", "request", ip.req)
		} else {
			logger.V(logutil.DEFAULT).Error(err, "Failed to process response headers")
		}
	}
}

// HandleResponseBody always returns the requestContext even in the error case, as the request context is used in error handling.
func (ip *inferenceProxyHelper) HandleResponseBody(ctx context.Context, response map[string]any) error {
	logger := log.FromContext(ctx)
	responseBytes, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("error marshalling responseBody - %w", err)
	}
	if response["usage"] != nil {
		usg := response["usage"].(map[string]any)
		usage := giehandlers.Usage{
			PromptTokens:     int(usg["prompt_tokens"].(float64)),
			CompletionTokens: int(usg["completion_tokens"].(float64)),
			TotalTokens:      int(usg["total_tokens"].(float64)),
		}
		if usg["prompt_token_details"] != nil {
			detailsMap := usg["prompt_token_details"].(map[string]any)
			if cachedTokens, ok := detailsMap["cached_tokens"]; ok {
				usage.PromptTokenDetails = &giehandlers.PromptTokenDetails{
					CachedTokens: int(cachedTokens.(float64)),
				}
			}
		}
		ip.reqCtx.Usage = usage
		logger.V(logutil.VERBOSE).Info("Response generated", "usage", ip.reqCtx.Usage)
	}
	ip.reqCtx.ResponseSize = len(responseBytes)
	// ResponseComplete is to indicate the response is complete. In non-streaming
	// case, it will be set to be true once the response is processed; in
	// streaming case, it will be set to be true once the last chunk is processed.
	// TODO(https://github.com/kubernetes-sigs/gateway-api-inference-extension/issues/178)
	// will add the processing for streaming case.
	ip.reqCtx.ResponseComplete = true

	_, err = ip.director.HandleResponseBodyComplete(ctx, ip.reqCtx)
	return err
}

// The function is to handle streaming response if the modelServer is streaming.
func (ip *inferenceProxyHelper) HandleResponseBodyModelStreaming(ctx context.Context, responseText string) {
	logger := log.FromContext(ctx)
	_, err := ip.director.HandleResponseBodyStreaming(ctx, ip.reqCtx)
	if err != nil {
		logger.Error(err, "error in HandleResponseBodyStreaming")
	}

	// Parse usage on EVERY chunk to catch split streams (where usage and [DONE] are in different chunks).
	if resp := giehandlers.ParseRespForUsage(ctx, responseText); resp.Usage.TotalTokens > 0 {
		ip.reqCtx.Usage = resp.Usage
	}

	if strings.Contains(responseText, giehandlers.StreamingEndMsg) {
		ip.reqCtx.ResponseComplete = true
		metrics.RecordInputTokens(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName, ip.reqCtx.Usage.PromptTokens)
		metrics.RecordOutputTokens(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName, ip.reqCtx.Usage.CompletionTokens)
		cachedToken := 0
		if ip.reqCtx.Usage.PromptTokenDetails != nil {
			cachedToken = ip.reqCtx.Usage.PromptTokenDetails.CachedTokens
		}
		metrics.RecordPromptCachedTokens(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName, cachedToken)
	}
}

func (ip *inferenceProxyHelper) handleEndOfResponse() {
	logger := log.FromContext(ip.req.Context())
	logger.V(logutil.TRACE).Info("stream completed")
	ctx := ip.req.Context()

	if ip.reqCtx.ModelServerStreaming {
		ip.reqCtx.ResponseComplete = true
		if _, err := ip.director.HandleResponseBodyComplete(ctx, ip.reqCtx); err != nil {
			logger.Error(err, "error in HandleResponseBodyComplete")
		}

		ip.reqCtx.ResponseCompleteTimestamp = time.Now()
		metrics.RecordNormalizedTimePerOutputToken(ctx, ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName,
			ip.reqCtx.RequestReceivedTimestamp, ip.reqCtx.ResponseCompleteTimestamp, ip.reqCtx.Usage.CompletionTokens)
	} else {
		var responseBody map[string]any
		// Don't send a 500 on a response error. Just let the message passthrough and log our error for debugging purposes.
		// We assume the body is valid JSON, err messages are not guaranteed to be json, and so capturing and sending a 500 obfuscates the response message.
		// Using the standard 'err' var will send an immediate error response back to the caller.
		var responseErr error
		responseErr = json.Unmarshal(ip.body, &responseBody)
		if responseErr != nil {
			logger.V(logutil.DEFAULT).Error(responseErr, "Error unmarshalling request body", "body", string(ip.body))
		}
		err := ip.HandleResponseBody(ctx, responseBody)
		if err != nil {
			if logger.V(logutil.DEBUG).Enabled() {
				logger.V(logutil.DEBUG).Error(err, "Failed to process response body", "request", ip.req)
			} else {
				logger.V(logutil.DEFAULT).Error(err, "Failed to process response body")
			}
			return
		}
		if ip.reqCtx.ResponseComplete {
			ip.reqCtx.ResponseCompleteTimestamp = time.Now()
			metrics.RecordRequestLatencies(ctx, ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName,
				ip.reqCtx.RequestReceivedTimestamp, ip.reqCtx.ResponseCompleteTimestamp)
			metrics.RecordInputTokens(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName,
				ip.reqCtx.Usage.PromptTokens)
			metrics.RecordOutputTokens(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName,
				ip.reqCtx.Usage.CompletionTokens)
			cachedToken := 0
			if ip.reqCtx.Usage.PromptTokenDetails != nil {
				cachedToken = ip.reqCtx.Usage.PromptTokenDetails.CachedTokens
			}
			metrics.RecordPromptCachedTokens(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName, cachedToken)
		}
	}
	metrics.RecordRequestLatencies(ctx, ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName,
		ip.reqCtx.RequestReceivedTimestamp, ip.reqCtx.ResponseCompleteTimestamp)
	metrics.RecordResponseSizes(ip.reqCtx.IncomingModelName, ip.reqCtx.TargetModelName, ip.reqCtx.ResponseSize)

}
