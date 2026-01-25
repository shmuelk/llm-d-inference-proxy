package runner

import (
	"sync/atomic"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/common"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	gieplugins "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/saturationdetector/framework/plugins/utilizationdetector"
	runserver "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/server"

	"github.com/llm-d/llm-d-inference-proxy/pkg/orchestrations"
	"github.com/llm-d/llm-d-inference-proxy/pkg/server"
)

type ProxyRunnerHelper struct {
	orchestrator orchestrations.OrchestrationPlugin
}

func (h *ProxyRunnerHelper) CreateAndRegisterServer(ds datastore.Datastore, opts *runserver.Options,
	gknn common.GKNN, director *requestcontrol.Director, saturationDetector *utilizationdetector.Detector,
	useExperimentalDatalayerV2 bool, mgr ctrl.Manager, logger logr.Logger) error {

	serverRunner := &server.HttpServerRunner{
		GrpcPort:                         opts.GRPCPort,
		GKNN:                             gknn,
		Datastore:                        ds,
		SecureServing:                    opts.SecureServing,
		HealthChecking:                   opts.HealthChecking,
		CertPath:                         opts.CertPath,
		EnableCertReload:                 opts.EnableCertReload,
		RefreshPrometheusMetricsInterval: opts.RefreshPrometheusMetricsInterval,
		MetricsStalenessThreshold:        opts.MetricsStalenessThreshold,
		Director:                         director,
		SaturationDetector:               saturationDetector,
		UseExperimentalDatalayerV2:       useExperimentalDatalayerV2,
	}
	if err := mgr.Add(serverRunner.AsRunnable(h.orchestrator, ctrl.Log.WithName("ext-proc"))); err != nil {
		logger.Error(err, "Failed to register ext-proc gRPC server runnable")
		return err
	}
	logger.Info("ExtProc server runner added to manager.")
	return nil
}

func (h *ProxyRunnerHelper) RegisterHealthServer(mgr ctrl.Manager, logger logr.Logger, ds datastore.Datastore,
	port int, isLeader *atomic.Bool, leaderElectionEnabled bool) error {
	return nil
}

func (h *ProxyRunnerHelper) AddPlugins(pluginObjects ...gieplugins.Plugin) {
	for _, plugin := range pluginObjects {
		if orchestrationPlugin, ok := plugin.(orchestrations.OrchestrationPlugin); ok {
			h.orchestrator = orchestrationPlugin
		}
	}
}
