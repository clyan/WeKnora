package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type frontendIndexProvider struct {
	codebase.CodeAnalysisProvider
	hints     []codebase.PlanningHint
	relations []codebase.PlanningHint
	queries   int
	reads     int
}

func (p *frontendIndexProvider) PlanningStructure(_ context.Context, _ codebase.SnapshotScope, stream string, opts codebase.QueryOptions) ([]codebase.PlanningHint, error) {
	p.queries++
	hints := p.hints
	if stream == "relations" {
		hints = p.relations
	}
	if opts.Offset >= len(hints) {
		return nil, nil
	}
	end := opts.Offset + opts.Limit
	if end > len(hints) {
		end = len(hints)
	}
	return hints[opts.Offset:end], nil
}

func (p *frontendIndexProvider) ReadSource(context.Context, codebase.SnapshotScope, string, int, int) (string, error) {
	p.reads++
	return "", fmt.Errorf("planning must not request source")
}

func indexCandidateID(path string) string {
	hash := sha256.Sum256([]byte(path))
	return hex.EncodeToString(hash[:12])
}

func indexGroupReply(paths []string, domain string) string {
	var keys []string
	for _, path := range paths {
		keys = append(keys, indexCandidateID(path))
	}
	data, _ := json.Marshal(map[string]any{"groups": []map[string]any{{"candidate_ids": keys, "category": "frontend_component", "domain": domain, "title": "组合交互组件", "scope": "从实际使用位置核对输入、事件和状态"}}})
	return string(data)
}

func moduleGroupReply(ids []string, domain string) string {
	data, _ := json.Marshal(map[string]any{"groups": []map[string]any{{"candidate_ids": ids, "category": "frontend_component", "domain": domain, "title": "组合交互组件", "scope": "从实际使用位置核对输入、事件和状态"}}})
	return string(data)
}

func TestFrontendIndexedPlanningBatchesWithoutPerFileModelReads(t *testing.T) {
	files := map[string]string{"README.md": "obsolete behavior", "mock/fake.ts": "obsolete mock"}
	var paths []string
	for i := 0; i < 40; i++ {
		path := fmt.Sprintf("unusual/%02d.ts", i)
		paths = append(paths, path)
		files[path] = "export const entry = () => 1;\n"
	}
	state, summaries := frontendCatalogFixture(t, files)
	state.cfg.TopicVersion = 1
	state.cfg.SourcePolicyVersion = 1
	provider := &frontendIndexProvider{}
	for _, path := range paths {
		provider.hints = append(provider.hints, codebase.PlanningHint{Path: path, Name: "entry", Kind: "Function", Line: 1})
	}
	state.provider = provider
	model := &codeWikiOutlineChat{responses: []string{moduleGroupReply([]string{"m0", "m1"}, "interactions")}}
	state.chat = model
	pages, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages, 1, "same domain across batches extends the existing topic")
	require.Len(t, pages[0].SourcePaths, 40)
	require.Len(t, model.prompts, 1, "40 files fit in one compact module request")
	require.Zero(t, provider.reads)
	require.Empty(t, state.cfg.FrontendPlanning.Repositories["front"].ReadSpans, "index locations are not read evidence")
	require.Empty(t, pages[0].SourceSpans)
	require.NotEmpty(t, pages[0].IndexHints, "topic workers retain index locations as navigation, never evidence")
	require.NotContains(t, strings.Join(model.prompts, "\n"), "obsolete behavior")
	require.NotContains(t, strings.Join(model.prompts, "\n"), "mock/fake.ts")
	require.Equal(t, 40, state.cfg.FrontendPlanning.Repositories["front"].StructureCursor)
}

func TestFrontendIndexedPlanningPagesAllIndexRowsAndResumes(t *testing.T) {
	files := map[string]string{}
	var paths []string
	for i := 0; i < 300; i++ {
		path := fmt.Sprintf("custom/%03d.ts", i)
		paths = append(paths, path)
		files[path] = "export const node=1;\n"
	}
	state, summaries := frontendCatalogFixture(t, files)
	state.cfg.TopicVersion = 1
	state.cfg.SourcePolicyVersion = 1
	state.cfg.MaxModelCalls = 1
	provider := &frontendIndexProvider{}
	// An exact full page must be followed by another query, even when many
	// symbols come from the same file. Detailed symbols remain in the index.
	for i := 0; i < 256; i++ {
		provider.hints = append(provider.hints, codebase.PlanningHint{Path: paths[0], Name: "node", Kind: "Variable", Line: 1})
	}
	state.provider = provider
	// Index pagination must not lose the remaining files when one file has
	// more declarations than the bounded prompt sample can contain.
	progress := &frontendRepoPlan{Decisions: map[string]string{}, ExcludedPaths: map[string]string{}}
	candidates, err := collectFrontendIndexCandidates(context.Background(), state, "front", paths, progress)
	require.NoError(t, err)
	require.Equal(t, 256, candidates[0].SymbolCount)
	require.Len(t, candidates[0].Symbols, 12)
	require.GreaterOrEqual(t, provider.queries, 3)
	require.NotEmpty(t, candidates[39].Boundary, "unindexed files remain discoverable")
	modules, err := frontendIndexModules(candidates, progress)
	require.NoError(t, err)
	end := frontendModuleBatchEnd(modules, 0)
	var firstIDs, nextIDs []string
	firstCount := 0
	for i, module := range modules {
		if i < end {
			firstIDs = append(firstIDs, module.ID)
			firstCount += len(module.Files)
		} else {
			nextIDs = append(nextIDs, module.ID)
		}
	}
	require.Greater(t, firstCount, 100)
	require.Less(t, firstCount, len(paths))
	model := &codeWikiOutlineChat{responses: []string{moduleGroupReply(firstIDs, "nodes")}}
	state.chat = model
	var saved []byte
	state.checkpoint = func(context.Context) error { saved, _ = json.Marshal(state.cfg); return nil }
	_, err = (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorIs(t, err, errCodeWikiRunPaused)
	require.NotEmpty(t, saved)
	require.NoError(t, json.Unmarshal(saved, &state.cfg))
	require.Equal(t, firstCount, state.cfg.FrontendPlanning.Repositories["front"].StructureCursor)
	state.modelCallsAtStart = state.run.ModelCalls
	// Module IDs are local to the next request; already classified paths are not replayed.
	for i := range nextIDs {
		nextIDs[i] = fmt.Sprintf("m%d", i)
	}
	resumed := &codeWikiOutlineChat{responses: []string{moduleGroupReply(nextIDs, "nodes")}}
	state.chat = resumed
	pages, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages, 1)
	require.Len(t, pages[0].SourcePaths, 300)
	require.Contains(t, resumed.prompts[0], "nodes", "stable topic identity is supplied for merging")
	require.NotContains(t, resumed.prompts[0], `"path":"000.ts"`)
}

func TestFrontendIndexGroupingRejectsMissingCandidatesAtomically(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"alpha.ts": "export const a=1", "beta.ts": "export const b=1"})
	var batch []frontendIndexCandidate
	byID := map[string]frontendIndexCandidate{}
	for _, path := range []string{"alpha.ts", "beta.ts"} {
		candidate := frontendIndexCandidate{ID: indexCandidateID(path), Path: path}
		batch = append(batch, candidate)
		byID[candidate.ID] = candidate
	}
	progress := &frontendRepoPlan{Decisions: map[string]string{}}
	var reply frontendIndexReply
	require.NoError(t, json.Unmarshal([]byte(indexGroupReply([]string{"alpha.ts"}, "a")), &reply))
	err := applyFrontendIndexGroups(state, "front", summaries, progress, batch, byID, reply)
	require.ErrorContains(t, err, "尚未交代")
	require.Empty(t, progress.Pages)
	require.Empty(t, progress.Decisions)
}

func TestFrontendIndexedPlanningRejectsOldCheckpointAndUnsupportedProvider(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"entry.ts": "export const entry=1"})
	state.cfg.TopicVersion = 1
	state.cfg.SourcePolicyVersion = 1
	_, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorContains(t, err, "不支持索引专题发现")
	state.cfg.FrontendPlanning.Repositories["front"].Strategy = ""
	_, err = (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorContains(t, err, "旧的逐文件目录规划")
}

func TestFrontendDocumentTemplatesReachTopicModel(t *testing.T) {
	for _, tc := range []struct{ category, section, question string }{
		{"frontend_api", "真实入参（各调用点反推）", "对象展开覆盖顺序"},
		{"frontend_workflow", "分支、失败与取消", "异步或并行步骤"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			s, _, run, _ := topicServiceFixture(t)
			s.modelService = topicFlowModelService{}
			state, _ := frontendCatalogFixture(t, nil)
			state.run = run
			state.interruptCheck = func() error { return nil }
			model := &codeWikiOutlineChat{responses: []string{`{"action":"items","offset":0}`}}
			state.chat = model
			repo := s.repo.(interfaces.CodeWikiTopicRepository)
			stored, err := repo.GetTopic(context.Background(), 7, "kb", run.ID, "0")
			require.NoError(t, err)
			worker, err := repo.ClaimTopic(context.Background(), 7, "kb", run.ID, "0", run.ExecutionEpoch, stored.Revision, "template-test")
			require.NoError(t, err)
			require.NotNil(t, worker)
			page := codeWikiPlannedPage{Category: tc.category, Title: "专题", Slug: "0"}
			cp := newTopicCheckpoint(page)
			_, err = s.runTopicRound(context.Background(), state, worker, page, &cp)
			require.NoError(t, err)
			require.Len(t, model.prompts, 1)
			require.Contains(t, model.prompts[0], tc.section)
			require.Contains(t, model.prompts[0], tc.question)
			require.NotContains(t, model.prompts[0], "有哪些按钮、菜单、行操作、表单和自动触发？继续追踪子组件。")
			require.Empty(t, cp.Evidence)
		})
	}
	for _, category := range types.CodeWikiFrontendCategories() {
		template, err := frontendDocumentTemplates.ReadFile("code_wiki_templates/" + category.Category + ".md")
		require.NoError(t, err)
		require.NotEmpty(t, template)
	}
}
