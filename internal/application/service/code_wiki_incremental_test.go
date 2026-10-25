package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiIncrementalInputsIgnoreExecutionButNotPolicy(t *testing.T) {
	before := codeWikiRunConfig{TopicVersion: 1, DirectoryVersion: types.CodeWikiDirectoryVersionFrontend, SourcePolicyVersion: 1, FullSourceReanalysis: true, FrontendPlannerVersion: 2}
	before.FrontendPlanning = &codeWikiFrontendPlanning{}
	published := &types.CodeWikiRun{Status: types.CodeWikiRunStatusPublished, ModelID: "model", ProfileVersion: 3}
	after := before
	after.FullSourceReanalysis, after.FrontendPlanReady = false, false
	after.FrontendPlanning = nil
	after.TopicConcurrency, after.MaxModelCalls, after.TimeoutMinutes = 8, 20, 30
	require.True(t, compatibleCodeWikiIncrementalInputs(published, "model", 3, before, after))
	after.SourcePolicyVersion++
	require.False(t, compatibleCodeWikiIncrementalInputs(published, "model", 3, before, after))
	after = before
	after.ContentInstructions = "different output requirements"
	require.False(t, compatibleCodeWikiIncrementalInputs(published, "model", 3, before, after))
	require.False(t, compatibleCodeWikiIncrementalInputs(published, "other model", 3, before, before))
}

func TestCodeWikiIncrementalPreservedTopicCarriesAnalysisWithoutGeneration(t *testing.T) {
	s, db, baselineRun, _ := topicServiceFixture(t)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_incremental_page_identity ON code_wiki_run_pages(run_id, page_key)`).Error)
	ctx := context.Background()
	baselineRun.Status = types.CodeWikiRunStatusPublished
	require.NoError(t, s.repo.UpdateRun(ctx, baselineRun))
	cp := types.CodeWikiTopicCheckpoint{Version: 1, Items: []types.CodeWikiTopicItem{{Key: "fact", Status: "analyzed", Conclusion: "Unchanged conclusion", EvidenceIDs: []string{"E1"}}},
		Evidence: []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "old", CommitSHA: "old-sha", Path: "a.ts", StartLine: 1, EndLine: 1}}}
	blob, err := json.Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, db.Model(&types.CodeWikiTopic{}).Where("run_id = ? AND topic_key = ?", baselineRun.ID, "0").Updates(map[string]any{"status": "completed", "checkpoint": types.JSON(blob), "revision": 7}).Error)
	live := &types.WikiPage{ID: "live", KnowledgeBaseID: "kb", Slug: "0", Title: "Stable", Content: "Exact original bytes\r\n", Version: 4}
	require.NoError(t, db.Create(live).Error)
	run := &types.CodeWikiRun{TenantID: 7, KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusPending}
	require.NoError(t, s.repo.CreateRun(ctx, run))
	page := codeWikiPlannedPage{Slug: "0", Title: live.Title, RepositoryID: "front", SourcePaths: []string{"a.ts"}}
	require.NoError(t, s.persistPlannedPages(ctx, run, []codeWikiPlannedPage{page}))
	state := &codeWikiRunState{run: run, pages: []codeWikiPlannedPage{page}, cfg: codeWikiRunConfig{MaxModelCalls: 10, TimeoutMinutes: 10, TopicConcurrency: 3,
		PublicationGuard: &types.CodeWikiPublicationGuard{TopicUpdates: true, ReviewApproved: true, BaselineRunID: baselineRun.ID,
			Pages: map[string]string{"0": types.CodeWikiPageFingerprint(live)}, Impact: map[string]types.CodeWikiPageImpact{"0": {Kind: "unaffected"}}}},
		scopes: map[string]codebase.SnapshotScope{"front": {SnapshotID: "new", CommitSHA: "new-sha"}}}
	require.NoError(t, s.startCodeWikiTopics(ctx, state, types.CodeWikiGeneratePayload{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID}))
	rows, err := s.repo.ListRunPages(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunPageStatusPreserved, rows[0].Status)
	require.Equal(t, live.Content, rows[0].Content)
	topic, err := s.repo.(interfaces.CodeWikiTopicRepository).GetTopic(ctx, 7, "kb", run.ID, "0")
	require.NoError(t, err)
	require.Equal(t, "completed", topic.Status)
	require.Equal(t, 7, topic.Revision)
	var copied types.CodeWikiTopicCheckpoint
	require.NoError(t, json.Unmarshal(topic.Checkpoint, &copied))
	require.Equal(t, cp.Items, copied.Items)
	require.Equal(t, "new", copied.Evidence[0].SnapshotID)
	require.Zero(t, run.ModelCalls)
	// A later deletion can require only a link edit in an otherwise unchanged
	// document. Reuse the same facts and preserve every other content byte.
	state.cfg.PublicationGuard.Impact["0"] = types.CodeWikiPageImpact{Kind: "link_cleanup"}
	state.cfg.PublicationGuard.ArchivePages = map[string]string{"retired": "fingerprint"}
	live.Content = "Exact original bytes\r\n[[retired|Former entry]]\n"
	require.NoError(t, db.Model(live).Update("content", live.Content).Error)
	state.cfg.PublicationGuard.Pages["0"] = types.CodeWikiPageFingerprint(live)
	require.NoError(t, s.startCodeWikiTopics(ctx, state, types.CodeWikiGeneratePayload{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID}))
	rows, err = s.repo.ListRunPages(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunPageStatusStaged, rows[0].Status)
	require.Equal(t, "Exact original bytes\r\nFormer entry\n", rows[0].Content)
	require.Zero(t, run.ModelCalls)
}

func TestCodeWikiImpactUsesOldNewTransitiveEdgesAndEvidence(t *testing.T) {
	graph := map[string]frontendIndexCandidate{
		"screen.tsx": moduleCandidate("screen.tsx", "params.ts"),
		"params.ts":  moduleCandidate("params.ts", "client.ts"),
		"client.ts":  moduleCandidate("client.ts", "params.ts"), // Cycle terminates.
	}
	page := codeWikiPlannedPage{RepositoryID: "front", SourcePaths: []string{"screen.tsx"}, DependencyVersion: 1,
		DependencyPaths: frontendDependencyPaths([]string{"screen.tsx"}, graph)}
	require.Equal(t, []string{"client.ts", "params.ts", "screen.tsx"}, page.DependencyPaths)
	for _, p := range []string{"client.ts", "params.ts", "screen.tsx"} {
		impact := codeWikiTopicImpact(page, []types.CodeWikiSourceChange{{RepositoryID: "front", Path: p, Kind: "modified"}}, nil)
		require.Equal(t, "dependency_changed", impact.Kind)
	}
	impact := codeWikiTopicImpact(page, []types.CodeWikiSourceChange{{RepositoryID: "front", Path: "other.ts", Kind: "modified"}}, nil)
	require.Equal(t, "unaffected", impact.Kind)
	impact = codeWikiTopicImpact(page, []types.CodeWikiSourceChange{{RepositoryID: "front", Path: "caller.ts", Kind: "deleted"}}, []types.CodeWikiEvidenceSpec{{RepositoryID: "front", Path: "caller.ts"}})
	require.Equal(t, "dependency_changed", impact.Kind, "old evidence survives deleted/new graph edges")
	impact = codeWikiTopicImpact(page, []types.CodeWikiSourceChange{{RepositoryID: "backend", Path: "handler.go", Kind: "modified"}}, nil)
	require.Equal(t, "cross_repository_review", impact.Kind)
	page.DependencyVersion = 0
	impact = codeWikiTopicImpact(page, []types.CodeWikiSourceChange{{RepositoryID: "front", Path: "other.ts", Kind: "modified"}}, nil)
	require.Equal(t, "dependency_gap_review", impact.Kind)
}

func TestCodeWikiIncrementalSourceMovesRequireUniqueSameRepositoryHashes(t *testing.T) {
	changes := []types.CodeWikiSourceChange{
		{RepositoryID: "front", Path: "old.ts", Kind: "deleted", BeforeHash: "exact"},
		{RepositoryID: "front", Path: "new.ts", Kind: "added", AfterHash: "exact"},
		{RepositoryID: "back", Path: "same.ts", Kind: "added", AfterHash: "exact"},
	}
	moves := codeWikiSourceMoves(changes)
	require.Equal(t, map[string]string{"front\x00old.ts": "new.ts"}, moves)
	page := codeWikiPlannedPage{Slug: "stable", Title: "Existing topic", RepositoryID: "front", SourcePaths: []string{"old.ts"}}
	relocateCodeWikiSources(&page, moves)
	require.Equal(t, "stable", page.Slug)
	require.Equal(t, "Existing topic", page.Title)
	require.Equal(t, []string{"new.ts"}, page.SourcePaths)
	changes = append(changes, types.CodeWikiSourceChange{RepositoryID: "front", Path: "ambiguous.ts", Kind: "added", AfterHash: "exact"})
	require.Empty(t, codeWikiSourceMoves(changes), "ambiguous copies do not establish identity")
}

func TestCodeWikiRepeatedIncrementalPlanningDoesNotRenameOrCallModelForExistingFiles(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{
		"strange/a.ts": "export const a = 1;", "elsewhere/b.ts": "export const b = 1;",
	})
	state.cfg.TopicVersion, state.cfg.SourcePolicyVersion = 1, 1
	state.cfg.PublicationGuard = &types.CodeWikiPublicationGuard{TopicUpdates: true}
	provider := &frontendIndexProvider{hints: []codebase.PlanningHint{
		{Path: "strange/a.ts", Line: 1, Kind: "Function", Name: "a"},
		{Path: "elsewhere/b.ts", Line: 1, Kind: "Function", Name: "b"},
	}}
	state.provider = provider
	state.pages = []codeWikiPlannedPage{frontendTopic("frontend_api", "stable-a", "strange/a.ts"), frontendTopic("frontend_page", "stable-b", "elsewhere/b.ts")}
	for i := range state.pages {
		state.pages[i].RepositoryID = "front"
	}
	// Any accidental planning call fails: there is no chat model configured.
	for round := 0; round < 4; round++ {
		state.cfg.FrontendPlanning = nil
		for i := range state.pages {
			if state.pages[i].Domain == "stable-a" {
				state.pages[i].Title = "固定标题 A"
			} else {
				state.pages[i].Title = "固定标题 B"
			}
		}
		pages, err := (&codeWikiService{}).planIndexedFrontendCatalog(context.Background(), state, summaries, "Chinese")
		require.NoError(t, err)
		require.Len(t, pages, 2)
		byDomain := map[string]codeWikiPlannedPage{}
		for _, page := range pages {
			byDomain[page.Domain] = page
		}
		require.Equal(t, "固定标题 A", byDomain["stable-a"].Title)
		require.Equal(t, "固定标题 B", byDomain["stable-b"].Title)
		require.Equal(t, 1, byDomain["stable-a"].DependencyVersion)
		require.Equal(t, []string{"strange/a.ts"}, byDomain["stable-a"].DependencyPaths)
		state.pages = pages
		provider.hints[0], provider.hints[1] = provider.hints[1], provider.hints[0]
		// Simulate persistence between rounds, not shared pointer state.
		blob, err := json.Marshal(state.pages)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(blob, &state.pages))
	}
	require.Zero(t, provider.reads)
	require.Zero(t, state.run.ModelCalls)
}
