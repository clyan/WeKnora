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
	"github.com/stretchr/testify/require"
)

func moduleCandidate(filePath string, dependencies ...string) frontendIndexCandidate {
	return frontendIndexCandidate{ID: indexCandidateID(filePath), Path: filePath, Dependencies: dependencies}
}

func TestFrontendModulesMergeExclusiveChildrenButKeepSharedDependenciesAndEntries(t *testing.T) {
	candidates := []frontendIndexCandidate{
		moduleCandidate("odd/checkout.ts", "odd/local/params.ts", "odd/shared/client.ts", "odd/routed/detail.ts"),
		moduleCandidate("odd/local/params.ts"),
		moduleCandidate("odd/shared/client.ts"),
		moduleCandidate("odd/routed/detail.ts"),
		moduleCandidate("elsewhere/list.ts", "odd/shared/client.ts"),
	}
	candidates[0].Entry, candidates[3].Entry = true, true
	modules, err := frontendIndexModules(candidates, &frontendRepoPlan{})
	require.NoError(t, err)
	require.Len(t, modules, 4)
	require.Len(t, modules[0].Files, 2)
	require.Equal(t, "local/params.ts", modules[0].Files[1].Path)
	require.True(t, modules[0].Entry)
	require.Equal(t, 2, modules[0].ImportCount)
	require.Equal(t, "odd/shared", modules[1].Directory)
	require.True(t, modules[2].Entry, "an independently registered entry must remain available to split")
	data, err := json.Marshal(modules)
	require.NoError(t, err)
	require.NotContains(t, string(data), "candidateID")
	require.NotContains(t, string(data), "target_path")
	require.Equal(t, 1, strings.Count(string(data), `"directory":"odd"`))
}

func TestFrontendModulesAllowSplittingAndRequireEveryFile(t *testing.T) {
	candidates := []frontendIndexCandidate{moduleCandidate("custom/a.ts"), moduleCandidate("custom/b.ts"), moduleCandidate("custom/c.ts")}
	modules, err := frontendIndexModules(candidates, &frontendRepoPlan{})
	require.NoError(t, err)
	require.Len(t, modules, 1)
	state, summaries := frontendCatalogFixture(t, map[string]string{"custom/a.ts": "export const a=1", "custom/b.ts": "export const b=1", "custom/c.ts": "export const c=1"})
	byID := map[string]frontendIndexCandidate{}
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	var reply frontendIndexReply
	require.NoError(t, json.Unmarshal([]byte(`{"groups":[{"candidate_ids":["f0","f1"],"category":"frontend_api","domain":"api","title":"调用专题","scope":"核对调用"}],"unassigned":[{"id":"f2","reason":"没有可确认入口"}]}`), &reply))
	reply, err = expandFrontendModuleReply(reply, modules)
	require.NoError(t, err)
	progress := &frontendRepoPlan{Decisions: map[string]string{}}
	require.NoError(t, applyFrontendIndexGroups(state, "front", summaries, progress, candidates, byID, reply))
	require.Len(t, progress.Pages, 1)
	require.Len(t, progress.Pages[0].SourcePaths, 2)
	require.Equal(t, 3, frontendIndexAccountedCount(candidates, progress))
	// A mixed module can never be skipped just because some members were assigned.
	progress = &frontendRepoPlan{Decisions: map[string]string{}}
	reply.Unassigned = nil
	require.ErrorContains(t, applyFrontendIndexGroups(state, "front", summaries, progress, candidates, byID, reply), "尚未交代")
	require.Empty(t, progress.Pages)
	require.Empty(t, progress.Decisions)
	require.NoError(t, json.Unmarshal([]byte(moduleGroupReply([]string{"m999"}, "bad")), &reply))
	_, err = expandFrontendModuleReply(reply, modules)
	require.ErrorContains(t, err, "不在本批次")
}

func TestFrontendDenseIndexPackingDoesNotDegenerateIntoTinyFileBatches(t *testing.T) {
	files := map[string]string{}
	provider := &frontendIndexProvider{}
	var paths []string
	for i := 0; i < 347; i++ {
		filePath := fmt.Sprintf("src/unfamiliar/module-%02d/portfolio-rebalancing-operation-%03d.tsx", i/10, i)
		files[filePath] = "export const View = () => null;"
		paths = append(paths, filePath)
		for j := 0; j < 24; j++ {
			provider.hints = append(provider.hints, codebase.PlanningHint{Path: filePath, Kind: "Function", Name: fmt.Sprintf("operation%d", j), Line: 1})
		}
	}
	state, summaries := frontendCatalogFixture(t, files)
	state.cfg.TopicVersion, state.cfg.SourcePolicyVersion = 1, 1
	state.provider = provider
	progress := &frontendRepoPlan{Decisions: map[string]string{}, ExcludedPaths: map[string]string{}}
	candidates, err := collectFrontendIndexCandidates(context.Background(), state, "front", paths, progress)
	require.NoError(t, err)
	modules, err := frontendIndexModules(candidates, progress)
	require.NoError(t, err)
	model := &codeWikiOutlineChat{}
	byID := map[string]frontendIndexCandidate{}
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	for start := 0; start < len(modules); {
		end := frontendModuleBatchEnd(modules, start)
		batch := frontendModuleCandidates(modules[start:end], byID)
		prompt := frontendModulePrompt(state, "app/main", "Chinese", progress, modules[start:end], batch)
		require.True(t, frontendModulePromptFits(state.cfg, prompt))
		var ids []string
		for _, module := range modules[start:end] {
			ids = append(ids, module.ID)
		}
		model.responses = append(model.responses, moduleGroupReply(ids, "combined"))
		start = end
	}
	state.chat = model
	pages, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages, 1)
	require.Len(t, pages[0].SourcePaths, 347)
	require.LessOrEqual(t, len(model.prompts), 3)
	require.Zero(t, provider.reads)
	for _, prompt := range model.prompts {
		require.NotContains(t, prompt, "target_path")
		require.NotContains(t, prompt, "\"line\":")
	}
	t.Logf("347 dense files: %d modules, %d model requests", len(modules), len(model.prompts))
}

func TestFrontendModulePlanningMigratesIndexedCheckpointWithoutReplayingFiles(t *testing.T) {
	files := map[string]string{"a.ts": "export const a=1", "b.ts": "export const b=1", "c.ts": "export const c=1"}
	state, summaries := frontendCatalogFixture(t, files)
	state.cfg.TopicVersion, state.cfg.SourcePolicyVersion = 1, 1
	state.provider = &frontendIndexProvider{}
	paths, err := frontendInventory(context.Background(), state.scopes["front"].LocalPath)
	require.NoError(t, err)
	progress := &frontendRepoPlan{Strategy: frontendFileIndexStrategy, Decisions: map[string]string{}, ExcludedPaths: map[string]string{}}
	candidates, err := collectFrontendIndexCandidates(context.Background(), state, "front", paths, progress)
	require.NoError(t, err)
	blob, _ := json.Marshal(candidates)
	hash := sha256.Sum256(blob)
	progress.StructureHash = hex.EncodeToString(hash[:])
	hash = sha256.Sum256([]byte(strings.Join(paths, "\x00")))
	progress.InventoryHash = hex.EncodeToString(hash[:])
	progress.Decisions["a.ts"] = "candidate: 已规划"
	progress.StructureCursor, progress.Cursor = 1, 1
	var prior frontendIndexReply
	require.NoError(t, json.Unmarshal([]byte(indexGroupReply([]string{"a.ts"}, "existing")), &prior))
	byID := map[string]frontendIndexCandidate{}
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	require.NoError(t, applyFrontendIndexGroups(state, "front", summaries, progress, candidates[:1], byID, prior))
	state.cfg.FrontendPlanning = &codeWikiFrontendPlanning{Repositories: map[string]*frontendRepoPlan{"front": progress}}
	model := &codeWikiOutlineChat{responses: []string{moduleGroupReply([]string{"m0"}, "existing")}}
	state.chat = model
	pages, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, model.prompts, 1)
	require.NotContains(t, model.prompts[0], `"path":"a.ts"`)
	require.Contains(t, model.prompts[0], "existing")
	require.Equal(t, frontendIndexStrategy, progress.Strategy)
	require.Equal(t, 3, progress.StructureCursor)
	require.Len(t, pages[0].SourcePaths, 3)
	state.chat = nil
	progress.Strategy, progress.StructureHash = frontendFileIndexStrategy, ""
	_, err = (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorContains(t, err, "缺少候选指纹")
}

func TestFrontendModuleFingerprintIncludesRelationsBeyondPromptSample(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{
		"entry.ts": "export const entry=1", "a.ts": "export const a=1", "b.ts": "export const b=1", "c.ts": "export const c=1",
	})
	state.cfg.TopicVersion, state.cfg.SourcePolicyVersion = 1, 1
	provider := &frontendIndexProvider{}
	for i := 0; i < 13; i++ {
		target := "a.ts"
		if i == 12 {
			target = "b.ts"
		}
		provider.relations = append(provider.relations, codebase.PlanningHint{Path: "entry.ts", Line: 1, Kind: "CALLS", Name: "entry", TargetPath: target, Target: "target"})
	}
	state.provider = provider
	state.chat = &codeWikiOutlineChat{responses: []string{moduleGroupReply([]string{"m0"}, "all")}}
	_, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	// Same row count and same first 12 sampled relations, but the actual graph
	// changed: continuing under the old fingerprint must still be rejected.
	provider.relations[12].TargetPath = "c.ts"
	state.chat = nil
	_, err = (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorContains(t, err, "索引候选已变化")
}

func TestFrontendModuleContextProtectsSmallModelsBeforeCalling(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"a.ts": "export const a=1"})
	state.cfg.TopicVersion, state.cfg.SourcePolicyVersion, state.cfg.ContextWindow = 1, 1, 4096
	state.provider = &frontendIndexProvider{}
	// A nil model proves the preflight check prevents an oversized request.
	_, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorIs(t, err, errCodeWikiFrontendPlanningContext)
	require.Zero(t, state.run.ModelCalls)
	require.Empty(t, state.cfg.FrontendPlanning.Repositories["front"].Pages)
	require.Zero(t, state.cfg.FrontendPlanning.Repositories["front"].StructureCursor)
}

func TestFrontendModuleCorrectionUsesRequestFileIDs(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"a.ts": "export const a=1", "b.ts": "export const b=1"})
	state.cfg.TopicVersion, state.cfg.SourcePolicyVersion = 1, 1
	state.provider = &frontendIndexProvider{}
	model := &codeWikiOutlineChat{responses: []string{
		moduleGroupReply([]string{"f0"}, "all"),
		moduleGroupReply([]string{"m0"}, "all"),
	}}
	state.chat = model
	pages, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages[0].SourcePaths, 2)
	require.Len(t, model.prompts, 2)
	require.Contains(t, model.prompts[1], "候选尚未交代：f1")
	require.NotContains(t, model.prompts[1], indexCandidateID("b.ts"))
}
