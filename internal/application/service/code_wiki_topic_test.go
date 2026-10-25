package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/codefacts"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type topicFlowModelService struct{ interfaces.ModelService }

func (topicFlowModelService) GetModelByID(context.Context, string) (*types.Model, error) {
	return &types.Model{}, nil
}

func TestCodeWikiTopicRoundTripResumesFromOnlySavedCheckpoint(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	s, _, run, _ := topicServiceFixture(t)
	s.modelService = topicFlowModelService{}
	repo := s.repo.(interfaces.CodeWikiTopicRepository)
	state, _ := frontendCatalogFixture(t, map[string]string{
		"unusual/screen.tsx":   "// OBSOLETE_ADMIN_ONLY\nimport {submit} from './transport';\nexport function Screen({status, loading, values}) {\nreturn status === 1 && <button disabled={loading} onClick={() => submit(values)}>Save</button>;\n}",
		"unusual/transport.ts": "export async function submit(values) {\nconst payload = {...values, note: values.note ?? null, mode: 'save'};\nconst response = await fetch('/orders', {method: 'POST', body: JSON.stringify(payload)});\nif (!response.ok) throw new Error('request failed');\nreturn response.status;\n}",
	})
	state.run = run
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	state.interruptCheck = func() error { return nil }
	page := frontendTopic("frontend_page", "screen", "unusual/screen.tsx", "unusual/transport.ts")
	page.Slug = "0"
	cp := types.CodeWikiTopicCheckpoint{Version: 1}
	model := &codeWikiOutlineChat{}
	state.chat = model
	ctx := context.Background()
	round := func(action any) bool {
		t.Helper()
		encoded, err := json.Marshal(action)
		require.NoError(t, err)
		model.responses = []string{string(encoded)}
		stored, err := repo.GetTopic(ctx, 7, "kb", run.ID, "0")
		require.NoError(t, err)
		worker, err := repo.ClaimTopic(ctx, 7, "kb", run.ID, "0", run.ExecutionEpoch, stored.Revision, uuid.NewString())
		require.NoError(t, err)
		require.NotNil(t, worker)
		finished, err := s.runTopicRound(ctx, state, worker, page, &cp)
		require.NoError(t, err)
		require.Empty(t, cp.Feedback)
		encoded, err = json.Marshal(cp)
		require.NoError(t, err)
		worker.Checkpoint, worker.Status = types.JSON(encoded), "queued"
		require.NoError(t, repo.SaveTopic(ctx, worker, run.ExecutionEpoch, worker.Revision))
		// Discard the in-memory analysis between every action. Only the
		// persisted checkpoint and immutable source can feed the next request.
		cp = types.CodeWikiTopicCheckpoint{}
		stored, err = repo.GetTopic(ctx, 7, "kb", run.ID, "0")
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(stored.Checkpoint, &cp))
		return finished
	}
	require.False(t, round(types.CodeWikiTopicAction{Action: "read", RepositoryID: "front", Path: "unusual/screen.tsx", StartLine: 1, EndLine: 5}))
	require.False(t, round(types.CodeWikiTopicAction{Action: "read", RepositoryID: "front", Path: "unusual/transport.ts", StartLine: 1, EndLine: 6}))
	require.Len(t, cp.Evidence, 2)
	for _, prompt := range model.prompts {
		require.NotContains(t, prompt, "OBSOLETE_ADMIN_ONLY")
	}
	// Controlled responses test persistence/protocol, not an external model's
	// ability to discover every field or prove the semantic conclusion.
	items := append([]types.CodeWikiTopicItem(nil), cp.Items...)
	for i := range items {
		items[i].Status = "unresolved"
		items[i].Conclusion = "Controlled sample: candidate requires further independent review"
	}
	items = append(items, types.CodeWikiTopicItem{Key: "conditions", Kind: "interaction", Question: "What enables this operation?", Status: "analyzed", Conclusion: "Visibility depends on status === 1; loading disables the button [E1]", EvidenceIDs: []string{"E1"}}, types.CodeWikiTopicItem{Key: "parameters", Kind: "request", Question: "What is actually sent?", Status: "analyzed", Conclusion: "The request spreads values, then overwrites note with values.note ?? null and mode with save; POST /orders, non-ok responses throw [E2]", EvidenceIDs: []string{"E2"}})
	require.False(t, round(map[string]any{"action": "record", "items": items}))
	require.False(t, round(map[string]any{"action": "write", "sections": []types.CodeWikiTopicSection{{Title: "操作与条件", Content: items[len(items)-2].Conclusion, ItemKeys: []string{"conditions"}}}}))
	_, _, err := s.finishTopicDocument(ctx, state, page, &cp)
	require.ErrorContains(t, err, "What is actually sent?", "writing only the UI condition must not lose the request result")
	require.False(t, round(map[string]any{"action": "write", "sections": []types.CodeWikiTopicSection{{Title: "实际参数与异常", Content: items[len(items)-1].Conclusion, ItemKeys: []string{"parameters"}}}}))
	require.True(t, round(map[string]string{"action": "finish"}))
	content, _, err := s.finishTopicDocument(ctx, state, page, &cp)
	require.NoError(t, err)
	require.Contains(t, content, "status === 1")
	require.Contains(t, content, "values.note ?? null")
	require.Contains(t, content, "待澄清")
	require.NotContains(t, content, "OBSOLETE_ADMIN_ONLY")
	require.Equal(t, 6, cp.Rounds)
}

type topicRelationProvider struct {
	codebase.CodeAnalysisProvider
	edges   []codebase.RelationEdge
	err     error
	options codebase.QueryOptions
	scopes  []codebase.SnapshotScope
}

func (p *topicRelationProvider) QueryRelations(_ context.Context, scope codebase.SnapshotScope, _ string, opts codebase.QueryOptions) ([]codebase.RelationEdge, error) {
	p.options, p.scopes = opts, []codebase.SnapshotScope{scope}
	return p.edges, p.err
}

func (p *topicRelationProvider) QueryCrossRepository(_ context.Context, scopes []codebase.SnapshotScope, _ string, opts codebase.QueryOptions) ([]codebase.RelationEdge, error) {
	p.options, p.scopes = opts, scopes
	return p.edges, p.err
}

func TestCodeWikiTopicRelationsOnlyExposeAuthorizedSourceLocations(t *testing.T) {
	for _, action := range []string{"relations", "cross_relations"} {
		t.Run(action, func(t *testing.T) {
			state, _ := frontendCatalogFixture(t, nil)
			provider := &topicRelationProvider{edges: []codebase.RelationEdge{
				{FromRepositoryID: "front", FromPath: "screen.tsx", ToRepositoryID: "front", ToPath: "api.ts", StartLine: 7, EndLine: 8, FromSymbol: "STALE_SUMMARY", ExternalTarget: "UNVERIFIED_URL"},
				{FromRepositoryID: "other", FromPath: "private.ts", ToRepositoryID: "front", ToPath: "api.ts"},
				{FromRepositoryID: "front", FromPath: "README.md", ToRepositoryID: "front", ToPath: "api.ts"},
				{FromRepositoryID: "front", FromPath: "screen.tsx", ToRepositoryID: "other", ToPath: "private.ts"},
			}}
			state.provider = provider
			cp := types.CodeWikiTopicCheckpoint{Version: 1}
			result, err := (&codeWikiService{}).topicToolResult(context.Background(), state, &cp, types.CodeWikiTopicAction{Action: action, RepositoryID: "front", Query: "save", Offset: 8})
			require.NoError(t, err)
			require.Equal(t, codebase.QueryOptions{Limit: 8, Offset: 8}, provider.options)
			require.Len(t, provider.scopes, 1)
			require.Contains(t, result, "screen.tsx")
			require.Contains(t, result, "api.ts")
			for _, excluded := range []string{"private.ts", "README.md", "STALE_SUMMARY", "UNVERIFIED_URL"} {
				require.NotContains(t, result, excluded)
			}
			require.Empty(t, cp.Items, "indexed locations cannot promote facts without source reads")
			require.Empty(t, cp.Evidence)
			provider.err = codebase.ErrUnsupported
			result, err = (&codeWikiService{}).topicToolResult(context.Background(), state, &cp, types.CodeWikiTopicAction{Action: action, RepositoryID: "front", Query: "save"})
			require.NoError(t, err)
			require.Contains(t, result, "search and source reads")
			require.Contains(t, result, "does not prove no callers")
		})
	}
}

type topicCaptureQueue struct {
	messages []types.CodeWikiGeneratePayload
}

func (q *topicCaptureQueue) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	var message types.CodeWikiGeneratePayload
	if err := json.Unmarshal(task.Payload(), &message); err != nil {
		return nil, err
	}
	q.messages = append(q.messages, message)
	return &asynq.TaskInfo{}, nil
}

func topicServiceFixture(t *testing.T) (*codeWikiService, *gorm.DB, *types.CodeWikiRun, *topicCaptureQueue) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.CodeWikiRun{}, &types.CodeWikiTopic{}, &types.CodeWikiModelSlot{}, &types.CodeWikiRunRepository{}, &types.CodeWikiRunPage{}, &types.CodeWikiRunEvent{}, &types.WikiPage{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	cfg := codeWikiRunConfig{TopicVersion: 1, Mode: types.CodeWikiModeProject, SourcePolicyVersion: types.CodeWikiSourcePolicyVersion, FactSchemaVersion: types.CodeWikiFactSchemaVersion, SourceAnalyzerVersion: types.CodeWikiSourceAnalyzerVersion}
	blob, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := repository.NewCodeWikiRepository(db)
	run := &types.CodeWikiRun{TenantID: 7, KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusPending, ConfigSnapshot: types.JSON(blob)}
	require.NoError(t, repo.CreateRun(context.Background(), run))
	queue := &topicCaptureQueue{}
	s := &codeWikiService{repo: repo, taskEnqueuer: queue, kbService: sourcePolicyKB{kb: &types.KnowledgeBase{ID: "kb", TenantID: 7, Type: types.KnowledgeBaseTypeCodeWiki}}}
	topics, err := s.topicRepository()
	require.NoError(t, err)
	var rows []*types.CodeWikiTopic
	for i := 0; i < 6; i++ {
		rows = append(rows, &types.CodeWikiTopic{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, TopicKey: fmt.Sprint(i), Title: "topic", Checkpoint: types.JSON(`{"version":1,"feedback":"private tool input","last_action":{"action":"search","query":"private query"},"items":[{"key":"q","kind":"question","question":"Meaning?","status":"unresolved"}]}`)})
	}
	require.NoError(t, topics.EnsureTopics(context.Background(), run, rows))
	require.NoError(t, topics.BeginTopicPass(context.Background(), run, 100, time.Hour, 3))
	return s, db, run, queue
}

func TestCodeWikiTopicDispatcherBoundsWorkAndKeepsRecoveryAlive(t *testing.T) {
	s, db, run, queue := topicServiceFixture(t)
	payload := types.CodeWikiGeneratePayload{TenantID: 7, KnowledgeBaseID: "kb", RunID: run.ID, ExecutionEpoch: run.ExecutionEpoch, Dispatch: true}
	require.NoError(t, s.processCodeWikiTopic(context.Background(), payload))
	require.Len(t, queue.messages, 4, "only three ready topics and one recovery monitor should be scheduled")
	next := queue.messages[3]
	require.True(t, next.Dispatch)
	require.NotEqual(t, payload.DispatchSequence, next.DispatchSequence, "the monitor must not deduplicate itself against the currently executing message")
	queue.messages = nil
	require.NoError(t, db.Model(&types.CodeWikiTopic{}).Where("run_id = ? AND topic_key = ?", run.ID, "0").Updates(map[string]any{"status": "running", "lease_expires_at": time.Now().UTC().Add(-time.Minute)}).Error)
	require.NoError(t, s.processCodeWikiTopic(context.Background(), next))
	require.NotEmpty(t, queue.messages)
	require.Equal(t, next.DispatchSequence+1, queue.messages[len(queue.messages)-1].DispatchSequence)
	queue.messages = nil
	payload.ExecutionEpoch--
	require.NoError(t, s.processCodeWikiTopic(context.Background(), payload))
	require.Empty(t, queue.messages, "stale dispatcher cannot revive an old execution pass")
}

func TestCodeWikiTopicQueriesHidePrivateInputsAndNeverUseModel(t *testing.T) {
	s, _, run, _ := topicServiceFixture(t)
	rows, err := s.ListRunTopics(context.Background(), "kb", run.ID)
	require.NoError(t, err)
	require.Len(t, rows, 6)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	detail, err := s.GetRunTopic(context.Background(), "kb", run.ID, "0", "items", 0)
	require.NoError(t, err)
	require.Len(t, detail.Items, 1)
	require.Equal(t, "unresolved", detail.Items[0].Status)
	encoded, err = json.Marshal(detail)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	_, err = s.GetRunTopic(context.Background(), "kb", run.ID, "0", "items", -1)
	require.Error(t, err)
	err = s.ReviewRunTopic(context.Background(), "kb", run.ID, "0", &types.CodeWikiTopicReviewRequest{Kind: "clarification", Result: "noted", Note: "human explanation"})
	require.ErrorContains(t, err, "已登录用户")
}

func TestCodeWikiTopicPagingRejectsMixedSnapshotsAndFilters(t *testing.T) {
	s, db, run, _ := topicServiceFixture(t)
	ctx := context.Background()
	first, err := s.GetRunTopicPage(ctx, "kb", run.ID, "queued", "", 2)
	require.NoError(t, err)
	require.Len(t, first.Topics, 2)
	require.Equal(t, 6, first.Total)
	require.NotEmpty(t, first.NextCursor)
	second, err := s.GetRunTopicPage(ctx, "kb", run.ID, "queued", first.NextCursor, 2)
	require.NoError(t, err)
	require.NotEqual(t, first.Topics[0].ID, second.Topics[0].ID)
	_, err = s.GetRunTopicPage(ctx, "kb", run.ID, "running", first.NextCursor, 2)
	require.Error(t, err, "cursor cannot switch its filter silently")
	_, err = s.GetRunTopicPage(ctx, "kb", run.ID, "queued", "invalid", 2)
	require.Error(t, err)
	require.NoError(t, db.Model(&types.CodeWikiTopic{}).Where("run_id = ? AND topic_key = ?", run.ID, "0").Update("status", "failed").Error)
	_, err = s.GetRunTopicPage(ctx, "kb", run.ID, "queued", first.NextCursor, 2)
	require.ErrorIs(t, err, types.ErrCodeWikiProgressChanged)
	fresh, err := s.GetRunTopicPage(ctx, "kb", run.ID, "failed", "", 2)
	require.NoError(t, err)
	require.Equal(t, 1, fresh.Total)
	require.Equal(t, 5, fresh.Counts["queued"])
	encoded, err := json.Marshal(fresh)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
}

func TestCodeWikiTopicDoesNotLoseDiscoveredOperationsAtWriting(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	state, _ := frontendCatalogFixture(t, map[string]string{"screen.tsx": "export const View = () => <button onClick={() => save({id})}>Save</button>;"})
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	s := &codeWikiService{}
	cp := types.CodeWikiTopicCheckpoint{Version: 1, Evidence: []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Path: "screen.tsx", StartLine: 1, EndLine: 1, Type: "implementation"}}}
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("operation-%d", i)
		cp.Items = append(cp.Items, types.CodeWikiTopicItem{Key: key, Kind: "interaction", Question: key, Status: "analyzed", Conclusion: "Code checked", EvidenceIDs: []string{"E1"}})
		if i < 8 {
			cp.Sections = append(cp.Sections, types.CodeWikiTopicSection{Title: key, Content: "Code checked [E1]", ItemKeys: []string{key}})
		}
	}
	cp.Evidence = append(cp.Evidence, types.CodeWikiEvidenceSpec{ID: "E2", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Path: "screen.tsx", StartLine: 1, EndLine: 1, Type: "implementation", Quote: "unused read excerpt"})
	page := codeWikiPlannedPage{Title: "Operations"}
	_, _, err := s.finishTopicDocument(context.Background(), state, page, &cp)
	require.ErrorContains(t, err, "正文遗漏已查明项目")
	for i := 8; i < 12; i++ {
		key := fmt.Sprintf("operation-%d", i)
		cp.Sections = append(cp.Sections, types.CodeWikiTopicSection{Title: key, Content: "Code checked [E1]", ItemKeys: []string{key}})
	}
	content, _, err := s.finishTopicDocument(context.Background(), state, page, &cp)
	require.NoError(t, err)
	require.Contains(t, content, "operation-11")
	body, sources, found := strings.Cut(content, "\n\n## 源码依据\n\n")
	require.True(t, found)
	require.NotContains(t, body, "[E1]")
	require.Equal(t, 1, strings.Count(sources, "[E1]"))
	require.Contains(t, sources, "screen.tsx:1-1")
	require.Contains(t, sources, "提交：`commit`")
	require.NotContains(t, sources, "[E2]")
	require.NotContains(t, sources, "unused read excerpt")
	require.Equal(t, &types.CodeWikiTopicValidation{Source: "passed", Citations: "passed", Coverage: "passed"}, cp.Validation)
	require.Equal(t, 12, topicCoverage(&cp).Documented)
	cp.Sections[0].Content = "Code checked [E1] and unknown citation [E404]"
	_, _, err = s.finishTopicDocument(context.Background(), state, page, &cp)
	require.ErrorContains(t, err, "引用检查未通过")
	require.Equal(t, "passed", cp.Validation.Source)
	require.Equal(t, "failed", cp.Validation.Citations)
	cp.Sections[0].Content = "No citation"
	_, _, err = s.finishTopicDocument(context.Background(), state, page, &cp)
	require.NoError(t, err, "正文无需内联引用，证据通过结构化关联校验")
	cp.Items[0].EvidenceIDs = nil
	_, _, err = s.finishTopicDocument(context.Background(), state, page, &cp)
	require.ErrorContains(t, err, "已查明结论必须包含源码依据")
	cp.Items[0].EvidenceIDs = []string{"E404"}
	_, _, err = s.finishTopicDocument(context.Background(), state, page, &cp)
	require.ErrorContains(t, err, "关联项目的源码依据无法核验")
}

func TestCodeWikiTopicConclusionsRequireReadEvidenceAndUnknownsStayVisible(t *testing.T) {
	cp := types.CodeWikiTopicCheckpoint{Version: 1, Evidence: []types.CodeWikiEvidenceSpec{{ID: "E1", Type: "declaration"}}}
	item := types.CodeWikiTopicItem{Key: "permission", Kind: "permission", Question: "Who can save?", Status: "analyzed", Conclusion: "Administrator", EvidenceIDs: []string{"E1"}}
	require.ErrorContains(t, mergeTopicItems(&cp, []types.CodeWikiTopicItem{item}), "类型声明")
	require.Empty(t, cp.Items)
	item.EvidenceIDs = []string{"E2"}
	require.ErrorContains(t, mergeTopicItems(&cp, []types.CodeWikiTopicItem{item}), "尚未阅读")
	item.Status, item.Conclusion, item.EvidenceIDs = "unresolved", "Only status === 1 is observed; role is unknown", nil
	require.NoError(t, mergeTopicItems(&cp, []types.CodeWikiTopicItem{item}))
	require.Equal(t, "unresolved", cp.Items[0].Status)
}

func TestCodeWikiTopicContextOverflowNeverCallsModel(t *testing.T) {
	state := &codeWikiRunState{cfg: codeWikiRunConfig{ContextWindow: 4096, MaxOutputTokens: 4096}}
	// All external collaborators are nil: overflow must save/pause before a call.
	_, err := (&codeWikiService{}).callTopicModel(context.Background(), state, &types.CodeWikiTopic{}, strings.Repeat("long input ", 1000))
	require.ErrorIs(t, err, errTopicContext)
}

func TestCodeWikiTopicSegmentedReadPreservesScopeAndDistinctActions(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	code := "export function View({status, id}) {\nif (status === 1) {\n" + strings.Repeat("\n", 600) + "return <><button onClick={() => save(id)}>Save</button><button onClick={() => remove(id)}>Remove</button></>;\n}\n}\n"
	state, _ := frontendCatalogFixture(t, map[string]string{"long.tsx": code})
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	cp := types.CodeWikiTopicCheckpoint{Version: 1}
	action := types.CodeWikiTopicAction{Action: "read", RepositoryID: "front", Path: "long.tsx", StartLine: 603, EndLine: 603}
	result, err := (&codeWikiService{}).topicToolResult(context.Background(), state, &cp, action)
	require.NoError(t, err)
	require.Contains(t, result, "status === 1")
	require.Contains(t, result, "parameters")
	require.Len(t, cp.Items, 2, "two actions on the same line are independent discovered questions")
	_, err = (&codeWikiService{}).topicToolResult(context.Background(), state, &cp, action)
	require.NoError(t, err)
	require.Len(t, cp.Items, 2, "re-reading a segment is idempotent")
	require.Len(t, cp.Evidence, 1)
}

func TestCodeWikiTopicOverviewUsesAcceptedRevisionsAndReportsGaps(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	state, _ := frontendCatalogFixture(t, map[string]string{"screen.tsx": "export const View = () => <button>Save</button>;", "api.ts": "export const save = () => fetch('/orders');"})
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	state.pages = []codeWikiPlannedPage{{Slug: "api", Title: "API", Category: "frontend_api"}, {Slug: "page", Title: "Page", Category: "frontend_page"}, {Slug: "failed", Title: "Failed", Category: "frontend_page"}}
	makeTopic := func(key, path, question string) *types.CodeWikiTopic {
		cp := types.CodeWikiTopicCheckpoint{Version: 1, Evidence: []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Path: path, StartLine: 1, EndLine: 1, Type: "implementation"}}, Items: []types.CodeWikiTopicItem{{Key: "item", Kind: "interaction", Question: question, Status: "analyzed", Conclusion: question + " [E1]", EvidenceIDs: []string{"E1"}}, {Key: "unknown", Status: "unresolved", Question: "Meaning", Conclusion: "Not established"}}}
		blob, err := json.Marshal(cp)
		require.NoError(t, err)
		return &types.CodeWikiTopic{TopicKey: key, Title: key, Revision: 7, Status: "completed", Checkpoint: types.JSON(blob)}
	}
	topics := []*types.CodeWikiTopic{makeTopic("api", "api.ts", "API fact"), makeTopic("page", "screen.tsx", "Page fact"), {TopicKey: "failed", Title: "Failed", Status: "failed"}}
	require.True(t, codeWikiOverviewReady(topics))
	cp := types.CodeWikiTopicCheckpoint{}
	s := &codeWikiService{}
	require.NoError(t, s.buildTopicOverview(context.Background(), state, &cp, topics))
	require.Len(t, cp.Items, 2)
	require.Equal(t, 7, cp.Items[1].SourceRevision)
	require.Equal(t, "Page fact [E2]", cp.Items[1].Conclusion)
	content, _, err := s.finishTopicDocument(context.Background(), state, codeWikiPlannedPage{Slug: types.CodeWikiSummaryTopicKey, Title: "Overview"}, &cp)
	require.NoError(t, err)
	require.Contains(t, content, "待澄清")
	require.Contains(t, content, "Failed：分析未完成")
	require.NotContains(t, content, "[[failed|")
	state.cfg.PublicationGuard = &types.CodeWikiPublicationGuard{ArchivePages: map[string]string{"api": "fingerprint"}}
	require.NoError(t, s.buildTopicOverview(context.Background(), state, &cp, topics))
	require.Len(t, cp.Items, 1, "archived conclusions must not be quoted in the new guide")
	require.Equal(t, "page", cp.Items[0].SourceTopicKey)
	require.NotContains(t, cp.Sections[0].Content, "[[api|")
	topics[1].Status = "paused"
	require.False(t, codeWikiOverviewReady(topics))
	topics[0].Status, topics[1].Status = "failed", "failed"
	require.ErrorContains(t, s.buildTopicOverview(context.Background(), state, &cp, topics), "没有可汇总")
}

func TestCodeWikiTopicSourceCacheSeparatesPolicyContentAndTenant(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	state, _ := frontendCatalogFixture(t, map[string]string{"screen.ts": "export const value = 1;"})
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	scope := state.scopes["front"]
	one, err := analyzeCachedCodeWikiSource(context.Background(), state, scope, "screen.ts", "// obsolete\nexport const value = 1;")
	require.NoError(t, err)
	two, err := analyzeCachedCodeWikiSource(context.Background(), state, scope, "screen.ts", "// obsolete\nexport const value = 2;")
	require.NoError(t, err)
	require.NotEqual(t, one.Source, two.Source)
	one.Kind = "mutated caller"
	three, err := analyzeCachedCodeWikiSource(context.Background(), state, scope, "screen.ts", "// obsolete\nexport const value = 1;")
	require.NoError(t, err)
	require.Equal(t, "implementation", three.Kind)
	require.NotContains(t, three.Source, "obsolete")
	state.run.TenantID = 99
	four, err := analyzeCachedCodeWikiSource(context.Background(), state, scope, "screen.ts", "// obsolete\nexport const value = 1;")
	require.NoError(t, err)
	require.Equal(t, three.Source, four.Source)
}
