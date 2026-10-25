package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func topicFixture(t *testing.T, count, budget int) (*codeWikiRepository, *types.CodeWikiRun) {
	t.Helper()
	repo := NewCodeWikiRepository(newCodeWikiTestDB(t)).(*codeWikiRepository)
	run := &types.CodeWikiRun{TenantID: 7, KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusPending, ModelID: "model"}
	require.NoError(t, repo.CreateRun(context.Background(), run))
	var topics []*types.CodeWikiTopic
	for i := 0; i < count; i++ {
		topics = append(topics, &types.CodeWikiTopic{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, TopicKey: fmt.Sprint(i), Title: "topic", Checkpoint: types.JSON(`{"version":1}`)})
	}
	require.NoError(t, repo.EnsureTopics(context.Background(), run, topics))
	require.NoError(t, repo.EnsureTopics(context.Background(), run, topics))
	require.NoError(t, repo.BeginTopicPass(context.Background(), run, budget, time.Hour, 3))
	return repo, run
}

func TestCodeWikiTopicConcurrentClaimsAndLeaseFencing(t *testing.T) {
	repo, run := topicFixture(t, 8, 100)
	ctx := context.Background()
	var workers sync.WaitGroup
	var mu sync.Mutex
	var claims []*types.CodeWikiTopic
	var failures []error
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			row, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, fmt.Sprint(i%8), run.ExecutionEpoch, 0, uuid.NewString())
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
			}
			if row != nil {
				claims = append(claims, row)
			}
		}(i)
	}
	workers.Wait()
	require.Empty(t, failures)
	require.Len(t, claims, 3)
	rows, err := repo.ListTopics(ctx, 7, "kb", run.ID)
	require.NoError(t, err)
	require.Len(t, rows, 8, "duplicate discovery must be idempotent")
	foreign, err := repo.ListTopics(ctx, 8, "kb", run.ID)
	require.NoError(t, err)
	require.Empty(t, foreign)
	_, err = repo.ClaimTopic(ctx, 8, "kb", run.ID, "0", run.ExecutionEpoch, 0, "foreign")
	require.Error(t, err)

	old := claims[0]
	require.NoError(t, repo.db.Model(old).Update("lease_expires_at", time.Now().UTC().Add(-time.Minute)).Error)
	fresh, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, old.TopicKey, run.ExecutionEpoch, old.Revision, "replacement")
	require.NoError(t, err)
	require.NotNil(t, fresh)
	require.NotEqual(t, old.AttemptID, fresh.AttemptID)
	old.Status = "queued"
	require.Error(t, repo.SaveTopic(ctx, old, run.ExecutionEpoch, old.Revision))
	fresh.Status = "queued"
	require.NoError(t, repo.SaveTopic(ctx, fresh, run.ExecutionEpoch, fresh.Revision))
	require.Error(t, repo.SaveTopic(ctx, fresh, run.ExecutionEpoch, fresh.Revision-1))

	// Starting a new pass fences every result from the preceding pass.
	epoch := run.ExecutionEpoch
	require.NoError(t, repo.BeginTopicPass(ctx, run, 10, time.Hour, 3))
	require.Error(t, repo.HeartbeatTopic(ctx, claims[1], epoch))
}

func TestCodeWikiTopicOverviewRebuiltAfterRetryWithoutNewModelBudget(t *testing.T) {
	repo, run := topicFixture(t, 1, 1)
	ctx := context.Background()
	key := types.CodeWikiSummaryTopicKey
	require.NoError(t, repo.EnsureTopics(ctx, run, []*types.CodeWikiTopic{{TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID, RunID: run.ID, TopicKey: key, Status: "completed", Checkpoint: types.JSON(`{"version":1}`)}}))
	require.NoError(t, repo.db.Create(&types.CodeWikiRunPage{TenantID: run.TenantID, KnowledgeBaseID: run.KnowledgeBaseID, RunID: run.ID, PageKey: key, Status: types.CodeWikiRunPageStatusStaged, Content: "old guide"}).Error)
	require.NoError(t, repo.BeginTopicPass(ctx, run, 1, time.Hour, 3))
	guide, err := repo.GetTopic(ctx, run.TenantID, run.KnowledgeBaseID, run.ID, key)
	require.NoError(t, err)
	require.Equal(t, "queued", guide.Status)
	var page types.CodeWikiRunPage
	require.NoError(t, repo.db.Where("run_id = ? AND page_key = ?", run.ID, key).First(&page).Error)
	require.Equal(t, types.CodeWikiRunPageStatusPending, page.Status)
	require.Empty(t, page.Content)
	require.NoError(t, repo.db.Model(&types.CodeWikiTopic{}).Where("run_id = ? AND topic_key = ?", run.ID, "0").Update("status", "completed").Error)
	require.NoError(t, repo.db.Model(run).Update("model_calls", run.ModelCallLimit).Error)
	refreshed, err := repo.RefreshTopicRun(ctx, run.TenantID, run.KnowledgeBaseID, run.ID, run.ExecutionEpoch)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusGenerating, refreshed.Status, "a source-based guide does not consume another model call")
	claimed, err := repo.ClaimTopic(ctx, run.TenantID, run.KnowledgeBaseID, run.ID, key, run.ExecutionEpoch, guide.Revision, "summary-worker")
	require.NoError(t, err)
	require.NotNil(t, claimed)
}

func TestCodeWikiTopicProgressSnapshotIsScopedAndVersioned(t *testing.T) {
	repo, run := topicFixture(t, 3, 10)
	ctx := context.Background()
	first, err := repo.ReadRunProgress(ctx, run.TenantID, run.KnowledgeBaseID, run.ID)
	require.NoError(t, err)
	require.Len(t, first.Topics, 3)
	require.Empty(t, first.Topics[0].Checkpoint)
	unchanged, err := repo.ReadRunProgress(ctx, run.TenantID, run.KnowledgeBaseID, run.ID)
	require.NoError(t, err)
	require.Equal(t, first.Revision, unchanged.Revision)
	_, err = repo.ReadRunProgress(ctx, run.TenantID+1, run.KnowledgeBaseID, run.ID)
	require.ErrorIs(t, err, types.ErrCodeWikiRunNotFound)
	topic, err := repo.ClaimTopic(ctx, run.TenantID, run.KnowledgeBaseID, run.ID, "0", run.ExecutionEpoch, 0, "worker")
	require.NoError(t, err)
	claimed, err := repo.ReadRunProgress(ctx, run.TenantID, run.KnowledgeBaseID, run.ID)
	require.NoError(t, err)
	require.NotEqual(t, first.Revision, claimed.Revision)
	topic.Status = "completed"
	require.NoError(t, repo.SaveTopic(ctx, topic, run.ExecutionEpoch, topic.Revision))
	complete, err := repo.ReadRunProgress(ctx, run.TenantID, run.KnowledgeBaseID, run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, complete.Run.CompletedPages, "progress uses the same topic snapshot, including before aggregate refresh")
}

func TestCodeWikiTopicDelayedDuplicateCannotAdvanceNewCheckpoint(t *testing.T) {
	repo, run := topicFixture(t, 1, 10)
	ctx := context.Background()
	topic, err := repo.ClaimTopic(ctx, run.TenantID, run.KnowledgeBaseID, run.ID, "0", run.ExecutionEpoch, 0, "first")
	require.NoError(t, err)
	require.NotNil(t, topic)
	topic.Status = "queued"
	require.NoError(t, repo.SaveTopic(ctx, topic, run.ExecutionEpoch, 0))
	duplicate, err := repo.ClaimTopic(ctx, run.TenantID, run.KnowledgeBaseID, run.ID, "0", run.ExecutionEpoch, 0, "duplicate")
	require.NoError(t, err)
	require.Nil(t, duplicate, "old delivery must not trigger another model request after a checkpoint advanced")
	next, err := repo.ClaimTopic(ctx, run.TenantID, run.KnowledgeBaseID, run.ID, "0", run.ExecutionEpoch, topic.Revision, "next")
	require.NoError(t, err)
	require.NotNil(t, next)
}

func TestCodeWikiTopicCheckpointsFollowExplicitKBDeletion(t *testing.T) {
	repo, run := topicFixture(t, 1, 10)
	ctx := context.Background()
	require.NoError(t, repo.db.Create(&types.CodeWikiTopic{ID: uuid.NewString(), TenantID: 9, KnowledgeBaseID: "other-kb", RunID: "other-run", TopicKey: "other"}).Error)
	reserved, err := repo.ReserveTopicModelCall(ctx, run, "scope/request", 3)
	require.NoError(t, err)
	require.True(t, reserved)
	require.NoError(t, repo.DeleteRunsByKB(ctx, "kb"))
	var count int64
	require.NoError(t, repo.db.Model(&types.CodeWikiTopic{}).Where("knowledge_base_id = ?", "kb").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, repo.db.Model(&types.CodeWikiTopic{}).Where("knowledge_base_id = ?", "other-kb").Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, repo.db.Model(&types.CodeWikiModelSlot{}).Where("run_id = ?", run.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestCodeWikiTopicAtomicBudgetAndIdempotentSettlement(t *testing.T) {
	repo, run := topicFixture(t, 2, 5)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var slots []string
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copyRun := *run
			slot := "shared-credentials/" + uuid.NewString()
			ok, _ := repo.ReserveTopicModelCall(ctx, &copyRun, slot, 3)
			if ok {
				mu.Lock()
				slots = append(slots, slot)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Len(t, slots, 3)
	for _, slot := range slots {
		require.NoError(t, repo.FinishTopicModelCall(ctx, run, slot, 10))
		require.NoError(t, repo.FinishTopicModelCall(ctx, run, slot, 10))
	}
	for i := 0; i < 2; i++ {
		ok, err := repo.ReserveTopicModelCall(ctx, run, "shared-credentials/"+uuid.NewString(), 3)
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := repo.ReserveTopicModelCall(ctx, run, "shared-credentials/overflow", 3)
	require.Error(t, err)
	require.False(t, ok)
	fresh, err := repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, 5, fresh.ModelCalls)
	require.EqualValues(t, 30, fresh.TokensUsed)
}

func TestCodeWikiTopicPausePreservesLastCallAndSuccessfulTopics(t *testing.T) {
	repo, run := topicFixture(t, 2, 1)
	ctx := context.Background()
	topic, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "0", run.ExecutionEpoch, 0, "worker")
	require.NoError(t, err)
	require.NotNil(t, topic)
	page := &types.CodeWikiRunPage{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, PageKey: "0", Status: types.CodeWikiRunPageStatusPending}
	require.NoError(t, repo.db.Create(page).Error)
	ok, err := repo.ReserveTopicModelCall(ctx, run, "scope/request", 3)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = repo.RefreshTopicRun(ctx, 7, "kb", run.ID, run.ExecutionEpoch)
	require.NoError(t, err)
	page.Content, page.Status, page.ValidationStatus = "verified result", types.CodeWikiRunPageStatusStaged, "passed"
	require.NoError(t, repo.StageTopic(ctx, topic, run.ExecutionEpoch, topic.Revision, page))
	paused, err := repo.RefreshTopicRun(ctx, 7, "kb", run.ID, run.ExecutionEpoch)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusPaused, paused.Status)
	require.Equal(t, 1, paused.CompletedPages)
	claimed, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "1", run.ExecutionEpoch, 0, "late")
	require.NoError(t, err)
	require.Nil(t, claimed)
	require.NoError(t, repo.RequestRunControl(ctx, "kb", run.ID, "resume"))
	require.NoError(t, repo.BeginTopicPass(ctx, run, 10, time.Hour, 3))
	rows, err := repo.ListTopics(ctx, 7, "kb", run.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", rows[0].Status)
	require.Equal(t, "queued", rows[1].Status)
}

func TestCodeWikiTopicPartialFailureRetryAndCancelFenceLateResults(t *testing.T) {
	repo, run := topicFixture(t, 2, 10)
	ctx := context.Background()
	first, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "0", run.ExecutionEpoch, 0, "first")
	require.NoError(t, err)
	second, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "1", run.ExecutionEpoch, 0, "second")
	require.NoError(t, err)
	first.Status = "completed"
	require.NoError(t, repo.SaveTopic(ctx, first, run.ExecutionEpoch, first.Revision))
	second.Status = "failed"
	require.NoError(t, repo.SaveTopic(ctx, second, run.ExecutionEpoch, second.Revision))
	paused, err := repo.RefreshTopicRun(ctx, 7, "kb", run.ID, run.ExecutionEpoch)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusPaused, paused.Status)
	require.Equal(t, 1, paused.CompletedPages)
	require.Equal(t, 1, paused.FailedPages)
	require.NoError(t, repo.RetryTopic(ctx, 7, "kb", run.ID, "1"))
	require.NoError(t, repo.RequestRunControl(ctx, "kb", run.ID, "resume"))
	require.NoError(t, repo.BeginTopicPass(ctx, run, 10, time.Hour, 3))
	retry, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "1", run.ExecutionEpoch, second.Revision, "retry")
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.NoError(t, repo.RequestRunControl(ctx, "kb", run.ID, "cancel"))
	closing, err := repo.RefreshTopicRun(ctx, 7, "kb", run.ID, run.ExecutionEpoch)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusGenerating, closing.Status, "an active worker is visibly ending before cancel settles")
	latePage := &types.CodeWikiRunPage{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, PageKey: "1", Content: "late"}
	require.Error(t, repo.StageTopic(ctx, retry, run.ExecutionEpoch, retry.Revision, latePage), "canceled provider output cannot become a document")
	retry.Status = "paused"
	require.NoError(t, repo.SaveTopic(ctx, retry, run.ExecutionEpoch, retry.Revision))
	canceled, err := repo.RefreshTopicRun(ctx, 7, "kb", run.ID, run.ExecutionEpoch)
	require.NoError(t, err)
	require.Equal(t, types.CodeWikiRunStatusCanceled, canceled.Status)
	require.Equal(t, 1, canceled.CompletedPages, "the independent successful topic stays available")
}

func TestCodeWikiTopicConcurrentConclusionsQueueRecheckBeforeStaging(t *testing.T) {
	repo, run := topicFixture(t, 2, 20)
	ctx := context.Background()
	var claims []*types.CodeWikiTopic
	for i := 0; i < 2; i++ {
		key := fmt.Sprint(i)
		topic, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, key, run.ExecutionEpoch, 0, "worker-"+key)
		require.NoError(t, err)
		cp := types.CodeWikiTopicCheckpoint{Version: 1, Items: []types.CodeWikiTopicItem{{Key: "source-shared", SourceNodeKey: "source-shared", Kind: "permission", Question: "Who can save?", Status: "analyzed", Conclusion: "Interpretation " + key, EvidenceIDs: []string{"E1"}}}}
		blob, err := json.Marshal(cp)
		require.NoError(t, err)
		topic.Checkpoint = types.JSON(blob)
		claims = append(claims, topic)
		require.NoError(t, repo.db.Create(&types.CodeWikiRunPage{ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, PageKey: key, Status: types.CodeWikiRunPageStatusPending}).Error)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, topic := range claims {
		wg.Add(1)
		go func(topic *types.CodeWikiTopic) {
			defer wg.Done()
			results <- repo.StageTopic(ctx, topic, run.ExecutionEpoch, topic.Revision, &types.CodeWikiRunPage{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, PageKey: topic.TopicKey, Content: "checked body", Status: types.CodeWikiRunPageStatusStaged, ValidationStatus: "passed"})
		}(topic)
	}
	wg.Wait()
	close(results)
	queued := 0
	for err := range results {
		if err == types.ErrCodeWikiTopicNeedsRecheck {
			queued++
		} else {
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, queued, "the run lock makes the second completion see the first, even when both finish concurrently")
	rows, err := repo.ListTopics(ctx, 7, "kb", run.ID)
	require.NoError(t, err)
	for _, row := range rows {
		var cp types.CodeWikiTopicCheckpoint
		require.NoError(t, json.Unmarshal(row.Checkpoint, &cp))
		require.Contains(t, cp.Items[0].Conclusion, "Interpretation")
		if row.Status == "queued" {
			require.Len(t, cp.Items, 2)
			require.Equal(t, "pending", cp.Items[1].Status)
			require.NotNil(t, cp.Items[1].ConsistencyPeer)
			require.Empty(t, types.CodeWikiConsistencyQuestions(row.TopicKey, &cp, rows), "the same comparison is not repeatedly inserted")
			var candidate types.CodeWikiRunPage
			require.NoError(t, repo.db.Where("run_id = ? AND page_key = ?", run.ID, row.TopicKey).First(&candidate).Error)
			require.Equal(t, types.CodeWikiRunPageStatusPending, candidate.Status)
		}
	}
}

func TestCodeWikiTopicSQLiteMigrationRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("CREATE TABLE code_wiki_runs (id TEXT PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("CREATE TABLE code_wiki_run_events (id INTEGER PRIMARY KEY)").Error)
	up, err := os.ReadFile("../../../migrations/sqlite/000018_code_wiki_topics.up.sql")
	require.NoError(t, err)
	down, err := os.ReadFile("../../../migrations/sqlite/000018_code_wiki_topics.down.sql")
	require.NoError(t, err)
	scopeUp, err := os.ReadFile("../../../migrations/sqlite/000019_code_wiki_model_slot_scope.up.sql")
	require.NoError(t, err)
	scopeDown, err := os.ReadFile("../../../migrations/sqlite/000019_code_wiki_model_slot_scope.down.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, db.Exec(string(up)).Error)
		require.NoError(t, db.Exec(string(scopeUp)).Error)
		require.True(t, db.Migrator().HasColumn(&types.CodeWikiModelSlot{}, "RunID"))
		require.True(t, db.Migrator().HasColumn(&types.CodeWikiRun{}, "ExecutionEpoch"))
		require.NoError(t, db.Exec(string(scopeDown)).Error)
		require.NoError(t, db.Exec(string(down)).Error)
		require.False(t, db.Migrator().HasTable(&types.CodeWikiTopic{}))
		require.False(t, db.Migrator().HasColumn(&types.CodeWikiRun{}, "ExecutionEpoch"))
	}
}

func TestCodeWikiTopicReviewIsVersionedAndDoesNotPromoteHumanMeaning(t *testing.T) {
	repo, run := topicFixture(t, 1, 10)
	ctx := context.Background()
	topic, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "0", run.ExecutionEpoch, 0, "worker")
	require.NoError(t, err)
	topic.Status = "paused"
	topic.Checkpoint = types.JSON(`{"version":1,"items":[{"key":"role","kind":"permission","question":"What is status 1?","status":"unresolved"}]}`)
	require.NoError(t, repo.SaveTopic(ctx, topic, run.ExecutionEpoch, topic.Revision))
	_, err = repo.RefreshTopicRun(ctx, 7, "kb", run.ID, run.ExecutionEpoch)
	require.NoError(t, err)
	review := types.CodeWikiTopicReview{ID: "review", ActorID: "user", Kind: "clarification", Result: "noted", Note: "Status 1 means administrator", CreatedAt: time.Now().UTC()}
	require.Error(t, repo.AddTopicReview(ctx, 8, "kb", run.ID, "0", topic.Revision, review))
	require.Error(t, repo.AddTopicReview(ctx, 7, "kb", run.ID, "0", topic.Revision-1, review))
	require.NoError(t, repo.AddTopicReview(ctx, 7, "kb", run.ID, "0", topic.Revision, review))
	rows, err := repo.ListTopics(ctx, 7, "kb", run.ID)
	require.NoError(t, err)
	var cp types.CodeWikiTopicCheckpoint
	require.NoError(t, json.Unmarshal(rows[0].Checkpoint, &cp))
	require.Equal(t, "unresolved", cp.Items[0].Status)
	require.Empty(t, cp.Items[0].Conclusion)
	require.Len(t, cp.Reviews, 1)
	require.Equal(t, topic.Revision, cp.Reviews[0].AnalysisRevision)
	require.Equal(t, "user", cp.Reviews[0].ActorID)
}

func TestCodeWikiTopicWaitingForSharedModelDoesNotSpendBudget(t *testing.T) {
	repo, run := topicFixture(t, 1, 1)
	ctx := context.Background()
	ok, err := repo.ReserveTopicModelCall(ctx, run, "scope/unused", 3)
	require.NoError(t, err)
	require.True(t, ok)
	foreign := *run
	foreign.TenantID++
	require.Error(t, repo.RefundTopicModelCall(ctx, &foreign, "scope/unused"))
	require.NoError(t, repo.RefundTopicModelCall(ctx, run, "scope/unused"))
	require.NoError(t, repo.RefundTopicModelCall(ctx, run, "scope/unused"))
	fresh, err := repo.GetRun(ctx, "kb", run.ID)
	require.NoError(t, err)
	require.Zero(t, fresh.ModelCalls)
	ok, err = repo.ReserveTopicModelCall(ctx, run, "scope/real-call", 3)
	require.NoError(t, err)
	require.True(t, ok)
}
