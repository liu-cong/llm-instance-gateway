package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthPb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"inference.networking.x-k8s.io/llm-instance-gateway/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	klog "k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/backend"
	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/backend/vllm"
	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/handlers"
	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/scheduling"
)

var (
	port                   = flag.Int("port", 9002, "gRPC port")
	targetPodHeader        = flag.String("targetPodHeader", "target-pod", "the header key for the target pod address to instruct Envoy to send the request to. This must match Envoy configuration.")
	serverPoolName         = flag.String("serverPoolName", "", "Name of the serverPool this Endpoint Picker is associated with.")
	serviceName            = flag.String("serviceName", "", "Name of the service that will be used to read the endpointslices from")
	namespace              = flag.String("namespace", "default", "The Namespace that the server pool should exist in.")
	zone                   = flag.String("zone", "", "The zone that this instance is created in. Will be passed to the corresponding endpointSlice. ")
	refreshPodsInterval    = flag.Duration("refreshPodsInterval", 10*time.Second, "interval to refresh pods")
	refreshMetricsInterval = flag.Duration("refreshMetricsInterval", 50*time.Millisecond, "interval to refresh metrics")
	scheme                 = runtime.NewScheme()
)

type healthServer struct{}

func (s *healthServer) Check(ctx context.Context, in *healthPb.HealthCheckRequest) (*healthPb.HealthCheckResponse, error) {
	klog.Infof("Handling grpc Check request + %s", in.String())
	return &healthPb.HealthCheckResponse{Status: healthPb.HealthCheckResponse_SERVING}, nil
}

func (s *healthServer) Watch(in *healthPb.HealthCheckRequest, srv healthPb.Health_WatchServer) error {
	return status.Error(codes.Unimplemented, "Watch is not implemented")
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {

	klog.InitFlags(nil)
	flag.Parse()

	ctrl.SetLogger(klog.TODO())

	// Print all flag values
	flags := "Flags: "
	flag.VisitAll(func(f *flag.Flag) {
		flags += fmt.Sprintf("%s=%v; ", f.Name, f.Value)
	})
	klog.Info(flags)

	klog.Infof("Listening on %q", fmt.Sprintf(":%d", *port))
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		klog.Fatalf("failed to listen: %v", err)
	}

	datastore := backend.NewK8sDataStore()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		klog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&backend.InferencePoolReconciler{
		Datastore:      datastore,
		Scheme:         mgr.GetScheme(),
		Client:         mgr.GetClient(),
		ServerPoolName: *serverPoolName,
		Namespace:      *namespace,
		Record:         mgr.GetEventRecorderFor("InferencePool"),
	}).SetupWithManager(mgr); err != nil {
		klog.Error(err, "Error setting up InferencePoolReconciler")
	}

	if err := (&backend.InferenceModelReconciler{
		Datastore:      datastore,
		Scheme:         mgr.GetScheme(),
		Client:         mgr.GetClient(),
		ServerPoolName: *serverPoolName,
		Namespace:      *namespace,
		Record:         mgr.GetEventRecorderFor("InferenceModel"),
	}).SetupWithManager(mgr); err != nil {
		klog.Error(err, "Error setting up InferenceModelReconciler")
	}

	if err := (&backend.EndpointSliceReconciler{
		Datastore:      datastore,
		Scheme:         mgr.GetScheme(),
		Client:         mgr.GetClient(),
		Record:         mgr.GetEventRecorderFor("endpointslice"),
		ServiceName:    *serviceName,
		Zone:           *zone,
		ServerPoolName: *serverPoolName,
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

	pp := backend.NewProvider(&vllm.PodMetricsClientImpl{}, datastore)
	if err := pp.Init(*refreshPodsInterval, *refreshMetricsInterval); err != nil {
		klog.Fatalf("failed to initialize: %v", err)
	}
	extProcPb.RegisterExternalProcessorServer(s, handlers.NewServer(pp, scheduling.NewScheduler(pp), *targetPodHeader, datastore))
	healthPb.RegisterHealthServer(s, &healthServer{})

	klog.Infof("Starting gRPC server on port :%v", *port)

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

	s.Serve(lis)

}
