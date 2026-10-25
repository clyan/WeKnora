package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDeleteCanceledCodeWikiRun(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{
		types.CodeWikiRunStatusCanceled, types.CodeWikiRunStatusPending,
		types.CodeWikiRunStatusGenerating, types.CodeWikiRunStatusPaused,
		types.CodeWikiRunStatusStaged, types.CodeWikiRunStatusFailed,
		types.CodeWikiRunStatusPublished, types.CodeWikiRunStatusSuperseded,
	} {
		t.Run(status, func(t *testing.T) {
			db := newCodeWikiTestDB(t)
			repo := NewCodeWikiRepository(db)
			run := &types.CodeWikiRun{KnowledgeBaseID: "kb", Status: status}
			require.NoError(t, repo.CreateRun(ctx, run))
			// A run ID from another KB must never authorize deletion.
			require.ErrorIs(t, repo.DeleteCanceledRun(ctx, "other-kb", run.ID), types.ErrCodeWikiRunNotFound)
			_, err := repo.GetRun(ctx, "kb", run.ID)
			require.NoError(t, err)
			err = repo.DeleteCanceledRun(ctx, "kb", run.ID)
			if status != types.CodeWikiRunStatusCanceled {
				require.ErrorIs(t, err, types.ErrCodeWikiRunNotCanceled)
				stored, err := repo.GetRun(ctx, "kb", run.ID)
				require.NoError(t, err)
				require.Equal(t, status, stored.Status)
				return
			}
			require.NoError(t, err)
			_, err = repo.GetRun(ctx, "kb", run.ID)
			require.ErrorIs(t, err, types.ErrCodeWikiRunNotFound)
			runs, err := repo.ListRuns(ctx, "kb", 50)
			require.NoError(t, err)
			require.Empty(t, runs)
			require.ErrorIs(t, repo.DeleteCanceledRun(ctx, "kb", run.ID), types.ErrCodeWikiRunNotFound)
		})
	}
}

func TestDeleteCanceledCodeWikiRunCleansOnlyOwnedData(t *testing.T) {
	db := newCodeWikiTestDB(t)
	repo := NewCodeWikiRepository(db)
	ctx := context.Background()
	run := &types.CodeWikiRun{ID: "canceled", KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusCanceled}
	require.NoError(t, repo.CreateRun(ctx, run))
	other := &types.CodeWikiRun{ID: "published", KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusPublished}
	require.NoError(t, repo.CreateRun(ctx, other))
	models := []any{
		&types.CodeWikiRunPage{ID: "candidate", RunID: run.ID},
		&types.CodeWikiRunRepository{ID: "manifest", RunID: run.ID},
		&types.CodeWikiTopic{ID: "topic", RunID: run.ID, TopicKey: "page"},
		&types.CodeWikiRunEvent{RunID: run.ID},
		&types.CodeWikiFactRecord{ID: "fact", RunID: run.ID, Payload: types.JSON(`{}`)},
		&types.CodeWikiModelSlot{ID: "slot", RunID: run.ID},
	}
	for _, model := range models {
		require.NoError(t, db.Create(model).Error)
	}
	for _, model := range []any{
		&types.CodeWikiPageEvidence{ID: "candidate-evidence", RunID: run.ID},
		&types.CodeWikiPageEvidence{ID: "published-evidence", RunID: other.ID, PageID: "published-page"},
		&types.WikiPage{ID: "published-page", KnowledgeBaseID: "kb", Slug: "overview", Content: "published content"},
		&types.CodeWikiSnapshot{ID: "shared-snapshot", RepositoryID: "repo"},
	} {
		require.NoError(t, db.Create(model).Error)
	}
	require.NoError(t, repo.DeleteCanceledRun(ctx, "kb", run.ID))
	for _, model := range models {
		var count int64
		require.NoError(t, db.Model(model).Where("run_id = ?", run.ID).Count(&count).Error)
		require.Zero(t, count)
	}
	var count int64
	require.NoError(t, db.Model(&types.CodeWikiPageEvidence{}).Where("run_id = ?", run.ID).Count(&count).Error)
	require.Zero(t, count)
	_, err := repo.GetRun(ctx, "kb", other.ID)
	require.NoError(t, err)
	var page types.WikiPage
	require.NoError(t, db.First(&page, "id = ?", "published-page").Error)
	require.Equal(t, "published content", page.Content)
	var evidence types.CodeWikiPageEvidence
	require.NoError(t, db.First(&evidence, "id = ?", "published-evidence").Error)
	var snapshot types.CodeWikiSnapshot
	require.NoError(t, db.First(&snapshot, "id = ?", "shared-snapshot").Error)
}
