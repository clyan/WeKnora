package service

import (
	"context"
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/codefacts"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCodeWikiTopicAddContinuesCurrentAndInvalidatesDerivedGuide(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	s, db, run, _ := topicServiceFixture(t)
	ctx := context.Background()
	repo := s.repo.(interfaces.CodeWikiTopicRepository)
	state, _ := frontendCatalogFixture(t, map[string]string{"screen.tsx": "export const View = () => <button>Save</button>;"})
	state.run = run
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	state.cfg.PublicationGuard = &types.CodeWikiPublicationGuard{Pages: map[string]string{}, AllowedPages: []string{"0", types.CodeWikiSummaryTopicKey}}
	blob, _ := json.Marshal(state.cfg)
	require.NoError(t, db.Model(run).Update("config_snapshot", types.JSON(blob)).Error)
	require.NoError(t, db.Where("run_id = ? AND topic_key <> ?", run.ID, "0").Delete(&types.CodeWikiTopic{}).Error)
	require.NoError(t, db.Create(&types.CodeWikiRunRepository{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, RepositoryID: "front", Role: types.CodeWikiRepoRoleFrontend, DisplayName: "app/main"}).Error)
	require.NoError(t, db.Create(&types.CodeWikiTopic{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, TopicKey: types.CodeWikiSummaryTopicKey, Status: "completed", Revision: 3}).Error)
	for _, key := range []string{"0", types.CodeWikiSummaryTopicKey} {
		require.NoError(t, db.Create(&types.CodeWikiRunPage{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, PageKey: key, ValidationStatus: "passed", Status: "staged", Content: "old guide"}).Error)
	}
	caller, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "0", run.ExecutionEpoch, 0, "worker")
	require.NoError(t, err)
	targets, _ := json.Marshal([]codeWikiPlannedPage{frontendTopic("frontend_page", "new-operation", "screen.tsx")})
	cp := types.CodeWikiTopicCheckpoint{Version: 1, Evidence: []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Path: "screen.tsx", StartLine: 1, EndLine: 1, Type: "implementation"}}, Items: []types.CodeWikiTopicItem{{Key: "old-item", Question: "Original", Status: "pending"}}, PlanChange: &types.CodeWikiTopicPlanRequest{Operation: "add", Reason: "Independent operation", Targets: types.JSON(targets)}}
	require.NoError(t, s.applyTopicPlanChange(ctx, state, caller, &cp))
	own, err := repo.GetTopic(ctx, 7, "kb", run.ID, "0")
	require.NoError(t, err)
	require.Equal(t, "queued", own.Status)
	require.Equal(t, 1, own.Revision)
	var saved types.CodeWikiTopicCheckpoint
	require.NoError(t, json.Unmarshal(own.Checkpoint, &saved))
	require.Equal(t, "old-item", saved.Items[0].Key)
	guide, err := repo.GetTopic(ctx, 7, "kb", run.ID, types.CodeWikiSummaryTopicKey)
	require.NoError(t, err)
	require.Equal(t, "queued", guide.Status)
	require.Equal(t, 4, guide.Revision)
	pages, err := s.repo.ListRunPages(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, pages, 3)
	for _, page := range pages {
		if page.PageKey == types.CodeWikiSummaryTopicKey {
			require.Empty(t, page.Content)
			require.Equal(t, types.CodeWikiRunPageStatusPending, page.Status)
		}
	}
	// A duplicate target is rejected atomically, leaving current checkpoint
	// and directory unchanged even if a worker proposes it again.
	caller, err = repo.ClaimTopic(ctx, 7, "kb", run.ID, "0", run.ExecutionEpoch, own.Revision, "worker2")
	require.NoError(t, err)
	cp.PlanChange = &types.CodeWikiTopicPlanRequest{Operation: "add", Reason: "Duplicate", Targets: types.JSON(targets)}
	require.ErrorContains(t, s.applyTopicPlanChange(ctx, state, caller, &cp), "主题标识已存在")
	unchanged, err := repo.GetTopic(ctx, 7, "kb", run.ID, "0")
	require.NoError(t, err)
	require.Equal(t, own.Revision, unchanged.Revision)
	pages, err = s.repo.ListRunPages(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, pages, 3)
}

func TestCodeWikiTopicSplitPreservesOwnershipAndRejectsIncompleteTransfer(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	s, db, run, _ := topicServiceFixture(t)
	ctx := context.Background()
	repo := s.repo.(interfaces.CodeWikiTopicRepository)
	state, _ := frontendCatalogFixture(t, map[string]string{"screen.tsx": "export const View = () => <><button onClick={save}>Save</button><button onClick={remove}>Remove</button></>;"})
	state.run = run
	state.cfg.TopicVersion = 1
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	state.cfg.PublicationGuard = &types.CodeWikiPublicationGuard{Pages: map[string]string{}, AllowedPages: []string{"0"}}
	blob, err := json.Marshal(state.cfg)
	require.NoError(t, err)
	require.NoError(t, db.Model(run).Update("config_snapshot", types.JSON(blob)).Error)
	require.NoError(t, db.Where("run_id = ? AND topic_key <> ?", run.ID, "0").Delete(&types.CodeWikiTopic{}).Error)
	require.NoError(t, db.Create(&types.CodeWikiRunRepository{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Role: types.CodeWikiRepoRoleFrontend, DisplayName: "app/main"}).Error)
	require.NoError(t, db.Create(&types.CodeWikiRunPage{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, PageKey: "0", Status: types.CodeWikiRunPageStatusPending, ValidationStatus: "pending"}).Error)
	caller, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "0", run.ExecutionEpoch, 0, "worker")
	require.NoError(t, err)
	pages := []codeWikiPlannedPage{frontendTopic("frontend_page", "save-scope", "screen.tsx"), frontendTopic("frontend_page", "remove-scope", "screen.tsx")}
	encoded, err := json.Marshal(pages)
	require.NoError(t, err)
	cp := types.CodeWikiTopicCheckpoint{Version: 1, Evidence: []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Path: "screen.tsx", StartLine: 1, EndLine: 1, Type: "implementation"}}, Items: []types.CodeWikiTopicItem{{Key: "save", Kind: "interaction", Question: "Save", Status: "analyzed", Conclusion: "Save [E1]", EvidenceIDs: []string{"E1"}}, {Key: "remove", Kind: "interaction", Question: "Remove", Status: "pending"}}, PlanChange: &types.CodeWikiTopicPlanRequest{Operation: "split", Reason: "Independent operations", Targets: types.JSON(encoded), Assignments: []types.CodeWikiTopicAssignment{{SourceKey: "0", ItemKey: "save", TargetDomain: "save-scope"}}}}
	require.ErrorContains(t, s.applyTopicPlanChange(ctx, state, caller, &cp), "尚未为已发现项目指定去向")
	rows, err := repo.ListTopicStates(ctx, 7, "kb", run.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "an incomplete proposal must not create partial topics or change the directory")
	cp.PlanChange.Assignments = append(cp.PlanChange.Assignments, types.CodeWikiTopicAssignment{SourceKey: "0", ItemKey: "remove", TargetDomain: "remove-scope"})
	require.NoError(t, s.applyTopicPlanChange(ctx, state, caller, &cp))
	old, err := repo.GetTopic(ctx, 7, "kb", run.ID, "0")
	require.NoError(t, err)
	require.Equal(t, "superseded", old.Status)
	var archived types.CodeWikiTopicCheckpoint
	require.NoError(t, json.Unmarshal(old.Checkpoint, &archived))
	require.Len(t, archived.Items, 2)
	require.Len(t, archived.Successors, 2)
	active, err := repo.ListTopicStates(ctx, 7, "kb", run.ID)
	require.NoError(t, err)
	require.Len(t, active, 2)
	for _, topic := range active {
		full, err := repo.GetTopic(ctx, 7, "kb", run.ID, topic.TopicKey)
		require.NoError(t, err)
		var next types.CodeWikiTopicCheckpoint
		require.NoError(t, json.Unmarshal(full.Checkpoint, &next))
		require.Equal(t, types.CodeWikiTopicRef{Key: "0", Revision: 0}, next.Predecessors[0])
		found := false
		for _, item := range next.Items {
			if item.SourceTopicKey == "0" {
				found = true
				require.NotEmpty(t, item.SourceItemKey)
			}
		}
		require.True(t, found, "every successor contains its assigned original item")
	}
	activePages, err := s.repo.ListRunPages(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, activePages, 2)
	fresh, err := s.repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, 2, fresh.TotalPages)
	var frozen codeWikiRunConfig
	require.NoError(t, json.Unmarshal(fresh.ConfigSnapshot, &frozen))
	require.Len(t, frozen.PublicationGuard.AllowedPages, 2)
	require.NotContains(t, frozen.PublicationGuard.AllowedPages, "0")
	require.Error(t, repo.SaveTopic(ctx, old, run.ExecutionEpoch, 0), "a late source worker cannot resurrect the retired topic")
}

func TestCodeWikiTopicMergeFencesRunningAndChangedSources(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	s, db, run, _ := topicServiceFixture(t)
	ctx := context.Background()
	repo := s.repo.(interfaces.CodeWikiTopicRepository)
	state, _ := frontendCatalogFixture(t, map[string]string{"screen.tsx": "export const View = () => <button onClick={save}>Save</button>;", "api.ts": "export const save = () => fetch('/save');"})
	state.run = run
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	state.cfg.PublicationGuard = &types.CodeWikiPublicationGuard{Pages: map[string]string{}, AllowedPages: []string{"0", "1"}}
	blob, _ := json.Marshal(state.cfg)
	require.NoError(t, db.Model(run).Update("config_snapshot", types.JSON(blob)).Error)
	require.NoError(t, db.Where("run_id = ? AND topic_key NOT IN ?", run.ID, []string{"0", "1"}).Delete(&types.CodeWikiTopic{}).Error)
	require.NoError(t, db.Create(&types.CodeWikiRunRepository{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, RepositoryID: "front", Role: types.CodeWikiRepoRoleFrontend, DisplayName: "app/main"}).Error)
	for _, key := range []string{"0", "1"} {
		require.NoError(t, db.Create(&types.CodeWikiRunPage{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, PageKey: key, ValidationStatus: "pending", Status: "pending"}).Error)
	}
	caller, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "0", run.ExecutionEpoch, 0, "first")
	require.NoError(t, err)
	peer, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "1", run.ExecutionEpoch, 0, "second")
	require.NoError(t, err)
	peerCP := types.CodeWikiTopicCheckpoint{Version: 1, Evidence: []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Path: "api.ts", StartLine: 1, EndLine: 1, Type: "implementation"}}, Items: []types.CodeWikiTopicItem{{Key: "call", Kind: "request", Question: "Call", Status: "analyzed", Conclusion: "API fact [E1]", EvidenceIDs: []string{"E1"}}}}
	encoded, _ := json.Marshal([]codeWikiPlannedPage{frontendTopic("frontend_page", "merged", "screen.tsx", "api.ts")})
	cp := types.CodeWikiTopicCheckpoint{Version: 1, Evidence: []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Path: "screen.tsx", StartLine: 1, EndLine: 1, Type: "implementation"}}, Items: []types.CodeWikiTopicItem{{Key: "button", Kind: "interaction", Question: "Button", Status: "analyzed", Conclusion: "Page fact [E1]", EvidenceIDs: []string{"E1"}}}, PlanChange: &types.CodeWikiTopicPlanRequest{Operation: "merge", Reason: "One shared operation", Sources: []types.CodeWikiTopicRef{{Key: "1", Revision: 0}}, Targets: types.JSON(encoded)}}
	require.ErrorContains(t, s.applyTopicPlanChange(ctx, state, caller, &cp), "仍在执行或修订已变化")
	blob, _ = json.Marshal(peerCP)
	peer.Checkpoint, peer.Status = types.JSON(blob), "queued"
	require.NoError(t, repo.SaveTopic(ctx, peer, run.ExecutionEpoch, peer.Revision))
	require.ErrorContains(t, s.applyTopicPlanChange(ctx, state, caller, &cp), "修订已变化")
	cp.PlanChange.Sources[0].Revision = peer.Revision
	require.NoError(t, s.applyTopicPlanChange(ctx, state, caller, &cp))
	active, err := repo.ListTopicStates(ctx, 7, "kb", run.ID)
	require.NoError(t, err)
	require.Len(t, active, 1)
	merged, err := repo.GetTopic(ctx, 7, "kb", run.ID, active[0].TopicKey)
	require.NoError(t, err)
	var mergedCP types.CodeWikiTopicCheckpoint
	require.NoError(t, json.Unmarshal(merged.Checkpoint, &mergedCP))
	require.Len(t, mergedCP.Predecessors, 2)
	require.Len(t, mergedCP.Evidence, 2)
	found := map[string]bool{}
	for _, item := range mergedCP.Items {
		if item.SourceTopicKey != "" {
			found[item.SourceTopicKey] = true
			require.NotEmpty(t, item.EvidenceIDs)
			require.Contains(t, item.Conclusion, "["+item.EvidenceIDs[0]+"]")
		}
	}
	require.Len(t, found, 2, "both original conclusions and their own evidence survive the merge")
	old, err := repo.GetTopic(ctx, 7, "kb", run.ID, "1")
	require.NoError(t, err)
	require.Equal(t, "superseded", old.Status)
	require.Error(t, repo.SaveTopic(ctx, peer, run.ExecutionEpoch, peer.Revision))
}
