package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	giehandlers "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/handlers"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metadata"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	errutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/error"
	requtil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
)

const (
	// defaultFairnessID is the default fairness ID used when no ID is provided in the request.
	// This ensures that requests without explicit fairness identifiers are still grouped and managed by the Flow Control
	// system.
	defaultFairnessID = "default-flow"

	// schemeKey is the Envoy Pseudo Header used for the scheme of the request
	schemeKey = ":scheme"
	// methodKey is the Envoy Pseudo Header used for the method of the request
	methodKey = ":method"
	// pathKey is the Envoy Pseudo Header used for the path of the request
	pathKey = ":path"
)

var (
	// EnvoyHeaders are sent by Envoy and are added here. However, as they
	// are invalid header names, they must be deleted here
	EnvoyHeaders = sets.New(
		strings.ToLower(schemeKey),
		strings.ToLower(methodKey),
		strings.ToLower(pathKey),
	)
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

	helper := newInferenceProxyHelper(ip.ds, ip.director, req, rw)
	helper.reqCtx.Request.Headers[requtil.RequestIdHeaderKey] = requestID // update in headers so director can consume it

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
		Rewrite: helper.rewrite,
	}
	proxy.ServeHTTP(helper, req)

	helper.handleEndOfResponse()
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
	reqCtx.Request.Headers[schemeKey] = req.URL.Scheme
	reqCtx.Request.Headers[methodKey] = req.Method
	reqCtx.Request.Headers[pathKey] = req.URL.Path

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
