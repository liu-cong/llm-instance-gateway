// Package test contains e2e tests for the ext proc while faking the backend pods.
package test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"inference.networking.x-k8s.io/llm-instance-gateway/api/v1alpha1"
	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/backend"
	"inference.networking.x-k8s.io/llm-instance-gateway/pkg/ext-proc/server"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	configPb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
)

const (
	port = 9002
)

var (
	cfg       *rest.Config
	k8sClient k8sclient.Client
	testEnv   *envtest.Environment
	scheme    = runtime.NewScheme()
)

func SKIPTestHandleRequestBody(t *testing.T) {
	tests := []struct {
		name        string
		req         *extProcPb.ProcessingRequest
		pods        []*backend.PodMetrics
		models      map[string]*v1alpha1.InferenceModel
		wantHeaders []*configPb.HeaderValueOption
		wantBody    []byte
		wantErr     bool
	}{
		{
			name: "success",
			req:  GenerateRequest("my-model"),
			models: map[string]*v1alpha1.InferenceModel{
				"my-model": {
					Spec: v1alpha1.InferenceModelSpec{
						ModelName: "my-model",
						TargetModels: []v1alpha1.TargetModel{
							{
								Name:   "my-model-v1",
								Weight: 100,
							},
						},
					},
				},
			},
			// pod-1 will be picked because it has relatively low queue size, with the requested
			// model being active, and has low KV cache.
			pods: []*backend.PodMetrics{
				{
					Pod: FakePod(0),
					Metrics: backend.Metrics{
						WaitingQueueSize:    0,
						KVCacheUsagePercent: 0.2,
						ActiveModels: map[string]int{
							"foo": 1,
							"bar": 1,
						},
					},
				},
				{
					Pod: FakePod(1),
					Metrics: backend.Metrics{
						WaitingQueueSize:    0,
						KVCacheUsagePercent: 0.1,
						ActiveModels: map[string]int{
							"foo":         1,
							"my-model-v1": 1,
						},
					},
				},
				{
					Pod: FakePod(2),
					Metrics: backend.Metrics{
						WaitingQueueSize:    10,
						KVCacheUsagePercent: 0.2,
						ActiveModels: map[string]int{
							"foo": 1,
						},
					},
				},
			},
			wantHeaders: []*configPb.HeaderValueOption{
				{
					Header: &configPb.HeaderValue{
						Key:      "target-pod",
						RawValue: []byte("address-1"),
					},
				},
				{
					Header: &configPb.HeaderValue{
						Key:      "Content-Length",
						RawValue: []byte("73"),
					},
				},
			},
			wantBody: []byte("{\"max_tokens\":100,\"model\":\"my-model-v1\",\"prompt\":\"hello\",\"temperature\":0}"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			// log.Fatalf("inference model: %v", *test.models["my-model"]) // TEMP

			client, cleanup := setUpServer(t, test.pods, test.models)
			t.Cleanup(cleanup)
			want := &extProcPb.ProcessingResponse{
				Response: &extProcPb.ProcessingResponse_RequestBody{
					RequestBody: &extProcPb.BodyResponse{
						Response: &extProcPb.CommonResponse{
							HeaderMutation: &extProcPb.HeaderMutation{
								SetHeaders: test.wantHeaders,
							},
							BodyMutation: &extProcPb.BodyMutation{
								Mutation: &extProcPb.BodyMutation_Body{
									Body: test.wantBody,
								},
							},
						},
					},
				},
			}
			res, err := sendRequest(t, client, test.req)

			if (err != nil) != test.wantErr {
				t.Fatalf("Unexpected error, got %v, want %v", err, test.wantErr)
			}

			if diff := cmp.Diff(want, res, protocmp.Transform()); diff != "" {
				t.Errorf("Unexpected response, (-want +got): %v", diff)
			}
		})
	}

}

func TestKubeInferenceModelRequest(t *testing.T) {

	log.Print("==== Start of TestKubeInferenceModelRequest") // logging

	// Set up mock k8s API Client
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	log.Print("====Started env")
	if err != nil {
		log.Fatalf("Failed to start test environment, cfg: %v error: %v", cfg, err)
	}
	t.Logf("=== Testenv config: %+v", testEnv.Config)

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	log.Print("===added scheme")

	k8sClient, err = k8sclient.New(cfg, k8sclient.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("Failed to start k8s Client: %v", err)
	} else if k8sClient == nil {
		log.Fatalf("No error, but returned kubernetes client is nil, cfg: %v", cfg)
	}

	pods := []*backend.PodMetrics{
		{
			Pod: FakePod(0),
			Metrics: backend.Metrics{
				WaitingQueueSize:    0,
				KVCacheUsagePercent: 0.2,
				ActiveModels: map[string]int{
					"foo": 1,
					"bar": 1,
				},
			},
		},
		{
			Pod: FakePod(1),
			Metrics: backend.Metrics{
				WaitingQueueSize:    0,
				KVCacheUsagePercent: 0.1,
				ActiveModels: map[string]int{
					"foo":            1,
					"sql-lora-1fdg2": 1,
				},
			},
		},
		{
			Pod: FakePod(2),
			Metrics: backend.Metrics{
				WaitingQueueSize:    10,
				KVCacheUsagePercent: 0.2,
				ActiveModels: map[string]int{
					"foo": 1,
				},
			},
		},
	}
	client, cleanup := setUpHermeticServer(t, cfg, pods)
	t.Cleanup(cleanup)

	req := GenerateRequest("sql-lora")
	time.Sleep(time.Minute)
	t.Logf("====Sending request: %v", req)
	res, err := sendRequest(t, client, req)
	t.Logf("====Got response %v: %v", res, err)
}

func setUpServer(t *testing.T, pods []*backend.PodMetrics, models map[string]*v1alpha1.InferenceModel) (client extProcPb.ExternalProcessor_ProcessClient, cleanup func()) {
	server := StartExtProc(port, time.Second, time.Second, pods, models)

	address := fmt.Sprintf("localhost:%v", port)
	// Create a grpc connection
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect to %v: %v", address, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client, err = extProcPb.NewExternalProcessorClient(conn).Process(ctx)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	return client, func() {
		cancel()
		conn.Close()
		server.GracefulStop()
	}
}

func setUpHermeticServer(t *testing.T, cfg *rest.Config, pods []*backend.PodMetrics) (client extProcPb.ExternalProcessor_ProcessClient, cleanup func()) {
	t.Logf("===Setting up hermetic server")
	klog.InitFlags(nil)
	flag.Parse()
	// Configure klog verbosity levels to print ext proc logs.
	_ = flag.Lookup("v").Value.Set("3")

	serverVars := &server.ExtProcServerVars{
		Port:            port,
		TargetPodHeader: "target-pod",
		// NOTE: This is important to match the InferenceModel CR.
		ServerPoolName:         "vllm-llama2-7b-pool",
		ServiceName:            "",
		Namespace:              "default",
		Zone:                   "",
		RefreshPodsInterval:    10 * time.Second,
		RefreshMetricsInterval: 50 * time.Millisecond,
		Scheme:                 scheme,
	}

	ps := make(backend.PodSet)
	pms := make(map[backend.Pod]*backend.PodMetrics)
	for _, pod := range pods {
		ps[pod.Pod] = true
		pms[pod.Pod] = pod
	}
	pmc := &backend.FakePodMetricsClient{Res: pms}
	// log.Printf("(fake) pod metrics client: %+v", pmc) // logging
	/*
		inferenceModel := &v1alpha1.InferenceModel{}
		err := k8sClient.Get(context.Background(), k8sclient.ObjectKey{
			Namespace: "default",
			Name:      "inferencemodel-sample",
		}, inferenceModel)
		if err != nil {
			log.Fatalf("unable to retrieve inferenceModel %v: %v", "inferencemodel-sample", err)
		}
		models := map[string]*v1alpha1.InferenceModel{
			inferenceModel.GetName(): inferenceModel,
		}
	*/
	//log.Printf("#### datastore before: %+v", datastore)
	pp := backend.NewProvider(pmc, backend.NewK8sDataStore())
	if err := pp.Init(serverVars.RefreshPodsInterval, serverVars.RefreshMetricsInterval); err != nil {
		log.Fatalf("failed to initialize: %v", err)
	}
	// log.Printf("pod provider: %+v", pp) // logging

	//log.Printf("inference model: %+v", inferenceModel.Spec) // TEMP

	datastore := backend.NewK8sDataStore()
	server, lis, err := server.RunExtProcWithConfig(serverVars, cfg, pp, datastore)
	if err != nil {
		log.Fatalf("Ext-proc failed with the err: %v", err)
	}
	t.Logf("#### [Before] datastore inference models: %+v", datastore.GetInferenceModels()) // logging

	// Unmarshal CRDs from file into structs
	manifestsPath := filepath.Join("..", "..", "..", "examples", "poc", "manifests", "inferencepool-with-model.yaml")
	docs, err := readDocuments(manifestsPath)
	if err != nil {
		log.Fatalf("Can't read object manifests at path %v, %v", manifestsPath, err)
	}

	var inferenceModels []*v1alpha1.InferenceModel
	for _, doc := range docs {
		// log.Printf("#### doc (yaml):%s", doc)
		inferenceModel := &v1alpha1.InferenceModel{}
		if err = yaml.Unmarshal(doc, inferenceModel); err != nil {
			log.Fatalf("Can't unmarshal object: %v", doc)
		}
		// log.Printf("#### inferenceModel.Kind: %v", inferenceModel.Kind)
		// log.Printf("#### object %+v", inferenceModel.Spec)
		if inferenceModel.Kind != "InferenceModel" {
			continue
		}
		inferenceModels = append(inferenceModels, inferenceModel)
	}
	t.Logf("=== Inference models to add: %+v", inferenceModels)
	for _, model := range inferenceModels {
		t.Logf("=== Creating inference model: %+v", model)
		if err := k8sClient.Create(context.Background(), model); err != nil {
			log.Fatalf("unable to create inferenceModel %v: %v", model.GetName(), err)
		}
	}

	reflection.Register(server)
	go server.Serve(lis)

	// Wait the reconciler to populate the datastore.
	time.Sleep(10 * time.Second)
	log.Printf("#### [After] datastore inference models: %+v", datastore.GetInferenceModels()) // logging
	//log.Fatalf("STOP")

	address := fmt.Sprintf("localhost:%v", port)
	// Create a grpc connection
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect to %v: %v", address, err)
	}

	// log.Printf("#### connection: %+v", conn) // logging

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client, err = extProcPb.NewExternalProcessorClient(conn).Process(ctx)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	return client, func() {
		cancel()
		conn.Close()
		server.GracefulStop()
	}
}

func sendRequest(t *testing.T, client extProcPb.ExternalProcessor_ProcessClient, req *extProcPb.ProcessingRequest) (*extProcPb.ProcessingResponse, error) {
	t.Logf("Sending request: %v", req)
	if err := client.Send(req); err != nil {
		t.Logf("Failed to send request %+v: %v", req, err)
		return nil, err
	}

	res, err := client.Recv()
	if err != nil {
		t.Logf("Failed to receive: %v", err)
		return nil, err
	}
	t.Logf("Received request %+v", res)
	return res, err
}

// readDocuments reads documents from file.
func readDocuments(fp string) ([][]byte, error) {
	b, err := os.ReadFile(fp)
	if err != nil {
		return nil, err
	}

	docs := [][]byte{}
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(b)))
	for {
		// Read document
		doc, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, err
		}

		docs = append(docs, doc)
	}

	return docs, nil
}
