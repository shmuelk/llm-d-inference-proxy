package orchestrations

import (
	"net/http"

	"github.com/go-logr/logr"
)

type orchestrationResponseWriter interface {
	Body() []byte
}

type BaseResponseWriter struct {
	body []byte
}

func (b *BaseResponseWriter) Body() []byte {
	return b.body
}

type responseHeaderHandler func(int, http.Header)
type streamingHandler func(string)

// CopyingResponseWriter acts a "tee" allowing the API calls to affect
// another ResponseWriter as well as storing data locally.
type CopyingResponseWriter struct {
	BaseResponseWriter
	isStreaming           bool
	targetRW              http.ResponseWriter
	responseHeaderHandler responseHeaderHandler
	streamingHandler      streamingHandler
	logger                logr.Logger
}

func NewCopyingResponseWriter(targetRW http.ResponseWriter, responseHeaderHandler responseHeaderHandler,
	streamingHandler streamingHandler, logger logr.Logger) *CopyingResponseWriter {
	return &CopyingResponseWriter{
		BaseResponseWriter: BaseResponseWriter{
			body: []byte{},
		},
		targetRW:              targetRW,
		responseHeaderHandler: responseHeaderHandler,
		streamingHandler:      streamingHandler,
		logger:                logger,
	}
}

func (crw *CopyingResponseWriter) Header() http.Header {
	return crw.targetRW.Header()
}

func (crw *CopyingResponseWriter) Write(data []byte) (int, error) {
	if crw.isStreaming {
		// Currently we punt on response parsing if the modelServer is streaming, and we just passthrough.
		responseText := string(data)
		crw.streamingHandler(responseText)
	} else {
		crw.body = append(crw.body, data...)
	}
	return crw.targetRW.Write(data)
}

func (crw *CopyingResponseWriter) WriteHeader(statusCode int) {
	crw.targetRW.WriteHeader(statusCode)
	crw.responseHeaderHandler(statusCode, crw.targetRW.Header())
}

// CollectingResponseWriter collects inside all of the headers and body written to it
type CollectingResponseWriter struct {
	BaseResponseWriter
	statusCode int
	headers    http.Header
}

func NewCollectingResponseWriter() *CollectingResponseWriter {
	return &CollectingResponseWriter{
		BaseResponseWriter: BaseResponseWriter{
			body: []byte{},
		},
		headers: http.Header{},
	}
}

func (crw *CollectingResponseWriter) Header() http.Header {
	return crw.headers
}

func (crw *CollectingResponseWriter) Write(data []byte) (int, error) {
	crw.body = append(crw.body, data...)
	return len(data), nil
}

func (crw *CollectingResponseWriter) WriteHeader(statusCode int) {
	crw.statusCode = statusCode
}
