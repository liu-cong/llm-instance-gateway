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

package scheduling

import (
	"context"
	"fmt"
	"math"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
)

// LLMRequest is a structured representation of the fields we parse out of the LLMRequest body.
type LLMRequest struct {
	Model string
	// Target models is a map of target model name to weight.
	TargetModels map[string]int
	// Resolved target model is the final target model after traffic split.
	ResolvedTargetModel string
	Critical            bool
}

func (pm *PodMetrics) String() string {
	if pm == nil {
		return ""
	}
	return fmt.Sprintf("%+v", *pm)
}

type PodMetrics struct {
	*backendmetrics.Pod
	*backendmetrics.Metrics
}

// Context holds contextual information during a scheduling operation.
type Context struct {
	context.Context
	logger       logr.Logger
	req          *LLMRequest
	podMetrics   []*PodMetrics
	maxQueueSize int
	minQueueSize int
}

func (s *Scheduler) newContext(ctx context.Context, req *LLMRequest) *Context {
	// Snapshot pod metrics from the datastore to avoid holding a lock on the datastore during scheduling.
	podMetrics := s.datastore.PodGetAll()
	pm := make([]*PodMetrics, 0, len(podMetrics))
	for _, pod := range podMetrics {
		pm = append(pm, &PodMetrics{pod.GetPod().Clone(), pod.GetMetrics().Clone()}) // Assuming PodMetrics implements the necessary interface.
	}
	logger := log.FromContext(ctx).WithValues("request", req)
	min, max := queueMinMax(pm)
	logger.Info("queue", "min", min, "max", max)
	return &Context{
		Context:      ctx,
		logger:       logger,
		req:          req,
		podMetrics:   pm,
		minQueueSize: min,
		maxQueueSize: max,
	}
}

func queueMinMax(pods []*PodMetrics) (int, int) {
	min := math.MaxInt
	max := 0
	for _, pod := range pods {
		if pod.WaitingQueueSize <= min {
			min = pod.WaitingQueueSize
		}
		if pod.WaitingQueueSize >= max {
			max = pod.WaitingQueueSize
		}
	}

	return min, max
}
