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

package metrics

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

type Datastore interface {
	PoolGet() (*v1alpha2.InferencePool, error)
	// PodMetrics operations
	// PodGetAll returns all pods and metrics, including fresh and stale.
	PodGetAll() []backendmetrics.PodMetrics
}

func FlushMetricsPeriodically(ctx context.Context, datastore Datastore, refreshPrometheusMetricsInterval time.Duration) {
	logger := log.FromContext(ctx)

	// Periodically flush prometheus metrics for inference pool
	go func() {
		for {
			select {
			case <-ctx.Done():
				logger.V(logutil.DEFAULT).Info("Shutting down prometheus metrics thread")
				return
			default:
				time.Sleep(refreshPrometheusMetricsInterval)
				flushPrometheusMetricsOnce(logger, datastore)
			}
		}
	}()
}

func flushPrometheusMetricsOnce(logger logr.Logger, datastore Datastore) {
	pool, err := datastore.PoolGet()
	if err != nil {
		// No inference pool or not initialize.
		logger.V(logutil.VERBOSE).Info("pool is not initialized, skipping flushing metrics")
		return
	}

	var kvCacheTotal float64
	var queueTotal int

	podMetrics := datastore.PodGetAll()
	logger.V(logutil.VERBOSE).Info("Flushing Prometheus Metrics", "ReadyPods", len(podMetrics))
	if len(podMetrics) == 0 {
		return
	}

	for _, pod := range podMetrics {
		kvCacheTotal += pod.GetMetrics().KVCacheUsagePercent
		queueTotal += pod.GetMetrics().WaitingQueueSize
	}

	podTotalCount := len(podMetrics)
	RecordInferencePoolAvgKVCache(pool.Name, kvCacheTotal/float64(podTotalCount))
	RecordInferencePoolAvgQueueSize(pool.Name, float64(queueTotal/podTotalCount))
}
