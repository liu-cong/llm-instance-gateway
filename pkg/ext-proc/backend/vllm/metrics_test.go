package vllm

import (
	"fmt"
	"testing"

	"inference.networking.x-k8s.io/gateway-api-inference-extension/pkg/ext-proc/backend"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func TestPromToPodMetrics(t *testing.T) {
	testCases := []struct {
		name              string
		metricFamilies    map[string]*dto.MetricFamily
		expectedMetrics   *backend.Metrics
		expectedErr       error
		initialPodMetrics *backend.PodMetrics
	}{
		{
			name: "all metrics available",
			metricFamilies: map[string]*dto.MetricFamily{
				RunningQueueSizeMetricName: {
					Metric: []*dto.Metric{
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(10),
							},
							TimestampMs: proto.Int64(100),
						},
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(15),
							},
							TimestampMs: proto.Int64(200), // This is the latest
						},
					},
				},
				WaitingQueueSizeMetricName: {
					Metric: []*dto.Metric{
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(20),
							},
							TimestampMs: proto.Int64(100),
						},
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(25),
							},
							TimestampMs: proto.Int64(200), // This is the latest
						},
					},
				},
				KVCacheUsagePercentMetricName: {
					Metric: []*dto.Metric{
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(0.8),
							},
							TimestampMs: proto.Int64(100),
						},
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(0.9),
							},
							TimestampMs: proto.Int64(200), // This is the latest
						},
					},
				},
				LoraRequestInfoMetricName: {
					Metric: []*dto.Metric{
						{
							Label: []*dto.LabelPair{
								{
									Name:  proto.String(LoraRequestInfoRunningAdaptersMetricName),
									Value: proto.String("lora3,lora4"),
								},
								{
									Name:  proto.String(LoraRequestInfoMaxAdaptersMetricName),
									Value: proto.String("2"),
								},
							},
							Gauge: &dto.Gauge{
								Value: proto.Float64(100),
							},
						},
						{
							Label: []*dto.LabelPair{
								{
									Name:  proto.String(LoraRequestInfoRunningAdaptersMetricName),
									Value: proto.String("lora2"),
								},
								{
									Name:  proto.String(LoraRequestInfoMaxAdaptersMetricName),
									Value: proto.String("2"),
								},
							},
							Gauge: &dto.Gauge{
								Value: proto.Float64(90),
							},
						},
					},
				},
			},
			expectedMetrics: &backend.Metrics{
				RunningQueueSize:    15,
				WaitingQueueSize:    25,
				KVCacheUsagePercent: 0.9,
				ActiveModels: map[string]int{
					"lora3": 0,
					"lora4": 0,
				},
				MaxActiveModels: 2,
			},
			initialPodMetrics: &backend.PodMetrics{},
			expectedErr:       nil,
		},
		{
			name: "invalid max lora",
			metricFamilies: map[string]*dto.MetricFamily{
				RunningQueueSizeMetricName: {
					Metric: []*dto.Metric{
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(10),
							},
							TimestampMs: proto.Int64(100),
						},
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(15),
							},
							TimestampMs: proto.Int64(200), // This is the latest
						},
					},
				},
				WaitingQueueSizeMetricName: {
					Metric: []*dto.Metric{
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(20),
							},
							TimestampMs: proto.Int64(100),
						},
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(25),
							},
							TimestampMs: proto.Int64(200), // This is the latest
						},
					},
				},
				KVCacheUsagePercentMetricName: {
					Metric: []*dto.Metric{
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(0.8),
							},
							TimestampMs: proto.Int64(100),
						},
						{
							Gauge: &dto.Gauge{
								Value: proto.Float64(0.9),
							},
							TimestampMs: proto.Int64(200), // This is the latest
						},
					},
				},
				LoraRequestInfoMetricName: {
					Metric: []*dto.Metric{
						{
							Label: []*dto.LabelPair{
								{
									Name:  proto.String(LoraRequestInfoRunningAdaptersMetricName),
									Value: proto.String("lora3,lora4"),
								},
								{
									Name:  proto.String(LoraRequestInfoRunningAdaptersMetricName),
									Value: proto.String("2a"),
								},
							},
							Gauge: &dto.Gauge{
								Value: proto.Float64(100),
							},
						},
						{
							Label: []*dto.LabelPair{
								{
									Name:  proto.String(LoraRequestInfoRunningAdaptersMetricName),
									Value: proto.String("lora2"),
								},
								{
									Name:  proto.String(LoraRequestInfoMaxAdaptersMetricName),
									Value: proto.String("2"),
								},
							},
							Gauge: &dto.Gauge{
								Value: proto.Float64(90),
							},
						},
					},
				},
			},
			expectedMetrics: &backend.Metrics{
				RunningQueueSize:    15,
				WaitingQueueSize:    25,
				KVCacheUsagePercent: 0.9,
				ActiveModels: map[string]int{
					"lora3": 0,
					"lora4": 0,
				},
				MaxActiveModels: 0,
			},
			initialPodMetrics: &backend.PodMetrics{},
			expectedErr:       fmt.Errorf("strconv.Atoi: parsing '2a': invalid syntax"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			updated, err := promToPodMetrics(tc.metricFamilies, tc.initialPodMetrics)
			if tc.expectedErr != nil {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedMetrics, &updated.Metrics)
			}
		})
	}
}
