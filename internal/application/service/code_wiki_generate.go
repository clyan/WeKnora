package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/codebase"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// Code wiki generation pipeline (plan §5.1):
//
//	freeze manifest → repository analysis → cross-repo association →
//	outline planning → per-page evidence retrieval + generation →
//	citation validation → staging → [explicit publish]
//
// Every stage persists its outputs; expensive inputs (snapshots, provider
// artifacts) are prepared by sync and reused across runs, so a retry never
// re-downloads or re-indexes.

// codeWikiRunConfig is frozen into CodeWikiRun.ConfigSnapshot at run
// creation and is the ONLY source of generation inputs for the worker — a
// later config edit cannot change what an in-flight run produces.
type codeWikiRunConfig struct {
	Languages            []string                     `json:"languages,omitempty"`
	ContentInstructions  string                       `json:"content_instructions,omitempty"`
	ExtractionInstructions string                     `json:"extraction_instructions,omitempty"`
	MaxPages             int                          `json:"max_pages"`
	MaxModelCalls        int                          `json:"max_model_calls"`
	TimeoutMinutes       int                          `json:"timeout_minutes"`
	Provider             codeWikiFrozenProvider       `json:"provider"`
}

// codeWikiFrozenProvider pins the analysis profile fields a run executes
// with (plan §4.4: 活动批次固定旧配置版本，不在中途切换工具或服务地址).
type codeWikiFrozenProvider struct {
	Kind             string                         `json:"kind"`
	MCPServiceID     string                         `json:"mcp_service_id,omitempty"`
	ToolMappings     []types.CodeWikiToolMapping    `json:"tool_mappings,omitempty"`
	SnapshotDelivery string                         `json:"snapshot_delivery"`
	SharedRoot       string                         `json:"shared_root,omitempty"`
}

type codeWikiPlannedPage struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Scope    string `json:"scope"`
}

const (
	codeWikiMaxEvidencePerPage = 12
	codeWikiMaxQueriesPerPage  = 6
	codeWikiMaxContextChars    = 24000
	codeWikiManifestReadBytes  = 32 << 10
)

// code wiki manifest files mined for tech-stack/startup clues (read-only).
var codeWikiManifestFiles = []string{
	"package.json", "go.mod", "requirements.txt", "pyproject.toml",
	"Pipfile", "pom.xml", "build.gradle", "Cargo.toml", "composer.json",
	"Makefile", "docker-compose.yml", "Dockerfile", "README.md",
}

// --- Run lifecycle ----------------------------------------------------------

// CreateRun validates preconditions, freezes inputs and enqueues the
// generation task. The heavy preconditions — capability check passed, at
// least one ready artifact under the current profile version, a usable
// model — are enforced HERE so the worker starts from a known-good state.
func (s *codeWikiService) CreateRun(ctx context.Context, kbID string, req *types.CodeWikiRunCreateRequest) (*types.CodeWikiRun, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if active, err := s.repo.ActiveRun(ctx, kbID); err != nil {
		return nil, err
	} else if active != nil {
		return nil, apperrors.NewBadRequestError("another generation run is already active for this knowledge base")
	}
	profile, err := s.repo.GetAnalysisProfile(ctx, kb.TenantID, kbID)
	if err != nil {
		return nil, err
	}
	if !profile.MeetsMinimum {
		return nil, apperrors.NewBadRequestError("analysis provider capabilities have not been verified; run the capability test first")
	}

	// Model: reuse the wiki generation model settings (plan §3.1).
	var synthesisModelID string
	if kb.WikiConfig != nil {
		synthesisModelID = kb.WikiConfig.SynthesisModelID
	}
	if synthesisModelID == "" {
		synthesisModelID = kb.SummaryModelID
	}
	if synthesisModelID == "" {
		return nil, apperrors.NewBadRequestError("no generation model configured; set the wiki synthesis model or KB summary model")
	}

	// Freeze the manifest: enabled bindings with ready artifacts under the
	// CURRENT profile version. Repositories without one are recorded as
	// excluded with the concrete reason — the UI must show actual scope.
	bindings, err := s.repo.ListRepositories(ctx, types.CodeWikiRepositoryFilter{
		KnowledgeBaseID: kbID, Enabled: enabledPtr(true),
	})
	if err != nil {
		return nil, err
	}
	var entries []*types.CodeWikiRunRepository
	included := 0
	for _, binding := range bindings {
		entry := &types.CodeWikiRunRepository{
			ID:              uuid.NewString(),
			TenantID:        kb.TenantID,
			KnowledgeBaseID: kbID,
			RepositoryID:    binding.ID,
			Role:            binding.Role,
			DisplayName:     binding.DisplayName,
			CommitSHA:       binding.LastCommitSHA,
		}
		artifact, aErr := s.repo.LatestReadyArtifact(ctx, binding.ID, profile.ConfigVersion)
		switch {
		case aErr != nil || artifact == nil:
			entry.Excluded = true
			entry.ExcludeReason = "analysis data not ready under the current provider configuration; sync repositories first"
		case binding.LastSnapshotID == nil || *binding.LastSnapshotID == "":
			entry.Excluded = true
			entry.ExcludeReason = "no snapshot; sync repositories first"
		default:
			entry.SnapshotID = *binding.LastSnapshotID
			entry.ArtifactID = artifact.ID
			included++
		}
		entries = append(entries, entry)
	}
	if included == 0 {
		return nil, apperrors.NewBadRequestError("no repositories have ready analysis data; sync repositories first")
	}

	// Freeze generation inputs.
	cfg := kb.CodeWikiConfig
	if cfg == nil {
		cfg = &types.CodeWikiConfig{}
	}
	var contentInstructions, extractionInstructions string
	if kb.WikiConfig != nil {
		contentInstructions = kb.WikiConfig.ContentInstructions
		extractionInstructions = kb.WikiConfig.ExtractionInstructions
	}
	frozen := codeWikiRunConfig{
		Languages:              cfg.Languages,
		ContentInstructions:    contentInstructions,
		ExtractionInstructions: extractionInstructions,
		MaxPages:               cfg.PagesCapOrDefault(),
		MaxModelCalls:          cfg.ModelCallsCapOrDefault(),
		TimeoutMinutes:         int(cfg.TimeoutOrDefault().Minutes()),
		Provider: codeWikiFrozenProvider{
			Kind:             profile.ProviderKind,
			MCPServiceID:     profile.MCPServiceID,
			SnapshotDelivery: profile.SnapshotDelivery,
			SharedRoot:       profile.SharedRoot,
		},
	}
	if frozen.Provider.Kind == "" {
		frozen.Provider.Kind = types.CodeWikiProviderBuiltin
	}
	if mappings, mErr := profile.ParsedToolMappings(); mErr == nil {
		frozen.Provider.ToolMappings = mappings
	}
	configBlob, err := json.Marshal(frozen)
	if err != nil {
		return nil, err
	}
	var capabilitiesBlob types.JSON
	if report, rErr := profile.ParsedCapabilityCheck(); rErr == nil && report != nil {
		if b, mErr := json.Marshal(report); mErr == nil {
			capabilitiesBlob = types.JSON(b)
		}
	}

	run := &types.CodeWikiRun{
		ID:              uuid.NewString(),
		TenantID:        kb.TenantID,
		KnowledgeBaseID: kbID,
		Status:          types.CodeWikiRunStatusPending,
		ModelID:         synthesisModelID,
		ProfileVersion:  profile.ConfigVersion,
		Capabilities:    capabilitiesBlob,
		ConfigSnapshot:  types.JSON(configBlob),
		IdempotencyKey:  strings.TrimSpace(req.IdempotencyKey),
	}
	if err := s.repo.CreateRun(ctx, run); err != nil {
		if errors.Is(err, types.ErrCodeWikiRunActiveExists) {
			return nil, apperrors.NewBadRequestError("another generation run is already active for this knowledge base")
		}
		return nil, err
	}
	if err := s.repo.ReplaceRunRepositories(ctx, run.ID, entries); err != nil {
		return nil, err
	}
	payload := &types.CodeWikiGeneratePayload{
		TenantID:        kb.TenantID,
		KnowledgeBaseID: kbID,
		RunID:           run.ID,
		Initiator:       types.TaskInitiatorFromContext(ctx),
	}
	if err := s.enqueueCodeWikiTask(ctx, types.TypeCodeWikiGenerate, payload, cfg.TimeoutOrDefault()); err != nil {
		run.Status = types.CodeWikiRunStatusFailed
		run.ErrorMessage = "failed to enqueue generation task"
		now := time.Now().UTC()
		run.FinishedAt = &now
		_ = s.repo.UpdateRun(ctx, run)
		return nil, err
	}
	recordKBActivity(ctx, s.audit, kb.TenantID, kbID, types.AuditActionKBUpdated,
		"code_wiki_run", run.ID, types.AuditOutcomeAccepted,
		map[string]any{"repositories": included, "profile_version": profile.ConfigVersion})
	return run, nil
}

func (s *codeWikiService) GetRun(ctx context.Context, kbID, runID string) (*types.CodeWikiRun, error) {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return nil, err
	}
	return s.repo.GetRun(ctx, kbID, runID)
}

// GetRunDetail returns the run with its frozen manifest and staged pages.
func (s *codeWikiService) GetRunDetail(ctx context.Context, kbID, runID string) (*types.CodeWikiRun, []*types.CodeWikiRunRepository, []*types.CodeWikiRunPage, error) {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return nil, nil, nil, err
	}
	run, err := s.repo.GetRun(ctx, kbID, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	manifest, err := s.repo.ListRunRepositories(ctx, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	var pages []*types.CodeWikiRunPage
	if run.Status == types.CodeWikiRunStatusStaged ||
		run.Status == types.CodeWikiRunStatusPublished ||
		run.Status == types.CodeWikiRunStatusSuperseded {
		if pages, err = s.repo.ListRunPages(ctx, runID); err != nil {
			return nil, nil, nil, err
		}
	}
	return run, manifest, pages, nil
}

func (s *codeWikiService) ListRuns(ctx context.Context, kbID string) ([]*types.CodeWikiRun, error) {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return nil, err
	}
	return s.repo.ListRuns(ctx, kbID, 50)
}

// CancelRun flags cancellation; the worker observes the flag between stages
// and between pages. A staged run has no live worker, so it is marked
// canceled directly — discarding the candidates affects nothing published.
func (s *codeWikiService) CancelRun(ctx context.Context, kbID, runID string) error {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return err
	}
	run, err := s.repo.GetRun(ctx, kbID, runID)
	if err != nil {
		return err
	}
	if types.IsTerminalCodeWikiRunStatus(run.Status) {
		return apperrors.NewBadRequestError("run has already finished")
	}
	if run.Status == types.CodeWikiRunStatusStaged {
		now := time.Now().UTC()
		run.Status = types.CodeWikiRunStatusCanceled
		run.Phase = "已取消（候选内容已丢弃）"
		run.FinishedAt = &now
		if err := s.repo.UpdateRun(ctx, run); err != nil {
			return err
		}
		recordKBActivity(ctx, s.audit, kb.TenantID, kbID, types.AuditActionKBUpdated,
			"code_wiki_run", run.ID, types.AuditOutcomeSuccess, map[string]any{"canceled": "staged"})
		return nil
	}
	run.CancelRequested = true
	if err := s.repo.UpdateRun(ctx, run); err != nil {
		return err
	}
	recordKBActivity(ctx, s.audit, kb.TenantID, kbID, types.AuditActionKBUpdated,
		"code_wiki_run", run.ID, types.AuditOutcomeSuccess, map[string]any{"cancel_requested": true})
	return nil
}

// persistPlannedPages writes the plan as pending run-page rows so a pause (or
// worker crash) can resume the remaining pages without re-planning, and the
// UI can show per-page progress before pages complete.
func (s *codeWikiService) persistPlannedPages(ctx context.Context, run *types.CodeWikiRun, pages []codeWikiPlannedPage) {
	for _, page := range pages {
		row := &types.CodeWikiRunPage{
			ID:              uuid.NewString(),
			TenantID:        run.TenantID,
			KnowledgeBaseID: run.KnowledgeBaseID,
			RunID:           run.ID,
			PageKey:         page.Slug,
			Title:           page.Title,
			PageType:        types.WikiPageTypeSynthesis,
			Category:        page.Category,
			Status:          types.CodeWikiRunPageStatusPending,
			ValidationStatus: "pending",
			Scope:           page.Scope,
		}
		if err := s.repo.UpsertRunPage(ctx, row); err != nil {
			logger.Errorf(ctx, "code wiki: persist planned page %s failed: %v", page.Slug, err)
		}
	}
}

// PauseRun flags a pause; the worker observes it between stages/pages and
// parks the run in paused (resumable, keeps the active-run slot).
func (s *codeWikiService) PauseRun(ctx context.Context, kbID, runID string) error {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return err
	}
	run, err := s.repo.GetRun(ctx, kbID, runID)
	if err != nil {
		return err
	}
	if types.IsTerminalCodeWikiRunStatus(run.Status) || run.Status == types.CodeWikiRunStatusStaged {
		return apperrors.NewBadRequestError("run is not in a pausable state")
	}
	if run.Status == types.CodeWikiRunStatusPaused {
		return nil // idempotent
	}
	run.PauseRequested = true
	return s.repo.UpdateRun(ctx, run)
}

// ResumeRun continues a paused run: the persisted plan is picked up where it
// left off (staged pages are kept, pending pages regenerate).
func (s *codeWikiService) ResumeRun(ctx context.Context, kbID, runID string) error {
	if _, err := s.requireCodeWikiKB(ctx, kbID); err != nil {
		return err
	}
	run, err := s.repo.GetRun(ctx, kbID, runID)
	if err != nil {
		return err
	}
	if run.Status != types.CodeWikiRunStatusPaused {
		return apperrors.NewBadRequestError("run is not paused")
	}
	// Clear the pause flag BEFORE enqueueing so the worker does not observe
	// its own pause request on the resumed pass.
	run.PauseRequested = false
	if err := s.repo.UpdateRun(ctx, run); err != nil {
		return err
	}
	var cfg codeWikiRunConfig
	if err := json.Unmarshal(run.ConfigSnapshot, &cfg); err != nil {
		return fmt.Errorf("decode frozen config: %w", err)
	}
	timeout := time.Duration(cfg.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = time.Duration(types.DefaultCodeWikiRunTimeoutMinutes) * time.Minute
	}
	payload := &types.CodeWikiGeneratePayload{
		TenantID:        run.TenantID,
		KnowledgeBaseID: kbID,
		RunID:           run.ID,
		Initiator:       types.TaskInitiatorFromContext(ctx),
	}
	return s.enqueueCodeWikiTask(ctx, types.TypeCodeWikiGenerate, payload, timeout)
}

// RetryRun creates a FRESH run after a failed/canceled one. Snapshots and
// analysis artifacts are reused (the expensive stages never re-run); the
// pages regenerate under the current configuration.
func (s *codeWikiService) RetryRun(ctx context.Context, kbID, runID string) (*types.CodeWikiRun, error) {
	if _, err := s.repo.GetRun(ctx, kbID, runID); err != nil {
		return nil, err
	}
	return s.CreateRun(ctx, kbID, &types.CodeWikiRunCreateRequest{})
}

// --- Generation worker ------------------------------------------------------

type codeWikiRunState struct {
	run      *types.CodeWikiRun
	cfg      codeWikiRunConfig
	provider codebase.CodeAnalysisProvider
	builtin  *codebase.BuiltinProvider
	chat     chat.Chat
	// included repositories keyed by repository id
	scopes map[string]codebase.SnapshotScope
	// display name per repository id, for prompts and evidence rendering
	repoNames map[string]string
	// display info for prompts
	manifest []string
	// planned pages (filled after the planning stage; used for neighbor links)
	pages []codeWikiPlannedPage
	// frozen capability report for limits disclosure
	capabilities *types.CodeWikiCapabilityReport
	// interruptCheck returns errCodeWikiRunCanceled / errCodeWikiRunPaused
	// (nil to continue), observed between stages and between pages.
	interruptCheck func() error
}

// ProcessCodeWikiGenerate is the asynq handler backing TypeCodeWikiGenerate.
func (s *codeWikiService) ProcessCodeWikiGenerate(ctx context.Context, task *asynq.Task) error {
	var payload types.CodeWikiGeneratePayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return err
	}
	ctx = payload.Initiator.Apply(ctx)
	ctx = context.WithValue(ctx, types.TenantIDContextKey, payload.TenantID)

	run, err := s.repo.GetRunByID(ctx, payload.RunID)
	if err != nil || run.KnowledgeBaseID != payload.KnowledgeBaseID {
		logger.Warnf(ctx, "code wiki generate: run %s not found or KB mismatch, dropping", payload.RunID)
		return nil
	}
	if run.Status != types.CodeWikiRunStatusPending && run.Status != types.CodeWikiRunStatusPaused {
		logger.Infof(ctx, "code wiki generate: run %s already %s, dropping", run.ID, run.Status)
		return nil
	}
	owner := fmt.Sprintf("%s-%d", hostnameCodeWiki(), os.Getpid())
	acquired, err := s.repo.TryAcquireRunLease(ctx, run.ID, owner, types.CodeWikiRunLeaseTTLSeconds)
	if err != nil {
		return err
	}
	if !acquired {
		logger.Infof(ctx, "code wiki generate: run %s claimed by another worker", run.ID)
		return nil
	}
	// Reload the freshly-leased state (status/lease fields were mutated by
	// the conditional UPDATE).
	if refreshed, rErr := s.repo.GetRunByID(ctx, run.ID); rErr == nil {
		run = refreshed
	}

	// Crash/cancel safety: never leave a run stuck in a working state.
	defer s.finalizeRunOnError(ctx, run)

	if err := s.executeRun(ctx, run, payload); err != nil {
		switch {
		case errors.Is(err, errCodeWikiRunCanceled):
			// User cancellation observed between stages/pages.
			now := time.Now().UTC()
			run.Status = types.CodeWikiRunStatusCanceled
			run.Phase = "已取消"
			run.ErrorMessage = ""
			run.FinishedAt = &now
			_ = s.repo.UpdateRun(ctx, run)
			logger.Infof(ctx, "code wiki generate: run %s canceled by user", run.ID)
			return nil
		case errors.Is(err, errCodeWikiRunPaused):
			// User pause observed between pages: the plan and completed pages
			// persist; resume re-leases the run and continues.
			run.Status = types.CodeWikiRunStatusPaused
			run.Phase = fmt.Sprintf("已暂停（已完成 %d/%d 页），可继续", run.CompletedPages, run.TotalPages)
			_ = s.repo.UpdateRun(ctx, run)
			logger.Infof(ctx, "code wiki generate: run %s paused by user (%d/%d pages)",
				run.ID, run.CompletedPages, run.TotalPages)
			return nil
		}
		logger.Errorf(ctx, "code wiki generate: run %s failed: %v", run.ID, err)
		run.Status = types.CodeWikiRunStatusFailed
		run.ErrorMessage = types.FormatCodeWikiError(err)
		now := time.Now().UTC()
		run.FinishedAt = &now
		_ = s.repo.UpdateRun(ctx, run)
		// Model/pipeline hiccups are retryable by asynq ONLY if the run is
		// still pending; since we already leased it, surface the failure on
		// the run row (user-driven retry) instead of blind queue retries.
		return nil
	}
	return nil
}

// finalizeRunOnError ensures a worker crash (context cancellation / panic
// recovery is handled by asynq) doesn't strand the run mid-phase: on any
// non-terminal exit the run is marked failed so the UI never shows a
// perpetual spinner. Paused is a deliberate resting state and is left alone.
func (s *codeWikiService) finalizeRunOnError(ctx context.Context, run *types.CodeWikiRun) {
	if types.IsTerminalCodeWikiRunStatus(run.Status) || run.Status == types.CodeWikiRunStatusStaged ||
		run.Status == types.CodeWikiRunStatusPaused {
		return
	}
	refreshed, err := s.repo.GetRunByID(context.Background(), run.ID)
	if err != nil || types.IsTerminalCodeWikiRunStatus(refreshed.Status) ||
		refreshed.Status == types.CodeWikiRunStatusStaged || refreshed.Status == types.CodeWikiRunStatusPaused {
		return
	}
	if ctx.Err() != nil {
		refreshed.Status = types.CodeWikiRunStatusCanceled
		refreshed.ErrorMessage = "worker stopped before completion; retry to resume from prepared data"
	} else {
		refreshed.Status = types.CodeWikiRunStatusFailed
		refreshed.ErrorMessage = "generation stopped unexpectedly; retry to resume from prepared data"
	}
	now := time.Now().UTC()
	refreshed.FinishedAt = &now
	_ = s.repo.UpdateRun(context.Background(), refreshed)
}

// executeRun walks the pipeline stages, checking cancellation and budget
// between stages and between pages.
func (s *codeWikiService) executeRun(ctx context.Context, run *types.CodeWikiRun, payload types.CodeWikiGeneratePayload) error {
	var cfg codeWikiRunConfig
	if err := json.Unmarshal(run.ConfigSnapshot, &cfg); err != nil {
		return fmt.Errorf("decode frozen config: %w", err)
	}
	state := &codeWikiRunState{
		run:     run,
		cfg:     cfg,
		builtin: s.registry.Builtin(),
	}
	// interruptCheck observes user flags (cancel / pause) and context death,
	// returning the matching sentinel so callers transition the run cleanly.
	state.interruptCheck = func() error {
		if ctx.Err() != nil {
			return errCodeWikiRunCanceled
		}
		refreshed, err := s.repo.GetRunByID(ctx, run.ID)
		if err != nil {
			return nil
		}
		if refreshed.CancelRequested {
			return errCodeWikiRunCanceled
		}
		if refreshed.PauseRequested {
			return errCodeWikiRunPaused
		}
		return nil
	}

	// Provider from the FROZEN profile snapshot, re-resolved live so a
	// disabled service stops the run (plan §4.4).
	frozenProfile := &types.CodeWikiAnalysisProfile{
		ProviderKind:     cfg.Provider.Kind,
		MCPServiceID:     cfg.Provider.MCPServiceID,
		SnapshotDelivery: cfg.Provider.SnapshotDelivery,
		SharedRoot:       cfg.Provider.SharedRoot,
	}
	if blob, err := json.Marshal(cfg.Provider.ToolMappings); err == nil && cfg.Provider.ToolMappings != nil {
		frozenProfile.ToolMappings = types.JSON(blob)
	}
	provider, err := s.registry.Resolve(ctx, frozenProfile)
	if err != nil {
		return fmt.Errorf("analysis provider unavailable: %w", err)
	}
	state.provider = provider
	if run.Capabilities != nil {
		var report types.CodeWikiCapabilityReport
		if err := json.Unmarshal(run.Capabilities, &report); err == nil {
			state.capabilities = &report
		}
	}

	// Model.
	chatModel, err := s.modelService.GetChatModel(ctx, run.ModelID)
	if err != nil {
		return fmt.Errorf("generation model unavailable: %w", err)
	}
	state.chat = chatModel

	// Load the frozen manifest and build query scopes.
	if err := s.buildRunScopes(ctx, state); err != nil {
		return err
	}
	if err := state.interruptCheck(); err != nil {
		return err
	}

	// Stage: analysis (structure + manifests; cross-repo handled during
	// evidence retrieval).
	s.setRunPhase(ctx, run, types.CodeWikiRunStatusAnalyzing, "分析仓库结构")
	repoSummaries, err := s.analyzeRepositories(ctx, state)
	if err != nil {
		return err
	}
	if err := state.interruptCheck(); err != nil {
		return err
	}

	// Stage: outline planning — or resume from the persisted plan when the
	// run already carries page rows (a previous pass paused after planning).
	lang := codeWikiRunLanguage(ctx, cfg)
	var pages, pagesToGenerate []codeWikiPlannedPage
	persisted, err := s.repo.ListRunPages(ctx, run.ID)
	if err != nil {
		return err
	}
	if len(persisted) > 0 {
		// Resume: staged results from the earlier pass are kept as-is; only
		// pending pages still need generation.
		for _, p := range persisted {
			planned := codeWikiPlannedPage{Slug: p.PageKey, Title: p.Title, Category: p.Category, Scope: p.Scope}
			pages = append(pages, planned)
			if p.Status == types.CodeWikiRunPageStatusPending {
				pagesToGenerate = append(pagesToGenerate, planned)
			}
		}
		s.setRunPhase(ctx, run, types.CodeWikiRunStatusGenerating,
			fmt.Sprintf("继续生成（剩余 %d/%d 页）", len(pagesToGenerate), len(pages)))
	} else {
		s.setRunPhase(ctx, run, types.CodeWikiRunStatusPlanning, "规划文档目录")
		pages, err = s.planOutline(ctx, state, repoSummaries, lang)
		if err != nil {
			return err
		}
		pagesToGenerate = pages
		// Persist the plan as pending rows: pause/resume (and crash recovery)
		// continue the remaining pages without re-planning, and the UI shows
		// per-page progress before pages complete.
		s.persistPlannedPages(ctx, run, pages)
	}
	state.pages = pages
	run.TotalPages = len(pages)
	_ = s.repo.UpdateRun(ctx, run)
	if err := state.interruptCheck(); err != nil {
		return err
	}

	// Stage: per-page evidence + generation + validation.
	if len(persisted) == 0 {
		s.setRunPhase(ctx, run, types.CodeWikiRunStatusGenerating, fmt.Sprintf("生成文档（共 %d 页）", len(pages)))
	}
	for _, page := range pagesToGenerate {
		if err := state.interruptCheck(); err != nil {
			return err
		}
		if run.ModelCalls >= cfg.MaxModelCalls {
			run.Phase = "已达到本轮模型调用上限，剩余页面跳过"
			_ = s.repo.UpdateRun(ctx, run)
			break
		}
		s.generateOnePage(ctx, state, page, repoSummaries, lang)
		_ = s.repo.UpdateRun(ctx, run)
	}

	// Stage: done — staged awaiting explicit publish. A run with zero
	// completed pages fails instead: staging it would surface a publish
	// button that can only error ("run has no publishable pages") and,
	// being staged, it could not even be canceled.
	now := time.Now().UTC()
	run.FinishedAt = &now
	if run.CompletedPages == 0 {
		run.Status = types.CodeWikiRunStatusFailed
		run.Phase = "生成失败"
		if run.ErrorMessage == "" {
			run.ErrorMessage = fmt.Sprintf(
				"all %d planned pages failed or were skipped; open the page list for per-page reasons (failed=%d)",
				run.TotalPages, run.FailedPages)
		}
		if err := s.repo.UpdateRun(ctx, run); err != nil {
			return err
		}
		logger.Errorf(ctx, "code wiki generate: run %s failed (%d/%d pages produced)", run.ID, run.CompletedPages, run.TotalPages)
		return nil
	}
	run.Status = types.CodeWikiRunStatusStaged
	run.Phase = "等待发布"
	if err := s.repo.UpdateRun(ctx, run); err != nil {
		return err
	}
	logger.Infof(ctx, "code wiki generate: run %s staged (%d/%d pages, %d model calls)",
		run.ID, run.CompletedPages, run.TotalPages, run.ModelCalls)
	recordKBActivity(ctx, s.audit, run.TenantID, run.KnowledgeBaseID, types.AuditActionKBUpdated,
		"code_wiki_run", run.ID, types.AuditOutcomeSuccess,
		map[string]any{"pages": run.CompletedPages, "failed": run.FailedPages})
	return nil
}

var (
	errCodeWikiRunCanceled = errors.New("code wiki run canceled")
	errCodeWikiRunPaused   = errors.New("code wiki run paused")
)

func (s *codeWikiService) setRunPhase(ctx context.Context, run *types.CodeWikiRun, status, phase string) {
	run.Status = status
	run.Phase = phase
	_ = s.repo.UpdateRun(ctx, run)
}

// buildRunScopes loads the frozen manifest entries and their snapshots into
// provider query scopes. Excluded entries are kept in the manifest for
// disclosure but produce no scope.
func (s *codeWikiService) buildRunScopes(ctx context.Context, state *codeWikiRunState) error {
	entries, err := s.repo.ListRunRepositories(ctx, state.run.ID)
	if err != nil {
		return err
	}
	snapshotIDs := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.Excluded && e.SnapshotID != "" {
			snapshotIDs = append(snapshotIDs, e.SnapshotID)
		}
	}
	snapshots, err := s.repo.ListSnapshotsByIDs(ctx, state.run.KnowledgeBaseID, snapshotIDs)
	if err != nil {
		return err
	}
	snapByID := make(map[string]*types.CodeWikiSnapshot, len(snapshots))
	for _, snap := range snapshots {
		snapByID[snap.ID] = snap
	}
	artifactIDs := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.Excluded && e.ArtifactID != "" {
			artifactIDs = append(artifactIDs, e.ArtifactID)
		}
	}
	artifacts, err := s.repo.ListArtifactsByIDs(ctx, state.run.KnowledgeBaseID, artifactIDs)
	if err != nil {
		return err
	}
	handleByRepo := make(map[string]string, len(artifacts))
	for _, a := range artifacts {
		handleByRepo[a.RepositoryID] = a.ProviderHandle
	}
	state.scopes = make(map[string]codebase.SnapshotScope, len(entries))
	state.repoNames = make(map[string]string, len(entries))
	for _, entry := range entries {
		state.repoNames[entry.RepositoryID] = entry.DisplayName
		if entry.Excluded {
			state.manifest = append(state.manifest,
				fmt.Sprintf("- %s (%s): EXCLUDED — %s", entry.DisplayName, entry.Role, entry.ExcludeReason))
			continue
		}
		snap, ok := snapByID[entry.SnapshotID]
		if !ok {
			return fmt.Errorf("snapshot %s missing for repository %s", entry.SnapshotID, entry.DisplayName)
		}
		state.scopes[entry.RepositoryID] = codebase.SnapshotScope{
			KnowledgeBaseID: state.run.KnowledgeBaseID,
			SnapshotID:      snap.ID,
			RepositoryID:    entry.RepositoryID,
			CommitSHA:       snap.CommitSHA,
			FilterHash:      snap.FilterHash,
			LocalPath:       snap.LocalPath,
			ArtifactHandle:  handleByRepo[entry.RepositoryID],
		}
		state.manifest = append(state.manifest, fmt.Sprintf(
			"- %s (role=%s, commit=%s, files=%d)", entry.DisplayName, entry.Role, shortSHA(snap.CommitSHA), snap.FileCount))
	}
	return nil
}

// codeWikiRepoSummary is the per-repository analysis digest used by the
// planning and generation prompts.
type codeWikiRepoSummary struct {
	Repo      string
	Role      string
	Commit    string
	TopDirs   []string
	Manifests string
}

func (s *codeWikiService) analyzeRepositories(ctx context.Context, state *codeWikiRunState) (map[string]*codeWikiRepoSummary, error) {
	entries, err := s.repo.ListRunRepositories(ctx, state.run.ID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*codeWikiRepoSummary, len(entries))
	for _, entry := range entries {
		if entry.Excluded {
			continue
		}
		scope := state.scopes[entry.RepositoryID]
		summary := &codeWikiRepoSummary{
			Repo:   entry.DisplayName,
			Role:   entry.Role,
			Commit: shortSHA(scope.CommitSHA),
		}
		// Directory structure: provider tree when available, else local walk.
		entriesList, treeErr := state.provider.ListDirectory(ctx, scope, "")
		if treeErr != nil && state.builtin != nil {
			entriesList, treeErr = state.builtin.ListDirectory(ctx, scope, "")
		}
		if treeErr == nil {
			dirSeen := make(map[string]struct{})
			for _, e := range entriesList {
				if !e.IsDir {
					continue
				}
				top := strings.SplitN(e.Path, "/", 2)[0]
				if _, ok := dirSeen[top]; ok {
					continue
				}
				dirSeen[top] = struct{}{}
				summary.TopDirs = append(summary.TopDirs, top)
				if len(summary.TopDirs) >= 15 {
					break
				}
			}
		}
		// Manifest files from the LOCAL snapshot (read-only, bounded).
		var manifestLines []string
		for _, name := range codeWikiManifestFiles {
			content := readSnapshotFileBounded(scope.LocalPath, name, 4<<10)
			if content == "" {
				continue
			}
			manifestLines = append(manifestLines, fmt.Sprintf("[%s]\n%s", name, content))
			if len(manifestLines) >= 5 {
				break
			}
		}
		summary.Manifests = strings.Join(manifestLines, "\n\n")
		out[entry.RepositoryID] = summary
	}
	return out, nil
}

// planOutline asks the model for the page list; a model failure degrades to
// the default outline rather than failing the run.
func (s *codeWikiService) planOutline(ctx context.Context, state *codeWikiRunState, summaries map[string]*codeWikiRepoSummary, lang string) ([]codeWikiPlannedPage, error) {
	var repoBlocks []string
	for repoID, summary := range summaries {
		repoBlocks = append(repoBlocks, fmt.Sprintf("repo_id=%s\nname=%s\nrole=%s\ncommit=%s\ntop_dirs=%s\nmanifests:\n%s",
			repoID, summary.Repo, summary.Role, summary.Commit, strings.Join(summary.TopDirs, ", "), summary.Manifests))
	}
	prompt := strings.NewReplacer(
		"{{.Repos}}", strings.Join(repoBlocks, "\n\n"),
		"{{.DefaultToc}}", agent.CodeWikiDefaultToc,
		"{{.ContentInstructions}}", orDefault(state.cfg.ContentInstructions, "无"),
		"{{.ExtractionInstructions}}", orDefault(state.cfg.ExtractionInstructions, "无"),
		"{{.MaxPages}}", fmt.Sprint(state.cfg.MaxPages),
		"{{.Language}}", lang,
	).Replace(agent.CodeWikiOutlinePrompt)

	content, err := s.callModel(ctx, state, prompt)
	if err != nil {
		logger.Warnf(ctx, "code wiki generate: outline planning failed (%v); using default outline", err)
		return defaultCodeWikiOutline(summaries), nil
	}
	var parsed struct {
		Pages []codeWikiPlannedPage `json:"pages"`
	}
	if err := json.Unmarshal([]byte(extractJSONBlock(content)), &parsed); err != nil || len(parsed.Pages) == 0 {
		logger.Warnf(ctx, "code wiki generate: outline JSON invalid; using default outline")
		return defaultCodeWikiOutline(summaries), nil
	}
	// Sanitize: valid category, unique non-empty slug, page cap.
	seen := make(map[string]struct{})
	pages := make([]codeWikiPlannedPage, 0, len(parsed.Pages))
	for _, p := range parsed.Pages {
		p.Slug = codeWikiPageSlug(p.Slug, p.Title)
		if p.Slug == "" {
			continue
		}
		if _, dup := seen[p.Slug]; dup {
			continue
		}
		if !types.IsValidCodeWikiPageCategory(p.Category) {
			p.Category = types.CodeWikiPageCategoryAnalysis
		}
		seen[p.Slug] = struct{}{}
		pages = append(pages, p)
		if len(pages) >= state.cfg.MaxPages {
			break
		}
	}
	if len(pages) == 0 {
		return defaultCodeWikiOutline(summaries), nil
	}
	return pages, nil
}

// defaultCodeWikiOutline builds the fallback plan §5.2 outline: fixed
// project-level chapters plus one page per included repository.
func defaultCodeWikiOutline(summaries map[string]*codeWikiRepoSummary) []codeWikiPlannedPage {
	pages := []codeWikiPlannedPage{
		{Slug: "project-overview", Title: "项目概览", Category: types.CodeWikiPageCategoryOverview, Scope: "项目说明、仓库清单、技术栈、系统边界"},
		{Slug: "system-architecture", Title: "系统架构", Category: types.CodeWikiPageCategoryArchitecture, Scope: "前端、网关、后端、公共服务及依赖关系"},
		{Slug: "frontend-backend-interaction", Title: "前后端交互", Category: types.CodeWikiPageCategoryInteraction, Scope: "API 对照、认证过程、请求流向"},
		{Slug: "devops-guide", Title: "开发运维", Category: types.CodeWikiPageCategoryDevOps, Scope: "启动步骤、配置说明、构建和部署"},
		{Slug: "analysis-notes", Title: "分析说明", Category: types.CodeWikiPageCategoryAnalysis, Scope: "快照清单、覆盖范围、未确认关联"},
	}
	slugs := make([]string, 0, len(pages))
	for _, p := range pages {
		slugs = append(slugs, p.Slug)
	}
	for _, summary := range summaries {
		pages = append(pages, codeWikiPlannedPage{
			Slug:     "repo-" + codeWikiPageSlug(summary.Repo, ""),
			Title:    "仓库：" + summary.Repo,
			Category: types.CodeWikiPageCategoryRepository,
			Scope:    "目录结构、模块职责、入口、公开接口、依赖",
		})
	}
	return pages
}

// generateOnePage retrieves evidence, generates content, validates
// citations and stages the candidate page.
func (s *codeWikiService) generateOnePage(ctx context.Context, state *codeWikiRunState, page codeWikiPlannedPage, summaries map[string]*codeWikiRepoSummary, lang string) {
	run := state.run
	entryRepos := make([]string, 0, len(state.scopes))
	for repoID := range state.scopes {
		entryRepos = append(entryRepos, repoID)
	}

	// 1) Evidence retrieval: searches scoped to the frozen artifacts.
	evidence := s.retrieveEvidence(ctx, state, page)
	if len(evidence) == 0 && page.Category != types.CodeWikiPageCategoryAnalysis {
		run.FailedPages++
		s.stageRunPage(ctx, run, page, "", evidence, "no code evidence found for this page's scope; page skipped")
		return
	}

	// 2) Generation with the evidence protocol.
	evidenceBlock := renderCodeWikiEvidence(state, evidence)
	neighborSlugs := make([]string, 0, len(state.pages))
	for _, p := range state.pages {
		if p.Slug != page.Slug {
			neighborSlugs = append(neighborSlugs, p.Slug)
		}
	}
	prompt := strings.NewReplacer(
		"{{.PageTitle}}", page.Title,
		"{{.Category}}", page.Category,
		"{{.Scope}}", page.Scope,
		"{{.ProjectContext}}", strings.Join(append(state.manifest, "pages: "+strings.Join(neighborSlugs, ", ")), "\n"),
		"{{.Evidence}}", evidenceBlock,
		"{{.ContentInstructions}}", orDefault(state.cfg.ContentInstructions, "清晰、面向工程师，先结论后细节"),
		"{{.KnownLimits}}", codeWikiKnownLimits(state),
		"{{.Language}}", lang,
	).Replace(agent.CodeWikiPagePrompt)

	content, err := s.callModel(ctx, state, prompt)
	if err != nil {
		run.FailedPages++
		s.stageRunPage(ctx, run, page, "", evidence, "generation failed: "+types.FormatCodeWikiError(err))
		return
	}

	// 3) Citation validation against the frozen snapshots.
	content, validationErrors := s.validateCitations(ctx, state, content, evidence)
	if len(validationErrors) > 0 && page.Category != types.CodeWikiPageCategoryAnalysis {
		// Dropped citations are fine; a page left with zero citations on a
		// code page means the model invented its support — reject it.
		if !contentCitesEvidence(content, evidence) {
			run.FailedPages++
			s.stageRunPage(ctx, run, page, content, evidence,
				"no valid citations remained after validation: "+strings.Join(validationErrors, "; "))
			return
		}
	}
	if len(validationErrors) > 0 {
		content += "\n\n> 注：部分引用未能通过校验并被移除（" + strings.Join(validationErrors, "；") + "）。"
	}

	run.CompletedPages++
	s.stageRunPage(ctx, run, page, content, evidence, "")
}

// retrieveEvidence runs bounded, scoped searches across the run's frozen
// repositories and assembles the pre-assigned evidence list.
func (s *codeWikiService) retrieveEvidence(ctx context.Context, state *codeWikiRunState, page codeWikiPlannedPage) []types.CodeWikiEvidenceSpec {
	queries := codeWikiEvidenceQueries(page)
	var specs []types.CodeWikiEvidenceSpec
	repoOrder := make([]string, 0, len(state.scopes))
	for repoID := range state.scopes {
		repoOrder = append(repoOrder, repoID)
	}
	sort.Strings(repoOrder)

	perRepo := codeWikiMaxEvidencePerPage / maxInt(len(repoOrder), 1)
	if perRepo < 1 {
		perRepo = 1
	}
	for _, repoID := range repoOrder {
		scope := state.scopes[repoID]
		collected := 0
		for _, query := range queries {
			if collected >= perRepo || len(specs) >= codeWikiMaxEvidencePerPage {
				break
			}
			hits, err := state.provider.SearchCode(ctx, scope, query, codebase.QueryOptions{Limit: 4})
			if err != nil {
				if !errors.Is(err, codebase.ErrUnsupported) {
					logger.Warnf(ctx, "code wiki: search %q on %s failed: %v", query, scope.RepositoryID, err)
				}
				continue
			}
			for _, hit := range hits {
				if collected >= perRepo || len(specs) >= codeWikiMaxEvidencePerPage {
					break
				}
				if hit.Path == "" {
					continue
				}
				if hit.StartLine <= 0 {
					hit.StartLine = 1
				}
				if hit.EndLine < hit.StartLine {
					hit.EndLine = hit.StartLine
				}
				quote := hit.Snippet
				if quote == "" {
					if snippet, sErr := s.readSource(ctx, state, scope, hit.Path, hit.StartLine, hit.EndLine); sErr == nil {
						quote = snippet
					}
				}
				specs = append(specs, types.CodeWikiEvidenceSpec{
					ID:           fmt.Sprintf("E%d", len(specs)+1),
					RepositoryID: repoID,
					SnapshotID:   scope.SnapshotID,
					CommitSHA:    scope.CommitSHA,
					Path:         hit.Path,
					StartLine:    hit.StartLine,
					EndLine:      hit.EndLine,
					Symbol:       hit.Symbol,
					Type:         types.CodeWikiEvidenceTypeFile,
					Quote:        truncateCodeWikiString(quote, 400),
				})
				collected++
			}
		}
	}
	return specs
}

// codeWikiEvidenceQueries derives bounded search terms from the page scope.
func codeWikiEvidenceQueries(page codeWikiPlannedPage) []string {
	base := []string{page.Title, page.Scope}
	switch page.Category {
	case types.CodeWikiPageCategoryInteraction:
		base = append(base, "router", "api", "axios fetch request")
	case types.CodeWikiPageCategoryDevOps:
		base = append(base, "Dockerfile", "docker-compose", "make", "config")
	case types.CodeWikiPageCategoryArchitecture:
		base = append(base, "main entry", "server", "app")
	}
	if len(base) > codeWikiMaxQueriesPerPage {
		base = base[:codeWikiMaxQueriesPerPage]
	}
	return base
}

// readSource reads an excerpt through the provider when mapped, falling
// back to WeKnora's local snapshot copy — the same bytes the snapshot froze.
func (s *codeWikiService) readSource(ctx context.Context, state *codeWikiRunState, scope codebase.SnapshotScope, path string, start, end int) (string, error) {
	if content, err := state.provider.ReadSource(ctx, scope, path, start, end); err == nil && content != "" {
		return content, nil
	}
	if state.builtin != nil {
		return state.builtin.ReadSource(ctx, scope, path, start, end)
	}
	return "", codebase.ErrUnsupported
}

var codeWikiCitationRegex = regexp.MustCompile(`\[E(\d+)\]`)

// validateCitations verifies every cited evidence ID against the frozen
// snapshot: the evidence must exist, the file must exist locally, and the
// line range must be inside the file. Invalid citations are stripped from
// the content; the reasons are returned for disclosure.
func (s *codeWikiService) validateCitations(ctx context.Context, state *codeWikiRunState, content string, evidence []types.CodeWikiEvidenceSpec) (string, []string) {
	byID := make(map[string]*types.CodeWikiEvidenceSpec, len(evidence))
	for i := range evidence {
		byID[evidence[i].ID] = &evidence[i]
	}
	valid := make(map[string]bool, len(evidence))
	var problems []string
	for _, spec := range evidence {
		scope, ok := state.scopes[spec.RepositoryID]
		if !ok {
			valid[spec.ID] = false
			problems = append(problems, spec.ID+" 引用的仓库不在本轮清单内")
			continue
		}
		if err := verifySnapshotFileLines(scope.LocalPath, spec.Path, spec.StartLine, spec.EndLine); err != nil {
			valid[spec.ID] = false
			problems = append(problems, fmt.Sprintf("%s %s: %v", spec.ID, spec.Path, err))
			continue
		}
		valid[spec.ID] = true
	}
	cleaned := codeWikiCitationRegex.ReplaceAllStringFunc(content, func(match string) string {
		if valid["E"+strings.TrimSuffix(strings.TrimPrefix(match, "[E"), "]")] {
			return match
		}
		return ""
	})
	return cleaned, problems
}

func contentCitesEvidence(content string, evidence []types.CodeWikiEvidenceSpec) bool {
	byID := make(map[string]struct{}, len(evidence))
	for _, spec := range evidence {
		byID[spec.ID] = struct{}{}
	}
	for _, match := range codeWikiCitationRegex.FindAllString(content, -1) {
		if _, ok := byID[match[1:len(match)-1]]; ok {
			return true
		}
	}
	return false
}

// verifySnapshotFileLines checks a cited path/line range against the local
// snapshot tree.
func verifySnapshotFileLines(snapshotDir, relPath string, start, end int) error {
	if snapshotDir == "" {
		return fmt.Errorf("snapshot unavailable")
	}
	clean := filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(relPath, "/")))
	target := filepath.Join(snapshotDir, clean)
	if !strings.HasPrefix(target, filepath.Clean(snapshotDir)+string(os.PathSeparator)) {
		return fmt.Errorf("path escapes snapshot")
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		return fmt.Errorf("file not found in snapshot")
	}
	if start < 1 {
		start = 1
	}
	// Cheap line-count bound: files over 2MB are rejected from citation.
	if info.Size() > 2<<20 {
		return fmt.Errorf("file too large to verify line numbers")
	}
	data, err := os.ReadFile(target) //nolint:gosec // confined to snapshot dir
	if err != nil {
		return err
	}
	lineCount := strings.Count(string(data), "\n") + 1
	if start > lineCount {
		return fmt.Errorf("start line %d beyond file length %d", start, lineCount)
	}
	return nil
}

// stageRunPage persists a candidate (success or failure) on the run.
func (s *codeWikiService) stageRunPage(ctx context.Context, run *types.CodeWikiRun, page codeWikiPlannedPage, content string, evidence []types.CodeWikiEvidenceSpec, genErr string) {
	baseline, baselineSource := s.pageBaseline(ctx, run.KnowledgeBaseID, page.Slug)
	candidate := &types.CodeWikiRunPage{
		ID:                     uuid.NewString(),
		TenantID:               run.TenantID,
		KnowledgeBaseID:        run.KnowledgeBaseID,
		RunID:                  run.ID,
		PageKey:                page.Slug,
		Title:                  page.Title,
		PageType:               types.WikiPageTypeSynthesis,
		Category:               page.Category,
		Content:                content,
		BaselineVersion:        baseline,
		BaselineLastEditSource: baselineSource,
		Status:                 types.CodeWikiRunPageStatusStaged,
		GenerationError:        genErr,
	}
	if blob, err := json.Marshal(evidence); err == nil {
		candidate.Evidence = types.JSON(blob)
	}
	if genErr != "" {
		candidate.ValidationStatus = "failed"
	} else {
		candidate.ValidationStatus = "passed"
	}
	if err := s.repo.UpsertRunPage(ctx, candidate); err != nil {
		logger.Errorf(ctx, "code wiki: stage page %s failed: %v", page.Slug, err)
	}
}

// pageBaseline records the live page version at generation time; publish
// compares against it to protect human edits.
func (s *codeWikiService) pageBaseline(ctx context.Context, kbID, slug string) (int, string) {
	live, err := s.wikiService.GetPageBySlug(ctx, kbID, slug)
	if err != nil || live == nil {
		return 0, ""
	}
	return live.Version, live.LastEditSource
}

// callModel performs one bounded LLM call under the run's budget. Rate
// limits (429) are retried with jittered exponential backoff — provider
// SDK errors surface as strings, so the status is detected textually and
// Retry-After cannot be honored directly (plan §5.5 acknowledges serial
// per-run calls do not eliminate account-level 429; this bounds the
// damage instead of failing the run).
func (s *codeWikiService) callModel(ctx context.Context, state *codeWikiRunState, prompt string) (string, error) {
	if len(prompt) > codeWikiMaxContextChars*2 {
		prompt = prompt[:codeWikiMaxContextChars*2]
	}
	messages := []chat.Message{
		{Role: "system", Content: "You are a precise technical documentation generator. Follow the evidence protocol strictly."},
		{Role: "user", Content: prompt},
	}
	var lastErr error
	for attempt := 0; attempt <= codeWikiMaxRateLimitRetries; attempt++ {
		if codeWikiSleepRespectingCancel(ctx, codeWikiRateLimitDelay(attempt)) {
			return "", context.Canceled
		}
		resp, err := state.chat.Chat(ctx, messages, &chat.ChatOptions{Temperature: 0.2})
		if err == nil {
			state.run.ModelCalls++
			state.run.TokensUsed += int64(resp.Usage.TotalTokens)
			return resp.Content, nil
		}
		lastErr = err
		if !isCodeWikiRateLimitError(err) || ctx.Err() != nil {
			return "", err
		}
		logger.Warnf(ctx, "code wiki generate: model rate limited (attempt %d/%d): %v",
			attempt+1, codeWikiMaxRateLimitRetries+1, err)
		// Surface the wait in the run phase so the UI shows "waiting for
		// quota" instead of a stuck spinner.
		phase := state.run.Phase
		state.run.Phase = "等待模型额度（429 退避中）"
		_ = s.repo.UpdateRun(ctx, state.run)
		state.run.Phase = phase
	}
	return "", lastErr
}

const (
	// codeWikiMaxRateLimitRetries bounds 429 retries per model call.
	codeWikiMaxRateLimitRetries = 3
	// codeWikiRateLimitBaseDelay is the first backoff step.
	codeWikiRateLimitBaseDelay = 5 * time.Second
)

// codeWikiRateLimitDelay returns the backoff for one attempt with ±30%
// jitter (attempt 0 waits before the first call only when a prior loop
// iteration flagged a rate limit — the sleep is skipped via zero delay).
func codeWikiRateLimitDelay(attempt int) time.Duration {
	if attempt == 0 {
		return 0
	}
	base := codeWikiRateLimitBaseDelay * (1 << (attempt - 1)) // 5s, 10s, 20s
	if base > 2*time.Minute {
		base = 2 * time.Minute
	}
	jitter := base / 3
	return base - jitter + time.Duration(rand.Int63n(int64(2*jitter)+1))
}

// codeWikiSleepRespectingCancel sleeps for d, returning true when the
// context was cancelled first.
func codeWikiSleepRespectingCancel(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

// isCodeWikiRateLimitError recognizes provider rate-limit failures from
// their stringified errors (status code, marker words).
func isCodeWikiRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "429") || strings.Contains(msg, "too many requests") {
		return true
	}
	return strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate_limit") ||
		strings.Contains(msg, "quota exceeded")
}

// --- helpers ----------------------------------------------------------------

func codeWikiRunLanguage(ctx context.Context, cfg codeWikiRunConfig) string {
	if len(cfg.Languages) > 0 && cfg.Languages[0] != "" {
		return cfg.Languages[0]
	}
	return types.LanguageNameFromContext(ctx)
}

func renderCodeWikiEvidence(state *codeWikiRunState, evidence []types.CodeWikiEvidenceSpec) string {
	if len(evidence) == 0 {
		return "(no evidence retrieved — describe only what the repository manifest shows and mark the page as low-coverage)"
	}
	lines := make([]string, 0, len(evidence))
	for _, spec := range evidence {
		name := state.repoNames[spec.RepositoryID]
		if name == "" {
			name = shortSHA(spec.RepositoryID)
		}
		lines = append(lines, fmt.Sprintf(agent.CodeWikiEvidenceLine,
			spec.ID, spec.Path, spec.StartLine, spec.EndLine, name, shortSHA(spec.CommitSHA), orDefault(spec.Quote, "")))
	}
	return strings.Join(lines, "\n")
}

func codeWikiKnownLimits(state *codeWikiRunState) string {
	if state.capabilities == nil {
		return "分析能力未记录：调用关系仅在有证据时描述。"
	}
	var notes []string
	if !state.capabilities.HasCapability(types.CodeWikiCapCallGraph) {
		notes = append(notes, "无调用图能力：仅当两侧代码都有证据时才描述调用关系")
	}
	if !state.capabilities.HasCapability(types.CodeWikiCapCrossRepo) {
		notes = append(notes, "无原生跨仓库分析：跨仓库关联由逐仓库检索拼接，未确认的链路须标注")
	}
	if !state.capabilities.HasCapability(types.CodeWikiCapSymbols) {
		notes = append(notes, "无符号解析：结构描述以目录和文件为据")
	}
	notes = append(notes, "所有引用必须指向快照中的固定 commit；无法确认的关系必须标注为未确认")
	return strings.Join(notes, "；")
}

func codeWikiPageSlug(slug, title string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	slug = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return '-'
		case r == ' ':
			return '-'
		default:
			return -1
		}
	}, slug)
	slug = strings.Trim(slug, "-")
	if slug == "" && title != "" {
		return codeWikiPageSlug(title, "")
	}
	if len(slug) > 120 {
		slug = slug[:120]
	}
	return slug
}

func readSnapshotFileBounded(snapshotDir, rel string, max int64) string {
	if snapshotDir == "" {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(rel, "/")))
	target := filepath.Join(snapshotDir, clean)
	if !strings.HasPrefix(target, filepath.Clean(snapshotDir)+string(os.PathSeparator)) {
		return ""
	}
	f, err := os.Open(target) //nolint:gosec // confined to snapshot dir
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := readAtMost(f, max)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readAtMost(f *os.File, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(f, max))
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func truncateCodeWikiString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func extractJSONBlock(s string) string {
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return s
	}
	return s[start : end+1]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func hostnameCodeWiki() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "worker"
	}
	return host
}
