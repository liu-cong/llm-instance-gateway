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
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	ModelNameIndexKey = "spec.modelName"
	// Note currently the EPP treats stale metrics same as fresh.
	// TODO: https://github.com/kubernetes-sigs/gateway-api-inference-extension/issues/336
	metricsValidityPeriod = 5 * time.Second
)

var (
	errPoolNotSynced = errors.New("InferencePool is not initialized in data store")
	once             sync.Once
)

// The datastore is a local cache of relevant data for the given InferencePool (currently all pulled from k8s-api)
type Datastore interface {
	// InferencePool operations
	PoolSet(pool *v1alpha2.InferencePool)
	PoolGet() (*v1alpha2.InferencePool, error)
	PoolHasSynced() bool
	PoolLabelsMatch(podLabels map[string]string) bool

	// InferenceModel operations
	ModelSetIfOlder(infModel *v1alpha2.InferenceModel) bool
	ModelGet(modelName string) *v1alpha2.InferenceModel
	ModelDelete(namespacedName types.NamespacedName) *v1alpha2.InferenceModel
	ModelResync(ctx context.Context, ctrlClient client.Client, modelName string) (bool, error)
	ModelGetAll() []*v1alpha2.InferenceModel

	// PodMetrics operations
	// PodGetAll returns all pods and metrics, including fresh and stale.
	PodGetAll() []*PodMetrics
	PodUpdateOrAddIfNotExist(ctx context.Context, pod *corev1.Pod) bool
	PodDelete(namespacedName types.NamespacedName)
	PodResyncAll(ctx context.Context, ctrlClient client.Client)

	// Clears the store state, happens when the pool gets deleted.
	Clear()
}

func NewDatastore(parentCtx context.Context, pmc PodMetricsClient, refreshMetricsInterval, refreshPrometheusMetricsInterval time.Duration) *datastore {
	store := &datastore{
		parentCtx:                        parentCtx,
		poolAndModelsMu:                  sync.RWMutex{},
		models:                           make(map[string]*v1alpha2.InferenceModel),
		pods:                             &sync.Map{},
		pmc:                              pmc,
		refreshMetricsInterval:           refreshMetricsInterval,
		refreshPrometheusMetricsInterval: refreshPrometheusMetricsInterval,
	}
	store.flushingMetricsPeriodically(parentCtx)
	return store
}

// Used for test only
func NewFakeDatastore(parentCtx context.Context, pmc PodMetricsClient, pods []*PodMetrics, models []*v1alpha2.InferenceModel, pool *v1alpha2.InferencePool) Datastore {
	store := NewDatastore(parentCtx, pmc, time.Millisecond, time.Millisecond)

	for _, m := range models {
		store.ModelSetIfOlder(m)
	}

	if pool != nil {
		store.PoolSet(pool)
	}

	for _, pod := range pods {
		// Making a copy since in tests we may use the same global PodMetric across tests.
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod.NamespacedName.Name,
				Namespace: pod.NamespacedName.Namespace,
			},
			Status: corev1.PodStatus{
				PodIP: pod.Address,
			},
		}
		store.PodUpdateOrAddIfNotExist(parentCtx, p)
	}

	return store
}

type datastore struct {
	// parentCtx controls the lifecycle of the background metrics goroutines that spawn up by the datastore.
	parentCtx context.Context
	// poolAndModelsMu is used to synchronize access to pool and the models map.
	poolAndModelsMu sync.RWMutex
	pool            *v1alpha2.InferencePool
	// key: InferenceModel.Spec.ModelName, value: *InferenceModel
	models map[string]*v1alpha2.InferenceModel
	// key: types.NamespacedName, value: *PodMetricsRefresher
	pods *sync.Map

	pmc                                                      PodMetricsClient
	refreshMetricsInterval, refreshPrometheusMetricsInterval time.Duration
}

func (ds *datastore) flushingMetricsPeriodically(ctx context.Context) {
	logger := log.FromContext(ctx)

	// Periodically flush prometheus metrics for inference pool
	go func() {
		for {
			select {
			case <-ctx.Done():
				logger.V(logutil.DEFAULT).Info("Shutting down prometheus metrics thread")
				return
			default:
				time.Sleep(ds.refreshPrometheusMetricsInterval)
				ds.flushPrometheusMetricsOnce(logger)
			}
		}
	}()

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
					logger.Info("Current Pods and metrics gathered", "fresh metrics", ds.podGet(FreshMetricsPredicate), "stale metrics", ds.podGet(StaleMetricsPredicate))
				}
			}
		}()
	}
}

func (ds *datastore) flushPrometheusMetricsOnce(logger logr.Logger) {
	pool, err := ds.PoolGet()
	if err != nil {
		// No inference pool or not initialize.
		logger.V(logutil.VERBOSE).Info("pool is not initialized, skipping flushing metrics")
		return
	}

	var kvCacheTotal float64
	var queueTotal int

	podMetrics := ds.PodGetAll()
	logger.V(logutil.VERBOSE).Info("Flushing Prometheus Metrics", "ReadyPods", len(podMetrics))
	if len(podMetrics) == 0 {
		return
	}

	for _, pod := range podMetrics {
		kvCacheTotal += pod.KVCacheUsagePercent
		queueTotal += pod.WaitingQueueSize
	}

	podTotalCount := len(podMetrics)
	metrics.RecordInferencePoolAvgKVCache(pool.Name, kvCacheTotal/float64(podTotalCount))
	metrics.RecordInferencePoolAvgQueueSize(pool.Name, float64(queueTotal/podTotalCount))
}

func (ds *datastore) Clear() {
	ds.poolAndModelsMu.Lock()
	defer ds.poolAndModelsMu.Unlock()
	ds.pool = nil
	ds.models = make(map[string]*v1alpha2.InferenceModel)
	ds.pods.Clear()
}

// /// InferencePool APIs ///
func (ds *datastore) PoolSet(pool *v1alpha2.InferencePool) {
	ds.poolAndModelsMu.Lock()
	defer ds.poolAndModelsMu.Unlock()
	ds.pool = pool
}

func (ds *datastore) PoolGet() (*v1alpha2.InferencePool, error) {
	ds.poolAndModelsMu.RLock()
	defer ds.poolAndModelsMu.RUnlock()
	if !ds.PoolHasSynced() {
		return nil, errPoolNotSynced
	}
	return ds.pool, nil
}

func (ds *datastore) PoolHasSynced() bool {
	ds.poolAndModelsMu.RLock()
	defer ds.poolAndModelsMu.RUnlock()
	return ds.pool != nil
}

func (ds *datastore) PoolLabelsMatch(podLabels map[string]string) bool {
	ds.poolAndModelsMu.RLock()
	defer ds.poolAndModelsMu.RUnlock()
	poolSelector := selectorFromInferencePoolSelector(ds.pool.Spec.Selector)
	podSet := labels.Set(podLabels)
	return poolSelector.Matches(podSet)
}

func (ds *datastore) ModelSetIfOlder(infModel *v1alpha2.InferenceModel) bool {
	ds.poolAndModelsMu.Lock()
	defer ds.poolAndModelsMu.Unlock()

	// Check first if the existing model is older.
	// One exception is if the incoming model object is the same, in which case, we should not
	// check for creation timestamp since that means the object was re-created, and so we should override.
	existing, exists := ds.models[infModel.Spec.ModelName]
	if exists {
		diffObj := infModel.Name != existing.Name || infModel.Namespace != existing.Namespace
		if diffObj && existing.ObjectMeta.CreationTimestamp.Before(&infModel.ObjectMeta.CreationTimestamp) {
			return false
		}
	}
	// Set the model.
	ds.models[infModel.Spec.ModelName] = infModel
	return true
}

func (ds *datastore) ModelResync(ctx context.Context, c client.Client, modelName string) (bool, error) {
	ds.poolAndModelsMu.Lock()
	defer ds.poolAndModelsMu.Unlock()

	var models v1alpha2.InferenceModelList
	if err := c.List(ctx, &models, client.MatchingFields{ModelNameIndexKey: modelName}, client.InNamespace(ds.pool.Namespace)); err != nil {
		return false, fmt.Errorf("listing models that match the modelName %s: %w", modelName, err)
	}
	if len(models.Items) == 0 {
		// No other instances of InferenceModels with this ModelName exists.
		return false, nil
	}

	var oldest *v1alpha2.InferenceModel
	for i := range models.Items {
		m := &models.Items[i]
		if m.Spec.ModelName != modelName || // The index should filter those out, but just in case!
			m.Spec.PoolRef.Name != v1alpha2.ObjectName(ds.pool.Name) || // We don't care about other pools, we could setup an index on this too!
			!m.DeletionTimestamp.IsZero() { // ignore objects marked for deletion
			continue
		}
		if oldest == nil || m.ObjectMeta.CreationTimestamp.Before(&oldest.ObjectMeta.CreationTimestamp) {
			oldest = m
		}
	}
	if oldest == nil {
		return false, nil
	}
	ds.models[modelName] = oldest
	return true, nil
}

func (ds *datastore) ModelGet(modelName string) *v1alpha2.InferenceModel {
	ds.poolAndModelsMu.RLock()
	defer ds.poolAndModelsMu.RUnlock()
	return ds.models[modelName]
}

func (ds *datastore) ModelDelete(namespacedName types.NamespacedName) *v1alpha2.InferenceModel {
	ds.poolAndModelsMu.Lock()
	defer ds.poolAndModelsMu.Unlock()
	for _, m := range ds.models {
		if m.Name == namespacedName.Name && m.Namespace == namespacedName.Namespace {
			delete(ds.models, m.Spec.ModelName)
			return m
		}
	}
	return nil
}

func (ds *datastore) ModelGetAll() []*v1alpha2.InferenceModel {
	ds.poolAndModelsMu.RLock()
	defer ds.poolAndModelsMu.RUnlock()
	res := []*v1alpha2.InferenceModel{}
	for _, v := range ds.models {
		res = append(res, v)
	}
	return res
}

// /// Pods/endpoints APIs ///

func (ds *datastore) PodGetAll() []*PodMetrics {
	return ds.podGet(AllPodsPredicates)
}

func (ds *datastore) podGet(predicate PodPredicate) []*PodMetrics {
	res := []*PodMetrics{}
	fn := func(k, v any) bool {
		pm := v.(*PodMetricsRefresher).GetPodMetrics()
		if predicate(pm) {
			res = append(res, pm)
		}
		return true
	}
	ds.pods.Range(fn)
	return res
}

type PodPredicate func(*PodMetrics) bool

var AllPodsPredicates = func(pm *PodMetrics) bool {
	return true
}

var FreshMetricsPredicate = func(pm *PodMetrics) bool {
	return time.Since(pm.UpdateTime) <= metricsValidityPeriod
}

var StaleMetricsPredicate = func(pm *PodMetrics) bool {
	return time.Since(pm.UpdateTime) > metricsValidityPeriod
}

func (ds *datastore) PodUpdateOrAddIfNotExist(ctx context.Context, pod *corev1.Pod) bool {
	log.FromContext(ds.parentCtx).V(logutil.VERBOSE).Info("Adding pod", "pod", pod.Name)
	pool, err := ds.PoolGet()
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to get the pool, this should never happen")
		return false
	}
	pm := &PodMetrics{
		Pod: Pod{
			NamespacedName: types.NamespacedName{
				Name:      pod.Name,
				Namespace: pod.Namespace,
			},
			Address: pod.Status.PodIP,
		},
		Metrics: NewMetrics(),
	}
	pmr := NewPodMetricsRefresher(ds.parentCtx, ds.pmc, pm, pool.Spec.TargetPortNumber, ds.refreshMetricsInterval)
	existingOrNew, ok := ds.pods.LoadOrStore(pm.NamespacedName, pmr)
	existingOrNewPMR := existingOrNew.(*PodMetricsRefresher)
	// It's safe to call start() multiple times.
	existingOrNewPMR.start()
	// Update pod properties if anything changed.
	existingOrNewPMR.UpdatePod(pm.Pod)
	return ok
}

func (ds *datastore) PodResyncAll(ctx context.Context, ctrlClient client.Client) {
	logger := log.FromContext(ctx)
	// Pool must exist to invoke this function.
	pool, _ := ds.PoolGet()
	podList := &corev1.PodList{}
	if err := ctrlClient.List(ctx, podList, &client.ListOptions{
		LabelSelector: selectorFromInferencePoolSelector(pool.Spec.Selector),
		Namespace:     pool.Namespace,
	}); err != nil {
		log.FromContext(ctx).V(logutil.DEFAULT).Error(err, "Failed to list clients")
		return
	}

	activePods := make(map[string]bool)
	for _, pod := range podList.Items {
		if podIsReady(&pod) {
			activePods[pod.Name] = true
			ds.PodUpdateOrAddIfNotExist(ctx, &pod)
		}
	}

	// Remove pods that don't belong to the pool or not ready any more.
	deleteFn := func(k, v any) bool {
		pmr := v.(*PodMetricsRefresher)
		if exist := activePods[pmr.GetPodMetrics().Pod.NamespacedName.Name]; !exist {
			logger.V(logutil.VERBOSE).Info("Removing pod", "pod", pmr.GetPodMetrics().Pod)
			ds.PodDelete(pmr.GetPodMetrics().Pod.NamespacedName)
		}
		return true
	}
	ds.pods.Range(deleteFn)
}

func (ds *datastore) PodDelete(namespacedName types.NamespacedName) {
	v, ok := ds.pods.LoadAndDelete(namespacedName)
	if ok {
		pmr := v.(*PodMetricsRefresher)
		pmr.stop()
	}
}

func selectorFromInferencePoolSelector(selector map[v1alpha2.LabelKey]v1alpha2.LabelValue) labels.Selector {
	return labels.SelectorFromSet(stripLabelKeyAliasFromLabelMap(selector))
}

func stripLabelKeyAliasFromLabelMap(labels map[v1alpha2.LabelKey]v1alpha2.LabelValue) map[string]string {
	outMap := make(map[string]string)
	for k, v := range labels {
		outMap[string(k)] = string(v)
	}
	return outMap
}

func RandomWeightedDraw(logger logr.Logger, model *v1alpha2.InferenceModel, seed int64) string {
	source := rand.NewSource(rand.Int63())
	if seed > 0 {
		source = rand.NewSource(seed)
	}
	r := rand.New(source)

	// all the weight values are nil, then we should return random model name
	if model.Spec.TargetModels[0].Weight == nil {
		index := r.Int31n(int32(len(model.Spec.TargetModels)))
		return model.Spec.TargetModels[index].Name
	}

	var weights int32
	for _, model := range model.Spec.TargetModels {
		weights += *model.Weight
	}
	logger.V(logutil.TRACE).Info("Weights for model computed", "model", model.Name, "weights", weights)
	randomVal := r.Int31n(weights)
	// TODO: optimize this without using loop
	for _, model := range model.Spec.TargetModels {
		if randomVal < *model.Weight {
			return model.Name
		}
		randomVal -= *model.Weight
	}
	return ""
}

func IsCritical(model *v1alpha2.InferenceModel) bool {
	if model.Spec.Criticality != nil && *model.Spec.Criticality == v1alpha2.Critical {
		return true
	}
	return false
}

// TODO: move out to share with pod_reconciler.go
func podIsReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			if condition.Status == corev1.ConditionTrue {
				return true
			}
			break
		}
	}
	return false
}
