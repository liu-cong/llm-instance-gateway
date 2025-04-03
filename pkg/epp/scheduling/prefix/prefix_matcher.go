package prefix

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"
	"k8s.io/apimachinery/pkg/types"
	backendmetrics "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/backend/metrics"
	schedulingtypes "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

type prefixCacheMatcher struct {
	// cacheblockSize is the size of each block in the cache. Requests with length shorter than the
	// block size will be ignored.
	cacheblockSize int
	// goroutine interval to scan the cache and evict entries.
	cacheEvictionInterval time.Duration
	// cacheExpiration is the time after which the cache entry is considered expired.
	cacheExpiration time.Duration
	// If the cache size exceeds this limit, the cache will stop recording new entries.
	// Current (approximate) size of the cache.
	// This is asynchronously updated by the goroutine that evicts entries.
	size         atomic.Int32
	cacheSize    atomic.Int32
	maxCacheSize int32
	table        *prefixCacheLookupTable
}

func NewPrefixCacheMatcher(cacheBlockSize int, maxCacheSize int32, cacheEvictionInterval time.Duration, cacheExpiration time.Duration) *prefixCacheMatcher {
	return &prefixCacheMatcher{
		cacheblockSize:        cacheBlockSize,
		cacheEvictionInterval: cacheEvictionInterval,
		cacheExpiration:       cacheExpiration,
		maxCacheSize:          maxCacheSize,
		table:                 newPrefixCacheLookupTable(),
		cacheSize:             atomic.Int32{},
	}
}

func (m *prefixCacheMatcher) Name() string {
	return "prefixCache"
}

func (m *prefixCacheMatcher) OnReceive(ctx *schedulingtypes.Context) {
	ctx.Hashes = toBlockHashes(ctx, m.cacheblockSize)
	ctx.Logger.V(logutil.DEBUG).Info("Calculated block hashes", "hashes", ctx.Hashes)
}

// If a request was routed to a pod, record it in the cache:
// Append the pod to the block hashes of the request. If the block was already cached in this pod, update it's last updated time.
func (m *prefixCacheMatcher) OnDispatch(ctx *schedulingtypes.Context, target *schedulingtypes.PodMetrics) {
	if m.cacheSize.Load() > m.maxCacheSize {
		ctx.Logger.Info("WARNING: Cache size exceeded, not recording new entries", "size", m.cacheSize.Load(), "maxSize", m.maxCacheSize)
		return
	}
	for _, hash := range ctx.Hashes {
		m.table.add(hash, ServerID(target.GetPod().NamespacedName))
	}
}

// Given a request, and a list of candidate pods, look up the prefix cache table to find pods with the longest prefix match.
func (m *prefixCacheMatcher) Filter(ctx *schedulingtypes.Context, pods []*schedulingtypes.PodMetrics) ([]*schedulingtypes.PodMetrics, error) {
	ctx.Logger.V(logutil.DEBUG).Info("Finding longest prefix match", "current cache", m.table)
	for i := len(ctx.Hashes) - 1; i >= 0; i-- {
		hash := ctx.Hashes[i]
		cachedServers := m.table.get(hash)
		if len(cachedServers) > 0 {
			ctx.Logger.V(logutil.VERBOSE).Info("Found cached servers", "cachedServers", cachedServers, "# blocks", len(ctx.Hashes), "longest prefix", i)
			res := []*schedulingtypes.PodMetrics{}
			for _, pod := range pods {
				if _, ok := cachedServers[ServerID(pod.GetPod().NamespacedName)]; ok {
					res = append(res, pod)
				}
			}
			return res, nil
		}
	}
	ctx.Logger.V(logutil.VERBOSE).Info("No cached servers found")
	return pods, nil
}

// TODO implement this
func (m *prefixCacheMatcher) Evict(ctx *schedulingtypes.Context) ([]backendmetrics.PodMetrics, error) {
	// Remove cache entries if last updated time is older than a threshold.
	return nil, nil
}

func toBlockHashes(ctx *schedulingtypes.Context, cacheBlockSize int) []schedulingtypes.BlockHash {
	prompt := []byte(ctx.Req.Prompt)
	if len(prompt) < cacheBlockSize {
		ctx.Logger.V(logutil.DEBUG).Info("Request body too small for prefix cache", "size", len(prompt))
		return nil
	}
	// Split the body into blocks of size cacheBlockSize. The +1 is to account for the model.
	// If the last block is smaller than cacheBlockSize, it will be ignored.
	res := make([]schedulingtypes.BlockHash, 0, 1+len(prompt)/cacheBlockSize)
	// Add the model to the first block hash so that different models have different hashes even with the same body.
	res = append(res, schedulingtypes.BlockHash(xxhash.Sum64String(ctx.Req.ResolvedTargetModel)))
	for i := 0; i+cacheBlockSize < len(prompt); i += cacheBlockSize {
		block := prompt[i : i+cacheBlockSize]
		prevBlockHash := res[len(res)-1]
		toHash := append(block, toBytes(prevBlockHash)...)
		res = append(res, schedulingtypes.BlockHash(xxhash.Sum64(toHash)))
	}
	return res
}

func toBytes(i schedulingtypes.BlockHash) []byte {
	bytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(bytes, uint64(i))
	return bytes
}

func newPrefixCacheLookupTable() *prefixCacheLookupTable {
	return &prefixCacheLookupTable{
		table: &sync.Map{},
	}
}

type prefixCacheLookupTable struct {
	// key: BlockHash; value: sync.Map, which is a map of server ID to the last time the
	// corresponding block was sent to this server.
	table *sync.Map // map[BlockHash]CachedServers
}

type ServerID types.NamespacedName

// key: NamespacedName of the pod;
// value: The last time the corresponding block was sent to this server.
type CachedServers map[ServerID]bool

func (cs CachedServers) String() string {
	list := []string{}
	for k := range cs {
		list = append(list, k.Namespace+"/"+k.Name)
	}
	return fmt.Sprintf("%v", list)
}

func (t *prefixCacheLookupTable) String() string {
	list := []string{}
	t.table.Range(func(key, value interface{}) bool {
		cachedServers := value.(*sync.Map)
		serverMap := make(map[ServerID]time.Time)
		cachedServers.Range(func(key, value interface{}) bool {
			serverMap[key.(ServerID)] = value.(time.Time)
			return true
		})
		list = append(list, fmt.Sprintf("%s: %v", key, serverMap))
		return true
	})
	return fmt.Sprintf("Entries: %v", list)
}

func (t *prefixCacheLookupTable) add(hash schedulingtypes.BlockHash, server ServerID) {
	loaded, _ := t.table.LoadOrStore(hash, &sync.Map{})
	cachedServers := loaded.(*sync.Map)
	cachedServers.Store(server, time.Now()) // Update the last updated time.
}

func (t *prefixCacheLookupTable) get(hash schedulingtypes.BlockHash) CachedServers {
	loaded, _ := t.table.LoadOrStore(hash, &sync.Map{})
	cachedServers := loaded.(*sync.Map)
	res := CachedServers{}
	cachedServers.Range(func(key, value interface{}) bool {
		res[key.(ServerID)] = true
		return true
	})
	return res
}

func (t *prefixCacheLookupTable) delete(hash schedulingtypes.BlockHash, server ServerID) {
	loaded, _ := t.table.LoadOrStore(hash, &sync.Map{})
	cachedServers := loaded.(*sync.Map)
	cachedServers.Delete(server)
}

func (t *prefixCacheLookupTable) ranges(hash schedulingtypes.BlockHash, server ServerID) {
	loaded, _ := t.table.LoadOrStore(hash, &sync.Map{})
	cachedServers := loaded.(*sync.Map)
	cachedServers.Delete(server)
}
