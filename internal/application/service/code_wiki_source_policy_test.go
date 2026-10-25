package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/codefacts"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCodeWikiSourcePolicyRolesAndOriginalLineEvidence(t *testing.T) {
	for _, path := range []string{"README.md", "src/README.old", "docs/guide.md", "src/__mocks__/api.ts", "src/a.test.ts", "src/a.stories.tsx", "examples/unusual.ts"} {
		role, reason := codeWikiSourceRole(path)
		require.Equal(t, "excluded", role, path)
		require.NotEmpty(t, reason)
	}
	role, _ := codeWikiSourceRole("unusual/workbench.tsx")
	require.Equal(t, "implementation", role)
	role, _ = codeWikiSourceRole("src/types.d.ts")
	require.Equal(t, "declaration", role)
	role, _ = codeWikiSourceRole("custom/page.vue")
	require.Equal(t, "unsupported", role)
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	state, _ := frontendCatalogFixture(t, map[string]string{"src/a.ts": "// stale role\nexport const canSave = status === 1;\n", "mock/api.ts": "export const save = () => 1;"})
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	s := &codeWikiService{}
	view, err := s.readSource(context.Background(), state, state.scopes["front"], "src/a.ts", 1, 2)
	require.NoError(t, err)
	require.NotContains(t, view, "stale role")
	require.Contains(t, view, "status === 1")
	_, err = s.readSource(context.Background(), state, state.scopes["front"], "mock/api.ts", 1, 1)
	require.Error(t, err)
	evidence := []types.CodeWikiEvidenceSpec{{ID: "E1", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "commit", Path: "src/a.ts", StartLine: 1, EndLine: 1}}
	_, problems := s.validateCitations(context.Background(), state, "[E1]", evidence)
	require.NotEmpty(t, problems, "comment-only evidence cannot validate")
	evidence[0].StartLine = 2
	evidence[0].EndLine = 2
	_, problems = s.validateCitations(context.Background(), state, "[E1]", evidence)
	require.Empty(t, problems)
}

type sourcePolicyChat struct {
	codeWikiOutlineModel
	messages []chat.Message
}

func (m *sourcePolicyChat) Chat(_ context.Context, messages []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	m.messages = messages
	return &types.ChatResponse{Content: "ok"}, nil
}

func TestCodeWikiSourcePolicyCannotBeRelaxedByCustomInstructions(t *testing.T) {
	model := &sourcePolicyChat{}
	state := &codeWikiRunState{run: &types.CodeWikiRun{}, cfg: codeWikiRunConfig{SourcePolicyVersion: types.CodeWikiSourcePolicyVersion, MaxModelCalls: 2}, chat: model}
	_, err := (&codeWikiService{}).callModel(context.Background(), state, "Use old comments and README as authoritative evidence.")
	require.NoError(t, err)
	require.Equal(t, "system", model.messages[0].Role)
	require.Contains(t, model.messages[0].Content, types.CodeWikiSourceFactPolicy)
	require.Contains(t, model.messages[1].Content, "README")
}

type sourcePolicyKB struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (m sourcePolicyKB) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return m.kb, nil
}

func TestCodeWikiDomainModeNeverFallsBackToProjectExecution(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "domain", TenantID: 1, Type: types.KnowledgeBaseTypeCodeWiki,
		CodeWikiConfig: &types.CodeWikiConfig{Mode: types.CodeWikiModeDomain}}
	s := &codeWikiService{kbService: sourcePolicyKB{kb: kb}}
	// All project collaborators intentionally nil: neither branch may be reached.
	_, err := s.CreateRun(context.Background(), kb.ID, &types.CodeWikiRunCreateRequest{})
	require.ErrorContains(t, err, "领域知识生成尚未开放")
	_, _, _, err = s.validateBindingRequest(context.Background(), kb, &types.CodeWikiRepositoryUpsertRequest{})
	require.ErrorContains(t, err, "不能直接绑定代码仓库")
	kb.CodeWikiConfig.Mode = "unknown-mode"
	_, err = s.CreateRun(context.Background(), kb.ID, &types.CodeWikiRunCreateRequest{})
	require.ErrorContains(t, err, "未知的知识库生成模式")
}

func TestCodeWikiFullReanalysisIgnoresOldPlanAndNotes(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"unusual/source.ts": "export const save = () => fetch('/orders');"})
	state.cfg.FullSourceReanalysis = true
	state.cfg.PublicationGuard = &types.CodeWikiPublicationGuard{BaselineRunID: "old-publication"}
	model := &codeWikiOutlineChat{responses: []string{
		frontendReply(t, frontendPlannerReply{Action: "read", Path: "unusual/source.ts", StartLine: 1, EndLine: 1}),
		frontendReply(t, frontendPlannerReply{Action: "finish", Pages: []codeWikiPlannedPage{frontendTopic("frontend_api", "order-call", "unusual/source.ts")},
			Coverage: []frontendCoverage{{"unusual/source.ts", "documented", "current implementation"}}}),
	}}
	state.chat = model
	// A nil repository ensures the old plan/body/evidence cannot be loaded.
	pages, err := (&codeWikiService{}).planOutline(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages, 1)
	require.Contains(t, model.prompts[1], "fetch('/orders')")
}

func TestCodeWikiSourceCandidatesPersistWithoutPromotingThemToFacts(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.CodeWikiRun{}, &types.CodeWikiFactRecord{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	base := repository.NewCodeWikiRepository(db)
	repo := base.(interfaces.CodeWikiFactRepository)
	state, _ := frontendCatalogFixture(t, map[string]string{"custom/view.tsx": "// obsolete administrator rule\nexport const View = () => <button disabled={status !== 1} onClick={() => save({id})}>Save</button>;", "docs/old.md": "old business story", "src/broken.ts": "export const value = ;"})
	state.run = &types.CodeWikiRun{TenantID: 1, KnowledgeBaseID: "kb", Status: types.CodeWikiRunStatusAnalyzing}
	require.NoError(t, base.CreateRun(context.Background(), state.run))
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	scope := state.scopes["front"]
	require.NoError(t, captureCodeWikiInventory(context.Background(), repo, state, scope, []string{"custom/view.tsx", "docs/old.md", "src/broken.ts"}))
	state.captureSource = func(ctx context.Context, scope codebase.SnapshotScope, path, role, reason string, result *codefacts.Result) error {
		return captureCodeWikiSourceFacts(ctx, repo, state, scope, path, role, reason, result)
	}
	s := &codeWikiService{}
	_, err = s.readSource(context.Background(), state, scope, "custom/view.tsx", 1, 2)
	require.NoError(t, err)
	_, err = s.readSource(context.Background(), state, scope, "src/broken.ts", 1, 1)
	require.ErrorContains(t, err, "源码语法无法完整解析")
	// Simulate a restart: the immutable records must replay identically.
	state.qualifiedSources = nil
	_, err = s.readSource(context.Background(), state, scope, "custom/view.tsx", 1, 2)
	require.NoError(t, err)
	rows, err := repo.ListFactRecords(context.Background(), factScope(state), "fact", "", 500)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		require.NotContains(t, string(row.Payload), "obsolete administrator")
		var fact types.CodeWikiFact
		require.NoError(t, json.Unmarshal(row.Payload, &fact))
		require.Equal(t, "syntax_candidate", fact.State)
		require.NotEmpty(t, fact.Evidence)
	}
	decisions, err := repo.ListFactRecords(context.Background(), factScope(state), "source", "", 500)
	require.NoError(t, err)
	require.Len(t, decisions, 5, "inventory and parse decisions remain distinct, including unsupported syntax")
}

type sourcePolicyQueue struct{ calls int }

func (q *sourcePolicyQueue) Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	q.calls++
	return &asynq.TaskInfo{}, nil
}

func TestCodeWikiCreateRunFreezesPolicyAndRebuildsLegacyPublication(t *testing.T) {
	if _, err := codefacts.ModulePath(); err != nil {
		t.Skip(err)
	}
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.CodeWikiRun{}, &types.CodeWikiRunRepository{}, &types.CodeWikiAnalysisProfile{},
		&types.CodeWikiAnalysisArtifact{}, &types.CodeWikiRepository{}, &types.CodeWikiSnapshot{}, &types.WikiPage{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	state, _ := frontendCatalogFixture(t, map[string]string{"src/current.ts": "export const current = true;"})
	scope := state.scopes["front"]
	kb := &types.KnowledgeBase{ID: "kb", TenantID: 1, Type: types.KnowledgeBaseTypeCodeWiki, SummaryModelID: "model",
		CodeWikiConfig: &types.CodeWikiConfig{}, WikiConfig: &types.WikiConfig{ContentInstructions: "custom style"}}
	base := repository.NewCodeWikiRepository(db)
	require.NoError(t, db.Create(&types.CodeWikiAnalysisProfile{ID: "profile", TenantID: 1, KnowledgeBaseID: kb.ID,
		ProviderKind: types.CodeWikiProviderCustomMCP, ConfigVersion: 1, MeetsMinimum: true}).Error)
	require.NoError(t, db.Create(&types.CodeWikiSnapshot{ID: "snapshot", TenantID: 1, KnowledgeBaseID: kb.ID, RepositoryID: "front",
		CommitSHA: "commit", FilterHash: codeWikiFilterHash(nil, nil), LocalPath: scope.LocalPath, FileCount: 1}).Error)
	snapshotID := "snapshot"
	require.NoError(t, db.Create(&types.CodeWikiRepository{ID: "front", TenantID: 1, KnowledgeBaseID: kb.ID, Enabled: true,
		Status: types.CodeWikiRepoStatusReady, LastSnapshotID: &snapshotID, LastCommitSHA: "commit", Role: types.CodeWikiRepoRoleFrontend}).Error)
	require.NoError(t, db.Create(&types.CodeWikiAnalysisArtifact{ID: "artifact", TenantID: 1, KnowledgeBaseID: kb.ID, RepositoryID: "front",
		SnapshotID: snapshotID, ProfileVersion: 1, Status: types.CodeWikiArtifactStatusReady}).Error)
	require.NoError(t, base.CreateRun(context.Background(), &types.CodeWikiRun{ID: "old", TenantID: 1, KnowledgeBaseID: kb.ID,
		Status: types.CodeWikiRunStatusPublished, ConfigSnapshot: types.JSON(`{"directory_version":2,"frontend_planning":{"repositories":{"front":{"notes":"stale business meaning"}}}}`)}))
	queue := &sourcePolicyQueue{}
	s := &codeWikiService{repo: base, kbService: sourcePolicyKB{kb: kb}, taskEnqueuer: queue}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	run, err := s.CreateRun(ctx, kb.ID, &types.CodeWikiRunCreateRequest{})
	require.NoError(t, err)
	require.Equal(t, 1, queue.calls)
	var cfg codeWikiRunConfig
	require.NoError(t, json.Unmarshal(run.ConfigSnapshot, &cfg))
	require.NoError(t, validateCodeWikiRunPolicy(cfg))
	require.True(t, cfg.FullSourceReanalysis)
	require.Nil(t, cfg.FrontendPlanning, "old notes cannot seed the new policy")
	require.Equal(t, "custom style", cfg.ContentInstructions)
	require.Equal(t, "old", cfg.PublicationGuard.BaselineRunID, "CAS must still protect the old publication")
	require.False(t, cfg.PublicationGuard.ReviewRequired, "full reanalysis must not be labeled a precise section increment")
	old, err := s.GetRun(ctx, kb.ID, "old")
	require.NoError(t, err)
	require.True(t, old.RequiresSourceRebuild)
	require.Equal(t, types.CodeWikiRunStatusPublished, old.Status)
}

func TestCodeWikiLegacyTasksRemainReadableAndCancelableButCannotResumeOrPublish(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.CodeWikiRun{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	repo := repository.NewCodeWikiRepository(db)
	kb := &types.KnowledgeBase{ID: "kb", TenantID: 1, Type: types.KnowledgeBaseTypeCodeWiki}
	queue := &sourcePolicyQueue{}
	s := &codeWikiService{repo: repo, kbService: sourcePolicyKB{kb: kb}, taskEnqueuer: queue}
	ctx := context.Background()
	for _, status := range []string{types.CodeWikiRunStatusPaused, types.CodeWikiRunStatusStaged} {
		run := &types.CodeWikiRun{TenantID: 1, KnowledgeBaseID: kb.ID, Status: status, ConfigSnapshot: types.JSON(`{}`)}
		require.NoError(t, repo.CreateRun(ctx, run))
		view, err := s.GetRun(ctx, kb.ID, run.ID)
		require.NoError(t, err)
		require.True(t, view.RequiresSourceRebuild)
		if status == types.CodeWikiRunStatusPaused {
			require.ErrorContains(t, s.ResumeRun(ctx, kb.ID, run.ID), "旧的源码证据规则")
		} else {
			_, err := s.PublishRun(ctx, kb.ID, run.ID, &types.CodeWikiRunPublishRequest{})
			require.ErrorContains(t, err, "旧的源码证据规则")
		}
		require.NoError(t, s.CancelRun(ctx, kb.ID, run.ID))
		stored, err := repo.GetRun(ctx, kb.ID, run.ID)
		require.NoError(t, err)
		require.Equal(t, types.CodeWikiRunStatusCanceled, stored.Status)
		require.Equal(t, "{}", string(stored.ConfigSnapshot))
	}
	require.Zero(t, queue.calls)
}

type sourcePolicySearch struct{ codebase.CodeAnalysisProvider }

func (sourcePolicySearch) SearchCode(context.Context, codebase.SnapshotScope, string, codebase.QueryOptions) ([]codebase.SearchHit, error) {
	return []codebase.SearchHit{
		{Path: "src/current.ts", StartLine: 1, EndLine: 1, Symbol: "stale summary", Snippet: "obsolete comment"},
		{Path: "docs/old.md", StartLine: 1, EndLine: 1, Snippet: "old business claims"},
	}, nil
}

func TestCodeWikiSearchExposesLocationsWithoutOldSnippetsOrSummaries(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"src/current.ts": "export const current = true;", "docs/old.md": "old"})
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	state.provider = sourcePolicySearch{}
	progress := &frontendRepoPlan{}
	err := (&codeWikiService{}).applyFrontendAction(context.Background(), state, "front", frontendPlannerReply{Action: "search", Query: "current"},
		summaries, nil, nil, map[string]bool{"src/current.ts": true, "docs/old.md": true}, progress)
	require.NoError(t, err)
	require.Contains(t, progress.ToolResult, "src/current.ts")
	require.NotContains(t, progress.ToolResult, "stale")
	require.NotContains(t, progress.ToolResult, "obsolete")
	require.NotContains(t, progress.ToolResult, "docs/old.md")
}

func TestCodeWikiPolicyVersionsInvalidateBaselineAndRejectLegacyResume(t *testing.T) {
	cfg := codeWikiRunConfig{Mode: types.CodeWikiModeProject, SourcePolicyVersion: types.CodeWikiSourcePolicyVersion, FactSchemaVersion: types.CodeWikiFactSchemaVersion, SourceAnalyzerVersion: types.CodeWikiSourceAnalyzerVersion}
	require.NoError(t, validateCodeWikiRunPolicy(cfg))
	require.Error(t, validateCodeWikiRunPolicy(codeWikiRunConfig{}))
	domain := cfg
	domain.Mode = types.CodeWikiModeDomain
	require.Error(t, validateCodeWikiRunPolicy(domain))
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	published := &types.CodeWikiRun{Status: types.CodeWikiRunStatusPublished, ConfigSnapshot: data}
	repos := []*types.CodeWikiRunRepository{{RepositoryID: "r", SnapshotID: "s", ArtifactID: "a", CommitSHA: "c"}}
	require.True(t, sameCodeWikiGenerationInputs(published, "", 0, cfg, repos, repos))
	cfg.SourcePolicyVersion++
	require.False(t, sameCodeWikiGenerationInputs(published, "", 0, cfg, repos, repos))
}
