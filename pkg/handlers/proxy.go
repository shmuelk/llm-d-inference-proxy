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

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	giehandlers "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/handlers"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metadata"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	errutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/error"
)

const (
	// defaultFairnessID is the default fairness ID used when no ID is provided in the request.
	// This ensures that requests without explicit fairness identifiers are still grouped and managed by the Flow Control
	// system.
	defaultFairnessID = "default-flow"
)

func NewProxy(ds datastore.Datastore, director *requestcontrol.Director) http.Handler {
	return &inferenceProxy{
		ds:       ds,
		director: director,
	}
}

type inferenceProxy struct {
	ds       datastore.Datastore
	director *requestcontrol.Director
}

func (ip *inferenceProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	logger := log.FromContext(ctx)

	helper := inferenceProxyHelper{
		ds:       ip.ds,
		director: ip.director,
		reqCtx: &giehandlers.RequestContext{
			Request: &giehandlers.Request{
				Headers:  make(map[string]string),
				Body:     make(map[string]any),
				Metadata: make(map[string]any),
			},
		},
	}

	err := prepareRequest(helper.reqCtx, req)
	if err != nil {
		sendError(err, rw)
		return
	}

	helper.reqCtx, err = ip.director.HandleRequest(ctx, helper.reqCtx)
	if err != nil {
		logger.V(logutil.DEFAULT).Error(err, "Error handling request")
		sendError(err, rw)
		return
	}

	proxy := httputil.ReverseProxy{
		Rewrite: helper.Rewrite,
	}
	proxy.ServeHTTP(rw, req)
}

type inferenceProxyHelper struct {
	ds       datastore.Datastore
	director *requestcontrol.Director
	reqCtx   *giehandlers.RequestContext
}

// Rewrite is called by the ReverseProxy to rewite/redirect the request as needed
func (ip *inferenceProxyHelper) Rewrite(pr *httputil.ProxyRequest) {
	targetURL, _ := url.Parse("http://" + ip.reqCtx.TargetEndpoint)
	pr.SetURL(targetURL)
	ip.updateOutgoingRequest(pr.In.Context(), pr.Out)
}

func (ip *inferenceProxyHelper) updateOutgoingRequest(ctx context.Context, req *http.Request) {
	requestBodyBytes, err := json.Marshal(ip.reqCtx.Request.Body)
	if err != nil {
		logger := log.FromContext(ctx)
		logger.V(logutil.DEFAULT).Error(err, "Error marshalling request body")
	}
	// Update RequestSize to match marshalled body for Content-Length header.
	ip.reqCtx.RequestSize = len(requestBodyBytes)
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewBuffer(requestBodyBytes))
	req.ContentLength = int64(ip.reqCtx.RequestSize)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", ip.reqCtx.RequestSize))

}

func prepareRequest(reqCtx *giehandlers.RequestContext, req *http.Request) error {
	ctx := req.Context()
	logger := log.FromContext(ctx)
	loggerTrace := logger.V(logutil.TRACE)
	loggerTrace.Info("Processing")
	for key, values := range req.Header {
		reqCtx.Request.Headers[key] = values[0]
		switch key {
		case metadata.FlowFairnessIDKey:
			reqCtx.FairnessID = reqCtx.Request.Headers[key]
		case metadata.ObjectiveKey:
			reqCtx.ObjectiveKey = reqCtx.Request.Headers[key]
		case metadata.ModelNameRewriteKey:
			reqCtx.TargetModelName = reqCtx.Request.Headers[key]
		}
	}

	if reqCtx.FairnessID == "" {
		reqCtx.FairnessID = defaultFairnessID
	}

	body, err := io.ReadAll(req.Body)
	defer req.Body.Close()
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
		if errUnmarshal := json.Unmarshal(body, &reqCtx.Request.Body); errUnmarshal != nil {
			if logger.V(logutil.DEBUG).Enabled() {
				logger.Info("Error unmarshaling request body", "body", string(body), "err", errUnmarshal)
			}
			err = errutil.Error{
				Code: errutil.BadRequest,
				Msg:  "Error unmarshaling request body",
			}
		} else {
			// Body stream complete. Capture raw size for flow control.
			reqCtx.RequestSize = len(body)
		}
	}
	return err
}

func sendError(err error, rw http.ResponseWriter) {
	rw.WriteHeader(500)
	rw.Write([]byte(err.Error()))
}
