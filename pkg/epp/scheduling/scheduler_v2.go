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

// Package scheduling implements request scheduling algorithms.
package scheduling

import (
	"context"
	"fmt"
	"math/rand"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	envutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// Config holds all the configuration values for the scheduler
type ConfigV2 struct {
	KVCacheThreshold       float64
	QueueThresholdCritical int
	QueueingThresholdLoRA  int
	LoraAffinityThreshold  float64
	TopKScore              int
	KVCacheScoreWeight     float64
	QueueScoreWeight       float64
	LoRAScoreWeight        float64
	PrefixCacheScoreWeight float64
}

// LoadConfig loads configuration from environment variables
func LoadConfigV2() ConfigV2 {
	// Use a default logger for initial configuration loading
	baseLogger := log.Log.WithName("scheduling-config")

	config := ConfigV2{
		KVCacheThreshold:       envutil.GetEnvFloat("KV_CACHE_THRESHOLD", defaultKVCacheThreshold, baseLogger),
		QueueThresholdCritical: envutil.GetEnvInt("QUEUE_THRESHOLD_CRITICAL", defaultQueueThresholdCritical, baseLogger),
		QueueingThresholdLoRA:  envutil.GetEnvInt("QUEUING_THRESHOLD_LORA", defaultQueueingThresholdLoRA, baseLogger),
		LoraAffinityThreshold:  envutil.GetEnvFloat("LORA_AFFINITY_THRESHOLD", defaultLoraAffinityThreshold, baseLogger),
		TopKScore:              envutil.GetEnvInt("TOP_K_SCORE", 1, baseLogger),
		KVCacheScoreWeight:     envutil.GetEnvFloat("KV_CACHE_SCORE_WEIGHT", 1.0, baseLogger),
		QueueScoreWeight:       envutil.GetEnvFloat("QUEUE_SCORE_WEIGHT", 1.0, baseLogger),
		LoRAScoreWeight:        envutil.GetEnvFloat("LORA_SCORE_WEIGHT", 1.0, baseLogger),
		PrefixCacheScoreWeight: envutil.GetEnvFloat("PREFIX_CACHE_SCORE_WEIGHT", 1.0, baseLogger),
	}

	baseLogger.V(logutil.DEFAULT).Info("Scheduler configuration loaded", "config", config)

	return config
}

var configV2 = LoadConfigV2()

var (
	lowLatencyFilterV2 = &filter{
		simpleFilter:           lowQueueFilter,
		nextOnSuccessOrFailure: topKFilter,
	}

	topKFilter = &scoreFilter{
		name: "low latency filter",
		k:    configV2.TopKScore,
		sc: scorerChain{
			loraScorer,
			queueScorer,
			kvCacheScorer,
		},
	}
)

func NewSchedulerV2(datastore datastore.Datastore) *SchedulerV2 {
	return &SchedulerV2{
		datastore: datastore,
	}
}

type SchedulerV2 struct {
	datastore datastore.Datastore
}

// Schedule finds the target pod based on metrics and the requested lora adapter.
func (s *SchedulerV2) Schedule(ctx context.Context, req *LLMRequest) (targetPod *PodMetrics, err error) {
	logger := log.FromContext(ctx).WithValues("request", req)

	// Snapshot pod metrics from the datastore to:
	// 1. Reduce concurrent access to the datastore.
	// 2. Ensure consistent data during the scheduling operation of a request.
	sCtx := newContext(ctx, req, toSchedulerPodMetrics(s.datastore.PodGetAll()))
	logger.V(logutil.DEBUG).Info(fmt.Sprintf("Scheduling a request. Metrics: %+v", sCtx.podsSnapshot))

	var filter Filter
	if req.Critical {
		filter = lowLatencyFilter
	} else {
		filter = sheddableRequestFilter
	}

	pods, err := filter.Filter(sCtx, sCtx.podsSnapshot)
	if err != nil || len(pods) == 0 {
		return nil, fmt.Errorf("failed to apply filter, resulted %v pods, this should never happen: %w", len(pods), err)
	}
	logger.V(logutil.DEBUG).Info(fmt.Sprintf("Selecting a random pod from %d candidates: %+v", len(pods), pods))
	i := rand.Intn(len(pods))
	return pods[i], nil
}
