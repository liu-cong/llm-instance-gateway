package datastore

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

var (
	pod1WithNewMetrics = &PodMetrics{
		Pod: pod1.Pod,
		Metrics: Metrics{
			WaitingQueueSize:    9999,
			KVCacheUsagePercent: 0.99,
			MaxActiveModels:     99,
			ActiveModels: map[string]int{
				"foo": 1,
				"bar": 1,
			},
		},
	}
)

func TestMetricsRefresher(t *testing.T) {
	ctx := context.Background()
	pmc := &FakePodMetricsClient{}
	// The refresher is initialized with empty metrics.
	initialMetrics := &PodMetrics{Pod: pod1.Pod}
	pmr := NewPodMetricsRefresher(ctx, pmc, initialMetrics, 8000, time.Millisecond)
	pmr.start()

	// Use SetRes to simulate an update of metrics from the pod.
	// Verify that the metrics are updated.
	pmc.SetRes(map[types.NamespacedName]*PodMetrics{
		pod1WithMetrics.NamespacedName: pod1WithMetrics,
	})
	condition := func(collect *assert.CollectT) {
		assert.True(collect, cmp.Equal(pmr.GetPodMetrics(), pod1WithMetrics, cmpopts.IgnoreFields(Metrics{}, "UpdateTime")))
	}
	assert.EventuallyWithT(t, condition, time.Second, time.Millisecond)

	// Stop the refresher, and simulate metric update again, this time the refresher won't get the
	// new update.
	pmr.stop()
	pmc.SetRes(map[types.NamespacedName]*PodMetrics{
		pod1WithMetrics.NamespacedName: pod1WithNewMetrics,
	})
	// Still expect the same condition (no metrics update).
	assert.EventuallyWithT(t, condition, time.Second, time.Millisecond)
}
