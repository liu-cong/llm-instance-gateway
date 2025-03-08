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

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	// Note currently the EPP treats stale metrics same as fresh.
	// TODO: https://github.com/kubernetes-sigs/gateway-api-inference-extension/issues/336
	metricsValidityPeriod = 5 * time.Second
)

type Datastore interface {
	PodList(func(PodMetrics) bool) []PodMetrics
}

func PrintMetricsForDebugging(ctx context.Context, datastore Datastore) {
	logger := log.FromContext(ctx)

	// Periodically print out the pods and metrics for DEBUGGING.
	if logger := logger.V(logutil.DEBUG); logger.Enabled() {
		go func() {
			for {
				select {
				case <-ctx.Done():
					logger.V(logutil.DEFAULT).Info("Shutting down metrics logger thread")
					return
				default:
					time.Sleep(5 * time.Second)
					podsWithFreshMetrics := datastore.PodList(func(pm PodMetrics) bool {
						return time.Since(pm.GetMetrics().UpdateTime) <= metricsValidityPeriod
					})
					podsWithStaleMetrics := datastore.PodList(func(pm PodMetrics) bool {
						return time.Since(pm.GetMetrics().UpdateTime) > metricsValidityPeriod
					})
					logger.Info("Current Pods and metrics gathered", "fresh metrics", podsWithFreshMetrics, "stale metrics", podsWithStaleMetrics)
				}
			}
		}()
	}
}
