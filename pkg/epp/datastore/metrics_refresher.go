package datastore

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

const (
	fetchMetricsTimeout = 5 * time.Second
)

type PodMetricsRefresher struct {
	once      sync.Once
	parentCtx context.Context
	done      chan struct{}
	interval  time.Duration
	port      int32
	pm        unsafe.Pointer // stores a pointer *PodMetrics
	pmc       PodMetricsClient
	logger    logr.Logger
}

type PodMetricsClient interface {
	FetchMetrics(ctx context.Context, existing *PodMetrics, port int32) (*PodMetrics, error)
}

func NewPodMetricsRefresher(parentCtx context.Context, pmc PodMetricsClient, pod *PodMetrics, port int32, interval time.Duration) *PodMetricsRefresher {
	return &PodMetricsRefresher{
		once:      sync.Once{},
		parentCtx: parentCtx,
		done:      make(chan struct{}),
		port:      port,
		interval:  interval,
		pm:        unsafe.Pointer(pod),
		pmc:       pmc,
		logger:    log.FromContext(parentCtx),
	}
}

func (r *PodMetricsRefresher) UpdatePod(pod Pod) {
	updated := r.GetPodMetrics().Clone()
	updated.Pod = pod
	atomic.StorePointer(&r.pm, unsafe.Pointer(updated))
}

func (r *PodMetricsRefresher) GetPodMetrics() *PodMetrics {
	return (*PodMetrics)(atomic.LoadPointer(&r.pm))
}

// start starts a goroutine exactly once to periodically update metrics. The goroutine will be
// stopped either when stop() is called, or the parentCtx is cancelled.
func (r *PodMetricsRefresher) start() {
	r.once.Do(func() {
		pm := r.GetPodMetrics()
		go func() {
			r.logger.V(logutil.DEFAULT).Info("Starting refresher", "pod", pm.Pod)
			for {
				select {
				case <-r.done:
					return
				case <-r.parentCtx.Done():
					return
				default:
				}

				err := r.refreshMetrics()
				if err != nil {
					r.logger.Error(err, "Failed to refresh metrics", "pod", pm.Pod)
				}

				time.Sleep(r.interval)
			}
		}()
	})
}

func (r *PodMetricsRefresher) refreshMetrics() error {
	ctx, cancel := context.WithTimeout(context.Background(), fetchMetricsTimeout)
	defer cancel()
	existing := r.GetPodMetrics()
	updated, err := r.pmc.FetchMetrics(ctx, existing, r.port)
	if err != nil {
		// As refresher is running in the background, it's possible that the pod is deleted but
		// the refresh goroutine doesn't read the done channel yet. In this case, we just return nil.
		// The refresher will be stopped after this interval.
		return nil
	}
	updated.UpdateTime = time.Now()

	r.logger.V(logutil.TRACE).Info("Refreshed metrics", "updated", updated)

	atomic.StorePointer(&r.pm, unsafe.Pointer(updated))
	return nil
}

func (r *PodMetricsRefresher) stop() {
	pm := r.GetPodMetrics()
	r.logger.V(logutil.DEFAULT).Info("Stopping refresher", "pod", pm.Pod)
	close(r.done)
}
