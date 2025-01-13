package main

import (
	"context"
	"flag"
	"fmt"
	"time"

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
	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/server"
)

var (
	port = flag.Int(
		"port",
		9002,
		"gRPC port")
	targetPodHeader = flag.String(
		"targetPodHeader",
		"target-pod",
		"Header key used by Envoy to route to the appropriate pod. This must match Envoy configuration.")
	serverPoolName = flag.String(
		"serverPoolName",
		"",
		"Name of the serverPool this Endpoint Picker is associated with.")
	serviceName = flag.String(
		"serviceName",
		"",
		"Name of the service that will be used to read the endpointslices from")
	namespace = flag.String(
		"namespace",
		"default",
		"The Namespace that the server pool should exist in.")
	zone = flag.String(
		"zone",
		"",
		"The zone that this instance is created in. Will be passed to the corresponding endpointSlice. ")
	refreshPodsInterval = flag.Duration(
		"refreshPodsInterval",
		10*time.Second,
		"interval to refresh pods")
	refreshMetricsInterval = flag.Duration(
		"refreshMetricsInterval",
		50*time.Millisecond,
		"interval to refresh metrics")

	scheme = runtime.NewScheme()
)

type healthServer struct{}

func (s *healthServer) Check(
	ctx context.Context,
	in *healthPb.HealthCheckRequest,
) (*healthPb.HealthCheckResponse, error) {
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

	vars := &server.ExtProcServerVars{
		Port:                   *port,
		TargetPodHeader:        *targetPodHeader,
		ServerPoolName:         *serverPoolName,
		ServiceName:            *serviceName,
		Namespace:              *namespace,
		Zone:                   *zone,
		RefreshPodsInterval:    *refreshPodsInterval,
		RefreshMetricsInterval: *refreshMetricsInterval,
		Scheme:                 scheme,
	}
	datastore := backend.NewK8sDataStore()
	pp := backend.NewProvider(&vllm.PodMetricsClientImpl{}, datastore)
	if err := pp.Init(*refreshPodsInterval, *refreshMetricsInterval); err != nil {
		klog.Fatalf("failed to initialize: %v", err)
	}

	s, lis, err := server.RunExtProcWithConfig(vars, ctrl.GetConfigOrDie(), pp, datastore)
	if err != nil {
		klog.Fatalf("Ext-proc failed with the err: %v", err)
	}

	err = s.Serve(lis)

	if err != nil {
		klog.Fatalf("Ext-proc failed with the err: %v", err)
	}
	healthPb.RegisterHealthServer(s, &healthServer{})
}
