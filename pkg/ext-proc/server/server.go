package server

import (
	"fmt"
	"net"
	"os"
	"time"

	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/backend"
	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/handlers"
	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/scheduling"
)

type ExtProcServerVars struct {
	Port                   int
	TargetPodHeader        string
	ServerPoolName         string
	ServiceName            string
	Namespace              string
	Zone                   string
	RefreshPodsInterval    time.Duration
	RefreshMetricsInterval time.Duration
	Scheme                 *runtime.Scheme
}

func RunExtProcWithConfig(vars *ExtProcServerVars, config *rest.Config, pp *backend.Provider, datastore *backend.K8sDatastore) (*grpc.Server, net.Listener, error) {

	klog.Infof("Listening on %q", fmt.Sprintf(":%d", vars.Port))
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", vars.Port))
	if err != nil {
		klog.Fatalf("failed to listen: %v", err)
	}

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: vars.Scheme,
	})
	if err != nil {
		klog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&backend.InferencePoolReconciler{
		Datastore:      datastore,
		Scheme:         mgr.GetScheme(),
		Client:         mgr.GetClient(),
		ServerPoolName: vars.ServerPoolName,
		Namespace:      vars.Namespace,
		Record:         mgr.GetEventRecorderFor("InferencePool"),
	}).SetupWithManager(mgr); err != nil {
		klog.Error(err, "Error setting up InferencePoolReconciler")
	}

	if err := (&backend.InferenceModelReconciler{
		Datastore:      datastore,
		Scheme:         mgr.GetScheme(),
		Client:         mgr.GetClient(),
		ServerPoolName: vars.ServerPoolName,
		Namespace:      vars.Namespace,
		Record:         mgr.GetEventRecorderFor("InferenceModel"),
	}).SetupWithManager(mgr); err != nil {
		klog.Error(err, "Error setting up InferenceModelReconciler")
	}

	if err := (&backend.EndpointSliceReconciler{
		Datastore:      datastore,
		Scheme:         mgr.GetScheme(),
		Client:         mgr.GetClient(),
		Record:         mgr.GetEventRecorderFor("endpointslice"),
		ServiceName:    vars.ServiceName,
		Zone:           vars.Zone,
		ServerPoolName: vars.ServerPoolName,
	}).SetupWithManager(mgr); err != nil {
		klog.Error(err, "Error setting up EndpointSliceReconciler")
	}

	errChan := make(chan error)
	go func() {
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			klog.Error(err, "Error running manager")
			errChan <- err
		}
	}()

	s := grpc.NewServer()

	extProcPb.RegisterExternalProcessorServer(
		s,
		handlers.NewServer(
			pp,
			scheduling.NewScheduler(pp),
			vars.TargetPodHeader,
			datastore))

	klog.Infof("Starting gRPC server on port :%v", vars.Port)
	/*
		// shutdown
		var gracefulStop = make(chan os.Signal, 1)
		signal.Notify(gracefulStop, syscall.SIGTERM)
		signal.Notify(gracefulStop, syscall.SIGINT)
		go func() {
			select {
			case sig := <-gracefulStop:
				klog.Infof("caught sig: %+v", sig)
				os.Exit(0)
			case err := <-errChan:
				klog.Infof("caught error in controller: %+v", err)
				os.Exit(0)
			}

		}()
	*/
	return s, lis, err
}
