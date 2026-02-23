package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"github.com/llm-d/llm-d-inference-proxy/pkg/handlers"
	"github.com/llm-d/llm-d-inference-proxy/pkg/orchestrations"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/common"

	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	datalayerlogger "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer/logger"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/saturationdetector/framework/plugins/utilizationdetector"
)

type HttpServerRunner struct {
	GrpcPort                         int
	GKNN                             common.GKNN
	Datastore                        datastore.Datastore
	SecureServing                    bool
	HealthChecking                   bool
	CertPath                         string
	EnableCertReload                 bool
	RefreshPrometheusMetricsInterval time.Duration
	MetricsStalenessThreshold        time.Duration
	Director                         *requestcontrol.Director
	SaturationDetector               *utilizationdetector.Detector
	UseExperimentalDatalayerV2       bool // Pluggable data layer feature flag

	// This should only be used in tests. We won't need this once we do not inject metrics in the tests.
	// TODO:(https://github.com/kubernetes-sigs/gateway-api-inference-extension/issues/432) Cleanup
	TestPodMetricsClient *backendmetrics.FakePodMetricsClient
}

func (r *HttpServerRunner) AsRunnable(orchestrator orchestrations.OrchestrationPlugin, logger logr.Logger) manager.Runnable {
	return NoLeaderElection(manager.RunnableFunc(func(ctx context.Context) error {
		if r.UseExperimentalDatalayerV2 {
			datalayerlogger.StartMetricsLogger(ctx, r.Datastore, r.RefreshPrometheusMetricsInterval, r.MetricsStalenessThreshold)
		} else {
			backendmetrics.StartMetricsLogger(ctx, r.Datastore, r.RefreshPrometheusMetricsInterval, r.MetricsStalenessThreshold)
		}

		srv := &http.Server{}
		if r.SecureServing {
			var cert tls.Certificate
			var err error
			if r.CertPath != "" {
				cert, err = tls.LoadX509KeyPair(r.CertPath+"/tls.crt", r.CertPath+"/tls.key")
			} else {
				// Create tls based credential.
				cert, err = CreateSelfSignedTLSCertificate(logger)
			}
			if err != nil {
				return fmt.Errorf("failed to create self signed certificate - %w", err)
			}

			var tlsConfig *tls.Config
			if r.CertPath != "" && r.EnableCertReload {
				reloader, err := common.NewCertReloader(ctx, r.CertPath, &cert)
				if err != nil {
					return fmt.Errorf("failed to create cert reloader: %w", err)
				}
				tlsConfig = &tls.Config{
					GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
						return reloader.Get(), nil
					},
				}
			} else {
				tlsConfig = &tls.Config{
					Certificates: []tls.Certificate{cert},
				}
			}
			srv.TLSConfig = tlsConfig
		}

		srv.Handler = handlers.NewProxy(orchestrator, r.Datastore, r.Director)

		// Forward to the Http runnable.
		return HttpServer("http-src", srv, r.GrpcPort, r.SecureServing).Start(ctx)
	}))
}

// HttpServer converts the given Httpserver into a runnable.
// The server name is just being used for logging.
func HttpServer(name string, srv *http.Server, port int, usingTLS bool) manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		// Use "name" key as that is what manager.Server does as well.
		log := ctrl.Log.WithValues("name", name)
		log.Info("Http server starting")

		srv.BaseContext = func(net.Listener) context.Context {
			return ctx
		}

		// Start listening.
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return fmt.Errorf("Http server failed to listen - %w", err)
		}

		log.Info("Http server listening", "port", port)

		// Shutdown on context closed.
		// Terminate the server on context closed.
		// Make sure the goroutine does not leak.
		doneCh := make(chan struct{})
		defer close(doneCh)
		go func() {
			select {
			case <-ctx.Done():
				log.Info("Http server shutting down")
				srv.Shutdown(context.Background())
			case <-doneCh:
			}
		}()

		// Keep serving until terminated.
		if usingTLS {
			err = srv.ServeTLS(lis, "", "")
		} else {
			err = srv.Serve(lis)
		}
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("Http server failed - %w", err)
		}
		log.Info("Http server terminated")
		return nil
	})
}
