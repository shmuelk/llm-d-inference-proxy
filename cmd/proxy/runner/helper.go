package runner

import (
	"sync/atomic"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/common"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/saturationdetector/framework/plugins/utilizationdetector"
	runserver "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/server"
)

type ProxyRunnerHelper struct{}

func (h *ProxyRunnerHelper) CreateAndRegisterServer(ds datastore.Datastore, opts *runserver.Options,
	gknn common.GKNN, director *requestcontrol.Director, saturationDetector *utilizationdetector.Detector,
	useExperimentalDatalayerV2 bool, mgr ctrl.Manager, logger logr.Logger) error {
	return nil
}

func (h *ProxyRunnerHelper) RegisterHealthServer(mgr ctrl.Manager, logger logr.Logger, ds datastore.Datastore,
	port int, isLeader *atomic.Bool, leaderElectionEnabled bool) error {
	return nil
}
