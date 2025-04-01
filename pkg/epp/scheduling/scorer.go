package scheduling

import (
	"sort"

	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

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
		pod.score = score
	}

	ctx.logger.V(logutil.DEBUG).Info("Selecting top K", "pods", pods, "k", sf.k)
	topK := GetTopKPods(pods, sf.k)
	ctx.logger.V(logutil.DEBUG).Info("Selected top K", "pods", topK)

	return topK, nil
}

type Scorer interface {
	Name() string
	Score(ctx *Context, pod *PodMetrics) (float64, error)
}

type scorerChain []Scorer

func (sc scorerChain) Score(ctx *Context, pod *PodMetrics) (float64, error) {
	// Iterate through each scorer in the chain and accumulate the scores.
	logger := ctx.logger.WithValues("pod", pod.NamespacedName)
	score := float64(0)
	for _, scorer := range sc {
		oneScore, err := scorer.Score(ctx, pod)
		if err != nil {
			logger.Error(err, "Failed to calculate score for scorer", "scorer", scorer.Name())
			return 0, err
		}
		score += oneScore
		logger.V(logutil.DEBUG).Info("After scorer", "scorer", scorer.Name(), "score", oneScore, "total score", score)
	}
	return score, nil
}

type weightedScorer struct {
	simpleScorer
	weight float64
}

func (ws *weightedScorer) Name() string {
	return ws.simpleScorer.Name()
}

func (ws *weightedScorer) Score(ctx *Context, pod *PodMetrics) (float64, error) {
	// Call the base score function to get the score
	score, err := ws.simpleScorer.Score(ctx, pod)
	if err != nil {
		ctx.logger.Error(err, "Failed to calculate score for weighted scorer", "scorer", ws.Name())
		return 0, err
	}
	return ws.weight * score, nil
}

type simpleScorer struct {
	name string
	f    scoreFunc
}

func (ss *simpleScorer) Name() string {
	return ss.name
}

func (ss *simpleScorer) Score(ctx *Context, pod *PodMetrics) (float64, error) {
	score, err := ss.f(ctx, pod)
	if err != nil {
		ctx.logger.Error(err, "Failed to calculate score for simple scorer", "scorer", ss.Name())
		return 0, err
	}
	// // Log a warning if the score is out of expected bounds
	// // Clamp the score to be within [-1, 1]
	// if score > 1.0 {
	// 	ctx.logger.Info("Score out of bounds", "scorer", ss.Name(), "score", score)
	// 	score = 1.0
	// } else if score < -1.0 {
	// 	ctx.logger.Info("Score out of bounds", "scorer", ss.Name(), "score", score)
	// 	score = -1.0
	// }
	return score, nil
}

type scoreFunc func(ctx *Context, pod *PodMetrics) (float64, error)

func GetTopKPods(pods []*PodMetrics, K int) []*PodMetrics {
	threshold := 0.05 // If scores of two pos differ within the threshold, consider them equal.
	// Sort pods by Score in descending order
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].score > pods[j].score
	})

	if K < 1 {
		K = 1
	}
	if K > len(pods) {
		K = len(pods)
	}
	res := pods[:K]
	for i := K; i < len(pods) && (pods[K-1].score-pods[i].score < threshold); i++ {
		res = append(res, pods[i])
	}
	return pods[:K]
}

var queueScorer = &weightedScorer{
	simpleScorer: simpleScorer{
		name: "queue",
		f:    queueScoreFunc,
	},
	weight: configV2.QueueScoreWeight,
}

func queueScoreFunc(ctx *Context, pod *PodMetrics) (float64, error) {
	resolution := 10
	// if ctx.maxQueueSize-ctx.minQueueSize < resolution {
	// 	return 0.0, nil
	// }

	// cur := pod.WaitingQueueSize / resolution * resolution
	return -float64(pod.WaitingQueueSize) / float64(resolution), nil
}

var kvCacheScorer = &weightedScorer{
	weight: configV2.KVCacheScoreWeight,
	simpleScorer: simpleScorer{
		name: "kv cache",
		f:    kvCacheScoreFunc,
	},
}

func kvCacheScoreFunc(ctx *Context, pod *PodMetrics) (float64, error) {
	resolution := 3
	return 1 - float64(int(100*pod.KVCacheUsagePercent)/resolution*resolution)/100.0, nil
}
