package repository

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiFactVersionsAreScopedImmutableAndReplayable(t *testing.T) {
	db := newCodeWikiTestDB(t)
	// Execute the actual migration against the existing schema as well.
	migration, err := os.ReadFile("../../../migrations/sqlite/000017_code_wiki_facts.up.sql")
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	base := NewCodeWikiRepository(db)
	repo := base.(interfaces.CodeWikiFactRepository)
	ctx := context.Background()
	run := &types.CodeWikiRun{ID: "facts-v1", TenantID: 1, KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusAnalyzing}
	require.NoError(t, base.CreateRun(ctx, run))
	scope := types.CodeWikiFactScope{TenantID: 1, KnowledgeBaseID: "kb", RunID: run.ID}
	row := func(key string, value any) *types.CodeWikiFactRecord {
		payload, err := json.Marshal(value)
		require.NoError(t, err)
		return &types.CodeWikiFactRecord{TenantID: 1, KnowledgeBaseID: "kb", RunID: run.ID, SchemaVersion: 1,
			RecordType: "source", RecordKey: key, Payload: payload}
	}
	first := row("a", types.CodeWikiSourceDecision{Path: "source.ts", Role: "implementation"})
	require.NoError(t, repo.AppendFactRecords(ctx, scope, []*types.CodeWikiFactRecord{first}))
	require.NoError(t, repo.AppendFactRecords(ctx, scope, []*types.CodeWikiFactRecord{first}))
	require.Error(t, repo.AppendFactRecords(ctx, scope, []*types.CodeWikiFactRecord{row("b", map[string]any{"role": "excluded"}), row("a", map[string]any{"role": "changed"})}))
	rows, err := repo.ListFactRecords(ctx, scope, "source", "", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "a", rows[0].RecordKey)
	rows, err = repo.ListFactRecords(ctx, scope, "source", "a", 10)
	require.NoError(t, err)
	require.Empty(t, rows, "conflicting batch must roll back its earlier inserts")
	for _, invalid := range []types.CodeWikiFactScope{{TenantID: 2, KnowledgeBaseID: "kb", RunID: run.ID}, {TenantID: 1, KnowledgeBaseID: "other", RunID: run.ID}, {TenantID: 1, KnowledgeBaseID: "kb", RunID: "missing"}, {}} {
		_, err := repo.ListFactRecords(ctx, invalid, "source", "", 10)
		require.Error(t, err)
		require.Error(t, repo.AppendFactRecords(ctx, invalid, []*types.CodeWikiFactRecord{first}))
	}
	require.NoError(t, db.Model(run).Update("status", types.CodeWikiRunStatusPublished).Error)
	require.ErrorContains(t, repo.AppendFactRecords(ctx, scope, []*types.CodeWikiFactRecord{first}), "sealed")
	next := &types.CodeWikiRun{ID: "facts-v2", TenantID: 1, KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusAnalyzing}
	require.NoError(t, base.CreateRun(ctx, next))
	nextScope := scope
	nextScope.RunID = next.ID
	second := row("a", map[string]any{"role": "declaration"})
	second.RunID = next.ID
	require.NoError(t, repo.AppendFactRecords(ctx, nextScope, []*types.CodeWikiFactRecord{second}))
	rows, err = repo.ListFactRecords(ctx, scope, "source", "", 10)
	require.NoError(t, err)
	require.Contains(t, string(rows[0].Payload), "implementation", "new versions must not mutate the published version")
	var count int64
	require.NoError(t, db.Model(&types.CodeWikiFactRecord{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
	require.Error(t, db.Create(first).Error, "the SQL layer must enforce the fact identity as well")
}

func TestCodeWikiFactPayloadRejectsHumanExplanationAsCode(t *testing.T) {
	payload, err := json.Marshal(types.CodeWikiFact{Kind: "permission", Subject: "save", State: "human_explanation", Attributes: types.JSON(`{}`), Evidence: types.StringArray{"source"}})
	require.NoError(t, err)
	require.ErrorContains(t, validateFactPayload(&types.CodeWikiFactRecord{RecordType: "fact", Payload: payload}), "human explanations")
}
