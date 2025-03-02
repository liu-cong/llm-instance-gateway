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

package datastore

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

type FakePodMetricsClient struct {
	errMu sync.RWMutex
	Err   map[types.NamespacedName]error
	resMu sync.RWMutex
	Res   map[types.NamespacedName]*PodMetrics
}

func (f *FakePodMetricsClient) FetchMetrics(ctx context.Context, existing *PodMetrics, port int32) (*PodMetrics, error) {
	f.errMu.RLock()
	err, ok := f.Err[existing.NamespacedName]
	f.errMu.RUnlock()
	if ok {
		return nil, err
	}
	f.resMu.RLock()
	res, ok := f.Res[existing.NamespacedName]
	f.resMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no pod found: %v", existing.NamespacedName)
	}
	log.FromContext(ctx).V(logutil.VERBOSE).Info("Fetching metrics for pod", "existing", existing, "new", res)
	return res.Clone(), nil
}

func (f *FakePodMetricsClient) SetRes(new map[types.NamespacedName]*PodMetrics) {
	f.resMu.Lock()
	defer f.resMu.Unlock()
	f.Res = new
}

func (f *FakePodMetricsClient) SetErr(new map[types.NamespacedName]error) {
	f.errMu.Lock()
	defer f.errMu.Unlock()
	f.Err = new
}

type FakeDataStore struct {
	Res map[string]*v1alpha2.InferenceModel
}

func (fds *FakeDataStore) FetchModelData(modelName string) (returnModel *v1alpha2.InferenceModel) {
	return fds.Res[modelName]
}
