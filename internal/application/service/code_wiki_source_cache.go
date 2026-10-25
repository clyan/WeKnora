package service

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/codefacts"
	"golang.org/x/sync/singleflight"
)

// Only immutable parser output is shared. Authorization and snapshot validation
// happen before lookup; no topic conclusions or model conversations are cached.
var codeWikiSourceCache = struct {
	sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	bytes   int
	flight  singleflight.Group
}{entries: make(map[string]*list.Element), order: list.New()}

type codeWikiCachedSource struct {
	key    string
	result *codefacts.Result
	bytes  int
}

func analyzeCachedCodeWikiSource(ctx context.Context, state *codeWikiRunState, scope codebase.SnapshotScope, path, raw string) (*codefacts.Result, error) {
	tenant := uint64(0)
	if state.run != nil {
		tenant = state.run.TenantID
	}
	hash := sha256.Sum256([]byte(raw))
	key := fmt.Sprintf("%d/%s/%s/%s/%s/%s/%s/%d/%d/%s/%x", tenant, scope.KnowledgeBaseID, scope.RepositoryID,
		scope.SnapshotID, scope.CommitSHA, scope.FilterHash, path, state.cfg.SourcePolicyVersion,
		state.cfg.FactSchemaVersion, state.cfg.SourceAnalyzerVersion, hash)
	value, err, _ := codeWikiSourceCache.flight.Do(key, func() (any, error) {
		codeWikiSourceCache.Lock()
		if entry := codeWikiSourceCache.entries[key]; entry != nil {
			codeWikiSourceCache.order.MoveToFront(entry)
			result := entry.Value.(codeWikiCachedSource).result
			codeWikiSourceCache.Unlock()
			return result, nil
		}
		codeWikiSourceCache.Unlock()
		result, err := codefacts.Analyze(ctx, path, raw)
		if err != nil {
			return nil, err
		}
		// Bound the shared cache even for source containing many syntax nodes.
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		size := len(raw) + len(encoded) + len(result.Nodes)*1024
		if size > 8<<20 {
			return result, nil
		}
		codeWikiSourceCache.Lock()
		defer codeWikiSourceCache.Unlock()
		for codeWikiSourceCache.bytes+size > 32<<20 && codeWikiSourceCache.order.Len() > 0 {
			entry := codeWikiSourceCache.order.Back()
			cached := entry.Value.(codeWikiCachedSource)
			delete(codeWikiSourceCache.entries, cached.key)
			codeWikiSourceCache.bytes -= cached.bytes
			codeWikiSourceCache.order.Remove(entry)
		}
		codeWikiSourceCache.entries[key] = codeWikiSourceCache.order.PushFront(codeWikiCachedSource{key, result, size})
		codeWikiSourceCache.bytes += size
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	// Callers may specialize source roles; never mutate shared cache entries.
	copyResult := *value.(*codefacts.Result)
	return &copyResult, nil
}
