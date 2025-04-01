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
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datastore"
	envutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// Config holds all the configuration values for the scheduler
type ConfigV2 struct {
	Config
	TopKScore              int
	KVCacheScoreWeight     float64
	QueueScoreWeight       float64
	PrefixCacheScoreWeight float64
}

// LoadConfig loads configuration from environment variables
func LoadConfigV2() ConfigV2 {
	// Use a default logger for initial configuration loading
	baseLogger := log.Log.WithName("scheduling-config")

	config := ConfigV2{
		Config:                 config,
		TopKScore:              envutil.GetEnvInt("TOP_K_SCORE", 1, baseLogger),
		KVCacheScoreWeight:     envutil.GetEnvFloat("KV_CACHE_SCORE_WEIGHT", 1.0, baseLogger),
		QueueScoreWeight:       envutil.GetEnvFloat("QUEUE_SCORE_WEIGHT", 1.0, baseLogger),
		PrefixCacheScoreWeight: envutil.GetEnvFloat("PREFIX_CACHE_SCORE_WEIGHT", 1.0, baseLogger),
	}

	baseLogger.V(logutil.DEFAULT).Info("Scheduler configuration loaded", "config", config)

	return config
}

var configV2 = LoadConfigV2()

var (
	lowLatencyFilterV2 = &filter{
		simpleFilter: lowQueueFilter,
		nextOnSuccess: &filter{
			simpleFilter: loRAAffinityFilter,
			nextOnSuccessOrFailure: &filter{
				simpleFilter: leastQueueFilter,
				nextOnSuccessOrFailure: &filter{
					simpleFilter: leastKVCacheFilter,
				},
			},
		},
		nextOnFailure: &filter{
			simpleFilter: leastQueueFilter,
			nextOnSuccessOrFailure: &filter{
				simpleFilter: loRAAffinityFilter,
				nextOnSuccessOrFailure: &filter{
					simpleFilter: leastKVCacheFilter,
				},
			},
		},
	}

	topKFilter = &scoreFilter{
		name: "low latency filter",
		k:    configV2.TopKScore,
		sc: scorerChain{
			queueScorer,
			kvCacheScorer,
		},
	}

	sheddableRequestFilterV2 = &filter{
		// When there is at least one model server that's not queuing requests, and still has KV
		// cache below a certain threshold, we consider this model server has capacity to handle
		// a sheddable request without impacting critical requests.
		simpleFilter:  hasCapacityFilter,
		nextOnSuccess: lowLatencyFilterV2,
		// If all pods are queuing or running above the KVCache threshold, we drop the sheddable
		// request to make room for critical requests.
		nextOnFailure: dropRequestFilter,
	}
)

func NewSchedulerV2(datastore datastore.Datastore) *Scheduler {
	return &Scheduler{
		datastore:              datastore,
		criticalRequestFilter:  lowLatencyFilterV2,
		sheddableRequestFilter: sheddableRequestFilterV2,
	}
}
