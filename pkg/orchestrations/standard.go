package orchestrations

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	errutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/error"
)

func (o *Orchestration) StandardProcessing() {
	ctx := o.req.Context()
	logger := log.FromContext(ctx)
	var err error
	o.reqCtx, err = o.director.HandleRequest(ctx, o.reqCtx)
	if err != nil {
		logger.V(logutil.DEFAULT).Error(err, "Error handling request")
		sendError(err, o.origRW)
		return
	}

	o.proxy.Rewrite = o.standardRewrite

	responseWriter := NewCopyingResponseWriter(o.origRW, o.standardResponseHeaderHandler,
		o.standardStreamingHandler, logger)
	o.proxy.ServeHTTP(responseWriter, o.req)

	o.handleEndOfResponse(responseWriter)
}

// standardRewrite is called by the ReverseProxy to rewite/redirect the request as needed
func (o *Orchestration) standardRewrite(pr *httputil.ProxyRequest) {
	targetURL, _ := url.Parse("http://" + o.reqCtx.TargetEndpoint)
	pr.SetURL(targetURL)
	o.updateOutgoingRequest(pr.Out)
	pr.SetXForwarded()
}

func (o *Orchestration) standardResponseHeaderHandler(statusCode int, headers http.Header) {
	logger := log.FromContext(o.req.Context())
	if statusCode != http.StatusOK {
		o.reqCtx.ResponseStatusCode = errutil.ModelServerError
	}
	for key, value := range headers {
		o.reqCtx.Response.Headers[key] = value[0]
		if key == "content-type" && strings.Contains(value[0], "text/event-stream") {
			o.reqCtx.ModelServerStreaming = true
			logger.V(logutil.TRACE).Info("model server is streaming response")
		}
	}

	var err error
	o.reqCtx, err = o.director.HandleResponseReceived(o.req.Context(), o.reqCtx)
	if err != nil {
		if logger.V(logutil.DEBUG).Enabled() {
			logger.V(logutil.DEBUG).Error(err, "Failed to process response headers", "request", o.req)
		} else {
			logger.V(logutil.DEFAULT).Error(err, "Failed to process response headers")
		}
	}
}

func (o *Orchestration) standardStreamingHandler(responseText string) {
	o.HandleResponseBodyModelStreaming(o.req.Context(), responseText)
}
