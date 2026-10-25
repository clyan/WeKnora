package service

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiNoChangeRequiresCompleteJointInput(t *testing.T) {
	cfg := codeWikiRunConfig{DirectoryVersion: 1, Languages: []string{"Chinese"}, MaxPages: 60}
	blob, err := json.Marshal(cfg)
	require.NoError(t, err)
	published := &types.CodeWikiRun{Status: types.CodeWikiRunStatusPublished, ModelID: "model-1", ProfileVersion: 2, ConfigSnapshot: blob}
	front := &types.CodeWikiRunRepository{RepositoryID: "frontend", SnapshotID: "f-snapshot", ArtifactID: "f-index", CommitSHA: "f-sha", Role: "frontend"}
	back := &types.CodeWikiRunRepository{RepositoryID: "backend", SnapshotID: "b-snapshot", ArtifactID: "b-index", CommitSHA: "b-sha", Role: "backend"}
	baseline := []*types.CodeWikiRunRepository{front, back}
	check := func(cfg codeWikiRunConfig, target []*types.CodeWikiRunRepository) bool {
		return sameCodeWikiGenerationInputs(published, "model-1", 2, cfg, baseline, target)
	}
	// Different response order, task row IDs and tracking branch names cannot
	// manufacture a code change. Repeat checks do not mutate the baseline.
	for range 5 {
		require.True(t, check(cfg, []*types.CodeWikiRunRepository{back, front}))
	}
	for _, original := range []*types.CodeWikiRunRepository{front, back} {
		changed := *original
		changed.CommitSHA = "different-branch-content"
		target := []*types.CodeWikiRunRepository{front, back}
		if original == front {
			target[0] = &changed
		} else {
			target[1] = &changed
		}
		require.False(t, check(cfg, target), "either side changing invalidates the joint baseline")
	}
	changedFilters := *front
	changedFilters.SnapshotID = "same-commit-different-filter-snapshot"
	require.False(t, check(cfg, []*types.CodeWikiRunRepository{&changedFilters, back}))
	changedConfig := cfg
	changedConfig.ContentInstructions = "new instructions"
	require.False(t, check(changedConfig, baseline))
	require.False(t, check(cfg, []*types.CodeWikiRunRepository{front}))
	require.False(t, check(cfg, []*types.CodeWikiRunRepository{front, front}))
	excluded := *back
	excluded.Excluded = true
	require.False(t, check(cfg, []*types.CodeWikiRunRepository{front, &excluded}))
	require.False(t, sameCodeWikiGenerationInputs(published, "different-model", 2, cfg, baseline, baseline))
	require.False(t, sameCodeWikiGenerationInputs(published, "model-1", 3, cfg, baseline, baseline))
	published.ConfigSnapshot = types.JSON(`invalid`)
	require.False(t, check(cfg, baseline))
}

func TestCodeWikiFrontendNoChangeIgnoresExecutionCheckpoints(t *testing.T) {
	cfg := codeWikiRunConfig{DirectoryVersion: types.CodeWikiDirectoryVersionFrontend, FrontendPlannerVersion: 1}
	stored := cfg
	stored.FrontendPlanReady = true
	stored.FrontendPlanning = &codeWikiFrontendPlanning{Repositories: map[string]*frontendRepoPlan{
		"frontend": {Cursor: 32, Notes: "execution-only notes"},
	}}
	blob, err := json.Marshal(stored)
	require.NoError(t, err)
	published := &types.CodeWikiRun{Status: types.CodeWikiRunStatusPublished, ModelID: "model", ConfigSnapshot: blob}
	repos := []*types.CodeWikiRunRepository{{RepositoryID: "frontend", SnapshotID: "snap", ArtifactID: "index", CommitSHA: "sha", Role: "frontend"}}
	require.True(t, sameCodeWikiGenerationInputs(published, "model", 0, cfg, repos, repos))
	cfg.TopicConcurrency = 3
	require.True(t, sameCodeWikiGenerationInputs(published, "model", 0, cfg, repos, repos), "execution concurrency does not change code facts")
	cfg.FrontendPlannerVersion++
	require.False(t, sameCodeWikiGenerationInputs(published, "model", 0, cfg, repos, repos), "planner changes are generation inputs")
}
