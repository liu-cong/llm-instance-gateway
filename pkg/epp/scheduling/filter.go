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
	"errors"
	"math"
	"math/rand"
	"time"

	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

type Filter interface {
	Name() string
	Filter(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error)
}

type scoreFilter struct {
	name string
	// topK pods to return after filtering.
	k  int
	sc scorerChain
}

func (sf *scoreFilter) Name() string {
	return sf.name
}

func (sf *scoreFilter) Filter(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error) {

	// Iterate through each pod and calculate the scores using the scorer chain.
	for _, pod := range pods {
		score, err := sf.sc.Score(ctx, pod)
		if err != nil {
			ctx.logger.Error(err, "Failed to calculate score for pod")
			return nil, err
		}
		pod.Score = score
	}

	ctx.logger.V(logutil.DEBUG).Info("Selecting top K", "pods", pods, "k", sf.k)
	topK := GetTopKPods(pods, sf.k)
	ctx.logger.V(logutil.DEBUG).Info("Selected top K", "pods", topK)

	return topK, nil
}

// filter applies current filterFunc, and then recursively applies next filters depending success or
// failure of the current filterFunc.
// It can be used to construct a flow chart algorithm.
type filter struct {
	simpleFilter
	// nextOnSuccess filter will be applied after successfully applying the current filter.
	// The filtered results will be passed to the next filter.
	nextOnSuccess Filter
	// nextOnFailure filter will be applied if current filter fails.
	// The original input will be passed to the next filter.
	nextOnFailure Filter
	// nextOnSuccessOrFailure is a convenience field to configure the next filter regardless of the
	// success or failure of the current filter.
	// NOTE: When using nextOnSuccessOrFailure, both nextOnSuccess and nextOnFailure SHOULD be nil.
	// However if that's not the case, nextOnSuccess and nextOnFailure will be used, instead of
	// nextOnSuccessOrFailure,  in the success and failure scenarios, respectively.
	nextOnSuccessOrFailure Filter
}

type simpleFilter struct {
	name   string
	filter filterFunc
}

func (sf *simpleFilter) Name() string {
	if sf == nil {
		return "nil"
	}
	return sf.name
}

func (sf *simpleFilter) Filter(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error) {
	loggerTrace := ctx.logger.V(logutil.TRACE)
	loggerTrace.Info("Running a filter", "name", sf.Name(), "podCount", len(pods))

	return sf.filter(ctx, pods)
}

func (f *filter) Filter(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error) {
	loggerTrace := ctx.logger.V(logutil.TRACE)
	filtered, err := f.simpleFilter.filter(ctx, pods)

	next := f.nextOnSuccessOrFailure
	if err == nil && len(filtered) > 0 {
		if f.nextOnSuccess == nil && f.nextOnSuccessOrFailure == nil {
			// No succeeding filters to run, return.
			return filtered, err
		}
		if f.nextOnSuccess != nil {
			next = f.nextOnSuccess
		}
		loggerTrace.Info("Filter succeeded", "filter", f.Name(), "next", next.Name(), "filteredPodCount", len(filtered))
		// On success, pass the filtered result to the next filter.
		return next.Filter(ctx, filtered)
	} else {
		if f.nextOnFailure == nil && f.nextOnSuccessOrFailure == nil {
			// No succeeding filters to run, return.
			return filtered, err
		}
		if f.nextOnFailure != nil {
			next = f.nextOnFailure
		}
		loggerTrace.Info("Filter failed", "filter", f.Name(), "next", next.Name())
		// On failure, pass the initial set of pods to the next filter.
		return next.Filter(ctx, pods)
	}
}

// filterFunc filters a set of input pods to a subset.
type filterFunc func(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error)

// toFilterFunc is a helper function to convert a per pod filter func to the FilterFunc.
func toFilterFunc(pp podPredicate) filterFunc {
	return func(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error) {
		filtered := []*PodMetrics{}
		for _, pod := range pods {
			pass := pp(ctx.req, pod)
			if pass {
				filtered = append(filtered, pod)
			}
		}
		if len(filtered) == 0 {
			return nil, errors.New("no pods left")
		}
		return filtered, nil
	}
}

var leastQueueFilter = simpleFilter{
	name:   "least queuing",
	filter: leastQueuingFilterFunc,
}

// leastQueuingFilterFunc finds the max and min queue size of all pods, divides the whole range
// (max-min) by the number of pods, and finds the pods that fall into the first range.
// The intuition is that if there are multiple pods that share similar queue size in the low range,
// we should consider them all instead of the absolute minimum one. This worked better than picking
// the least one as it gives more choices for the next filter, which on aggregate gave better
// results.
// TODO: Compare this strategy with other strategies such as top K.
func leastQueuingFilterFunc(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error) {
	min := math.MaxInt
	max := 0
	filtered := []*PodMetrics{}

	for _, pod := range pods {
		if pod.WaitingQueueSize <= min {
			min = pod.WaitingQueueSize
		}
		if pod.WaitingQueueSize >= max {
			max = pod.WaitingQueueSize
		}
	}

	for _, pod := range pods {
		if pod.WaitingQueueSize >= min && pod.WaitingQueueSize <= min+(max-min)/len(pods) {
			filtered = append(filtered, pod)
		}
	}
	return filtered, nil
}

var lowQueueFilter = simpleFilter{
	name:   "low queueing filter",
	filter: toFilterFunc((lowQueueingPodPredicate)),
}

func lowQueueingPodPredicate(_ *LLMRequest, pod *PodMetrics) bool {
	return pod.WaitingQueueSize < config.QueueingThresholdLoRA
}

var leastKVCacheFilter = simpleFilter{
	name:   "least KV cache percent",
	filter: leastKVCacheFilterFunc,
}

// leastKVCacheFilterFunc finds the max and min KV cache of all pods, divides the whole range
// (max-min) by the number of pods, and finds the pods that fall into the first range.
// The intuition is that if there are multiple pods that share similar KV cache in the low range, we
// should consider them all instead of the absolute minimum one. This worked better than picking the
// least one as it gives more choices for the next filter, which on aggregate gave better results.
// TODO: Compare this strategy with other strategies such as top K.
func leastKVCacheFilterFunc(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error) {
	min := math.MaxFloat64
	var max float64 = 0
	filtered := []*PodMetrics{}

	for _, pod := range pods {
		if pod.KVCacheUsagePercent <= min {
			min = pod.KVCacheUsagePercent
		}
		if pod.KVCacheUsagePercent >= max {
			max = pod.KVCacheUsagePercent
		}
	}

	for _, pod := range pods {
		if pod.KVCacheUsagePercent >= min && pod.KVCacheUsagePercent <= min+(max-min)/float64(len(pods)) {
			filtered = append(filtered, pod)
		}
	}
	return filtered, nil
}

// podPredicate is a filter function to check whether a pod is desired.
type podPredicate func(req *LLMRequest, pod *PodMetrics) bool

// We consider serving an adapter low cost it the adapter is active in the model server, or the
// model server has room to load the adapter. The lowLoRACostPredicate ensures weak affinity by
// spreading the load of a LoRA adapter across multiple pods, avoiding "pinning" all requests to
// a single pod. This gave good performance in our initial benchmarking results in the scenario
// where # of lora slots > # of lora adapters.
func lowLoRACostPredicate(req *LLMRequest, pod *PodMetrics) bool {
	_, ok := pod.ActiveModels[req.ResolvedTargetModel]
	return ok || len(pod.ActiveModels) < pod.MaxActiveModels
}

var loRAAffinityFilter = simpleFilter{
	name:   "affinity LoRA",
	filter: loRASoftAffinityFilterFunc,
}

// loRASoftAffinityPredicate implements a pod selection strategy that prioritizes pods
// with existing LoRA model affinity while allowing for load balancing through randomization.
//
// The function works by:
// 1. Separating pods into two groups: those with target model affinity and those with available capacity
// 2. Using a probability threshold to sometimes select from non-affinity pods to enable load balancing
// 3. Falling back to whatever group has pods if one group is empty
//
// Parameters:
//   - logger: Logger interface for diagnostic output
//   - req: LLM request containing the resolved target model
//   - pods: Slice of pod metrics to filter
//
// Returns:
//   - Filtered slice of pod metrics based on affinity and availability
//   - Error if any issues occur during filtering
func loRASoftAffinityFilterFunc(ctx *Context, pods []*PodMetrics) ([]*PodMetrics, error) {

	// Pre-allocate slices with estimated capacity
	filtered_affinity := make([]*PodMetrics, 0, len(pods))
	filtered_available := make([]*PodMetrics, 0, len(pods))

	// Categorize pods based on affinity and availability
	for _, pod := range pods {

		if _, exists := pod.ActiveModels[ctx.req.ResolvedTargetModel]; exists {
			filtered_affinity = append(filtered_affinity, pod)
		} else if len(pod.ActiveModels) < pod.MaxActiveModels {
			filtered_available = append(filtered_available, pod)
		}
	}

	// Use crypto/rand for better randomization in production environments
	randSource := rand.NewSource(time.Now().UnixNano())
	randGen := rand.New(randSource)

	// If both groups have pods, use probability to select which group to return
	if len(filtered_affinity) > 0 && len(filtered_available) > 0 {
		if randGen.Float64() < config.LoraAffinityThreshold {
			return filtered_affinity, nil
		}
		return filtered_available, nil
	}

	// Return whichever group has pods
	if len(filtered_affinity) > 0 {
		return filtered_affinity, nil
	}

	return filtered_available, nil
}

func noQueueAndLessThanKVCacheThresholdPredicate(queueThreshold int, kvCacheThreshold float64) podPredicate {
	return func(req *LLMRequest, pod *PodMetrics) bool {
		return pod.WaitingQueueSize <= queueThreshold && pod.KVCacheUsagePercent <= kvCacheThreshold
	}
}
