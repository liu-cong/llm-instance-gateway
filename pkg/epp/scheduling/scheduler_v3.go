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
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/prefix"
	envutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// Config holds all the configuration values for the scheduler
type ConfigV3 struct {
	Config
	TopKScore              int
	KVCacheScoreWeight     float64
	QueueScoreWeight       float64
	PrefixCacheScoreWeight float64
}

// LoadConfig loads configuration from environment variables
func LoadConfigV3() ConfigV3 {
	// Use a default logger for initial configuration loading
	baseLogger := log.Log.WithName("scheduling-config")

	config := ConfigV3{
		Config:                 config,
		TopKScore:              envutil.GetEnvInt("TOP_K_SCORE", 1, baseLogger),
		KVCacheScoreWeight:     envutil.GetEnvFloat("KV_CACHE_SCORE_WEIGHT", 1.0, baseLogger),
		QueueScoreWeight:       envutil.GetEnvFloat("QUEUE_SCORE_WEIGHT", 1.0, baseLogger),
		PrefixCacheScoreWeight: envutil.GetEnvFloat("PREFIX_CACHE_SCORE_WEIGHT", 1.0, baseLogger),
	}

	baseLogger.V(logutil.DEFAULT).Info("Scheduler configuration loaded", "config", config)

	return config
}

var configV3 = LoadConfigV3()

var (
	prefixFilter = prefix.NewPrefixCacheMatcher(64, 2>>20, time.Millisecond*100, time.Second*10)

	lowLatencyFilterV3 = &decisionTreeFilter{
		current: lowQueueFilter,
		nextOnSuccess: &decisionTreeFilter{
			current: prefixFilter,
			nextOnSuccessOrFailure: &decisionTreeFilter{
				current: leastQueueFilter,
				nextOnSuccessOrFailure: &decisionTreeFilter{
					current: leastKVCacheFilter,
				},
			},
		},
		nextOnFailure: &decisionTreeFilter{
			current: leastQueueFilter,
			nextOnSuccessOrFailure: &decisionTreeFilter{
				current: prefixFilter,
				nextOnSuccessOrFailure: &decisionTreeFilter{
					current: leastKVCacheFilter,
				},
			},
		},
	}

	sheddableRequestFilterV3 = &decisionTreeFilter{
		// When there is at least one model server that's not queuing requests, and still has KV
		// cache below a certain threshold, we consider this model server has capacity to handle
		// a sheddable request without impacting critical requests.
		current:       hasCapacityFilter,
		nextOnSuccess: lowLatencyFilterV3,
		// If all pods are queuing or running above the KVCache threshold, we drop the sheddable
		// request to make room for critical requests.
		nextOnFailure: dropRequestFilter,
	}
)

func NewSchedulerV3(datastore datastore.Datastore) *Scheduler {
	return &Scheduler{
		datastore:              datastore,
		criticalRequestFilter:  lowLatencyFilterV3,
		sheddableRequestFilter: sheddableRequestFilterV3,
		eventHandler:           prefixFilter,
	}
}
