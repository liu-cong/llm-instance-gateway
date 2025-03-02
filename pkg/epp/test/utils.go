/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package test

import (
	"encoding/json"
	"fmt"

	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

func GenerateRequest(logger logr.Logger, prompt, model string) *extProcPb.ProcessingRequest {
	j := map[string]interface{}{
		"model":       model,
		"prompt":      prompt,
		"max_tokens":  100,
		"temperature": 0,
	}

	llmReq, err := json.Marshal(j)
	if err != nil {
		logutil.Fatal(logger, err, "Failed to unmarshal LLM request")
	}
	req := &extProcPb.ProcessingRequest{
		Request: &extProcPb.ProcessingRequest_RequestBody{
			RequestBody: &extProcPb.HttpBody{Body: llmReq},
		},
	}
	return req
}

func FakePodMetrics(index int, metrics datastore.Metrics) *datastore.PodMetrics {
	address := fmt.Sprintf("192.168.1.%d", index+1)
	pod := datastore.PodMetrics{
		Pod: datastore.Pod{
			NamespacedName: types.NamespacedName{Name: fmt.Sprintf("pod-%v", index), Namespace: "default"},
			Address:        address,
		},
		Metrics: metrics,
	}
	return &pod
}
