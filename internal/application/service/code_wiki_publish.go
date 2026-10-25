package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/codebase"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// Code wiki publish + evidence reading (plan §5.4).
//
// Publish is user-triggered (POST runs/:id/publish), never automatic: the
// staged candidates are compared against the live pages' versions inside
// one transaction — a human edit that landed during generation is a
// conflict, and the caller chooses abort (default) or keep-human (the
// candidate is skipped, the edit stays).

// codeWikiCategoryFolder maps a code wiki category to the wiki browser
// directory breadcrumb it publishes under.
var codeWikiCategoryFolder = map[string][]string{
	types.CodeWikiPageCategoryOverview:     {"项目概览"},
	types.CodeWikiPageCategoryArchitecture: {"系统架构"},
	types.CodeWikiPageCategoryInteraction:  {"系统架构", "前后端交互"},
	types.CodeWikiPageCategoryWorkflow:     {"业务流程"},
	types.CodeWikiPageCategoryRepository:   {"仓库说明"},
	types.CodeWikiPageCategoryDevOps:       {"开发运维"},
	types.CodeWikiPageCategoryAnalysis:     {"分析说明"},
}

func codeWikiFoldersForRun(run *types.CodeWikiRun) (map[string][]string, error) {
	var cfg codeWikiRunConfig
	if err := json.Unmarshal(run.ConfigSnapshot, &cfg); err != nil {
		return nil, fmt.Errorf("invalid frozen generation config: %w", err)
	}
	if cfg.DirectoryVersion == 0 {
		return codeWikiCategoryFolder, nil
	}
	if cfg.DirectoryVersion == types.CodeWikiDirectoryVersionFrontend {
		folders := make(map[string][]string)
		for _, category := range types.CodeWikiFrontendCategories() {
			folders[category.Category] = []string{category.Title}
		}
		folders["frontend_workflow"] = []string{"架构总览", "flows"}
		return folders, nil
	}
	if cfg.DirectoryVersion != types.CodeWikiDirectoryVersionV1 {
		return nil, fmt.Errorf("unsupported directory version %d", cfg.DirectoryVersion)
	}
	folders := make(map[string][]string, 7)
	for _, category := range types.CodeWikiDirectoryCategories() {
		folders[category.Category] = []string{category.Title}
	}
	return folders, nil
}

// PublishRun executes the atomic batch publish.
func (s *codeWikiService) PublishRun(ctx context.Context, kbID, runID string, req *types.CodeWikiRunPublishRequest) (*types.CodeWikiRun, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	run, err := s.repo.GetRun(ctx, kbID, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != types.CodeWikiRunStatusStaged {
		return nil, apperrors.NewBadRequestError("run is not staged; only staged runs can be published")
	}
	var inputConfig codeWikiRunConfig
	if err := json.Unmarshal(run.ConfigSnapshot, &inputConfig); err != nil {
		return nil, err
	}
	if err := validateCodeWikiExecutionPolicy(inputConfig); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	if len(inputConfig.SnapshotReferences) > 0 {
		entries, err := s.repo.ListRunRepositories(ctx, runID)
		if err != nil {
			return nil, err
		}
		if err := s.verifyCodeWikiSnapshotReferences(ctx, kbID, entries, inputConfig.SnapshotReferences); err != nil {
			return nil, err
		}
	}
	folders, err := codeWikiFoldersForRun(run)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	resolve := strings.TrimSpace(req.ResolveConflicts)
	if resolve == "" {
		resolve = types.CodeWikiConflictAbort
	}
	if resolve != types.CodeWikiConflictAbort && resolve != types.CodeWikiConflictKeep {
		return nil, apperrors.NewBadRequestError("resolve_conflicts must be abort or keep")
	}

	pages, err := s.repo.ListRunPages(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := validateCodeWikiPublishCompleteness(run, pages); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	publishable := make([]*types.CodeWikiRunPage, 0, len(pages))
	for _, page := range pages {
		if page.Status == types.CodeWikiRunPageStatusStaged || page.Status == types.CodeWikiRunPageStatusConflict {
			page.Status = types.CodeWikiRunPageStatusStaged
			publishable = append(publishable, page)
		}
	}
	if len(publishable) == 0 {
		var cfg codeWikiRunConfig
		_ = json.Unmarshal(run.ConfigSnapshot, &cfg)
		if cfg.PublicationGuard == nil || !cfg.PublicationGuard.ReviewApproved {
			return nil, apperrors.NewBadRequestError("run has no publishable pages")
		}
		for _, page := range pages {
			if page.Status != types.CodeWikiRunPageStatusPreserved {
				return nil, apperrors.NewBadRequestError("run has unresolved pages")
			}
		}
	}
	// With resolve=keep, pages whose live version is human-authored since
	// the baseline are pre-marked skipped so the transaction never sees
	// them as conflicts.
	if resolve == types.CodeWikiConflictKeep && len(publishable) > 0 {
		for _, page := range publishable {
			live, lErr := s.wikiService.GetPageBySlug(ctx, kbID, page.PageKey)
			if lErr != nil {
				continue
			}
			if live != nil && live.Version != page.BaselineVersion &&
				live.LastEditSource == types.WikiEditSourceUser {
				page.Status = types.CodeWikiRunPageStatusSkipped
			}
		}
		refreshed := make([]*types.CodeWikiRunPage, 0, len(publishable))
		for _, page := range publishable {
			if page.Status == types.CodeWikiRunPageStatusStaged {
				refreshed = append(refreshed, page)
			}
		}
		publishable = refreshed
		if len(publishable) == 0 {
			return nil, apperrors.NewBadRequestError("all pages were kept as human edits; nothing to publish")
		}
	}

	// Resolve folders inside the publication transaction, after all guards.
	// A failed page/citation must not leave an orphaned directory behind.
	folderIDByCategory := make(map[string]string, len(folders))

	// Evidence specs per page key.
	evidenceByPage := make(map[string][]*types.CodeWikiEvidenceSpec, len(publishable))
	for _, page := range publishable {
		specs := parseCodeWikiEvidence(page.Evidence)
		evidenceByPage[page.PageKey] = specs
	}

	applyPage := func(tx interfaces.TX, candidate *types.CodeWikiRunPage, live *types.WikiPage) (*types.WikiPage, *types.WikiPageRevision, error) {
		if live == nil {
			if len(folderIDByCategory) == 0 {
				for order, entry := range types.CodeWikiDirectoryCategories() {
					id, err := tx.EnsureWikiFolderPath(kbID, kb.TenantID, folders[entry.Category], order)
					if err != nil {
						return nil, nil, err
					}
					folderIDByCategory[entry.Category] = id
				}
			}
			path, err := codeWikiCandidatePath(run, candidate)
			if err != nil {
				return nil, nil, err
			}
			id, err := tx.EnsureWikiFolderPath(kbID, kb.TenantID, path, candidate.SortOrder)
			if err != nil {
				return nil, nil, err
			}
			folderIDByCategory[candidate.PageKey] = id
		}
		return buildPublishedWikiPage(kb, run, candidate, live, folderIDByCategory)
	}
	// Include explicit skips so their preservation can be checked and saved
	// atomically with the publication, rather than written ahead of it.
	conflicts, err := s.repo.PublishRun(ctx, run, pages, evidenceByPage, applyPage)
	if err != nil {
		if err == types.ErrCodeWikiPublishConflict {
			return run, apperrors.NewBadRequestError(fmt.Sprintf(
				"publish paused: %d page(s) were edited after generation (%s); retry with resolve_conflicts=keep to preserve those edits",
				len(conflicts), strings.Join(conflicts, ", ")))
		}
		return nil, err
	}

	// Legacy publishing retains its existing maintenance. Guarded runs may
	// not run this whole-library rewrite: it touches every row and timestamp.
	var frozen codeWikiRunConfig
	_ = json.Unmarshal(run.ConfigSnapshot, &frozen)
	if frozen.PublicationGuard == nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := s.wikiService.RebuildLinks(bgCtx, kbID); err != nil {
				logger.Warnf(bgCtx, "code wiki publish: rebuild links failed (non-fatal): %v", err)
			}
		}()
	}
	s.recordRunEvent(ctx, run, "published", "本轮已发布；未变文档、章节与历史证据原样保留", nil)

	recordKBActivity(ctx, s.audit, kb.TenantID, kbID, types.AuditActionKBUpdated,
		"code_wiki_run", run.ID, types.AuditOutcomeSuccess,
		map[string]any{"published_pages": len(publishable), "skipped": len(pages) - len(publishable)})
	logger.Infof(ctx, "code wiki publish: run %s published %d pages", run.ID, len(publishable))
	return run, nil
}

func codeWikiCandidatePath(run *types.CodeWikiRun, candidate *types.CodeWikiRunPage) ([]string, error) {
	folders, err := codeWikiFoldersForRun(run)
	if err != nil {
		return nil, err
	}
	root, ok := folders[candidate.Category]
	if !ok {
		return nil, fmt.Errorf("unknown code wiki category %q", candidate.Category)
	}
	path := []string(candidate.CategoryPath)
	if len(path) == 0 {
		return root, nil
	}
	if len(path) < len(root) {
		return nil, fmt.Errorf("invalid category path")
	}
	for i := range root {
		if path[i] != root[i] {
			return nil, fmt.Errorf("category path escapes its fixed directory")
		}
	}
	if len(path) > 4 {
		return nil, fmt.Errorf("document directory exceeds depth limit")
	}
	for _, segment := range path {
		if segment == "" || strings.TrimSpace(segment) != segment || strings.ContainsAny(segment, "/\\\x00") {
			return nil, fmt.Errorf("invalid category path segment")
		}
	}
	return path, nil
}

// A successful subset is not a successful run. Check the persisted plan,
// not just counters supplied by the worker, before any publishing writes.
func validateCodeWikiPublishCompleteness(run *types.CodeWikiRun, pages []*types.CodeWikiRunPage) error {
	if len(pages) == 0 || len(pages) != run.TotalPages || run.FailedPages > 0 {
		return fmt.Errorf("run has incomplete or failed pages; inspect page details before publishing")
	}
	for _, page := range pages {
		if page.Status == types.CodeWikiRunPageStatusPending || page.GenerationError != "" || page.ValidationStatus != "passed" {
			return fmt.Errorf("page %s is incomplete or unvalidated; inspect page details before publishing", page.PageKey)
		}
	}
	return nil
}

// buildPublishedWikiPage computes the next wiki page state from a staged
// candidate. New pages start at version 1 with pipeline provenance. Updates
// keep the live page's current version (the repository's
// updateWikiPageRow performs the optimistic-locked version bump) and return
// a revision snapshot of the PRE-EDIT state so history stays diffable and
// revertable.
func buildPublishedWikiPage(
	kb *types.KnowledgeBase,
	run *types.CodeWikiRun,
	candidate *types.CodeWikiRunPage,
	live *types.WikiPage,
	folderIDByCategory map[string]string,
) (*types.WikiPage, *types.WikiPageRevision, error) {
	now := time.Now().UTC()
	categoryPath, err := codeWikiCandidatePath(run, candidate)
	if err != nil {
		return nil, nil, err
	}
	if live != nil && strings.Join(live.CategoryPath, "\x00") != strings.Join(categoryPath, "\x00") {
		if len(candidate.CategoryPath) == 0 || strings.Join(live.CategoryPath, "\x00") != strings.Join(candidate.CategoryPath, "\x00") {
			return nil, nil, fmt.Errorf("page %s directory change requires an explicit migration", candidate.PageKey)
		}
		categoryPath = []string(live.CategoryPath)
	}
	metadata := map[string]interface{}{}
	if live != nil && len(live.PageMetadata) > 0 {
		if err := json.Unmarshal(live.PageMetadata, &metadata); err != nil {
			return nil, nil, fmt.Errorf("invalid existing page metadata")
		}
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["code_wiki_category"], metadata["code_wiki_run_id"], metadata["code_wiki_profile_version"] = candidate.Category, run.ID, run.ProfileVersion
	metadataJSON, _ := json.Marshal(metadata)
	outLinks := parseCodeWikiOutLinks(candidate.Content)

	if live == nil {
		folderID := folderIDByCategory[candidate.PageKey]
		if folderID == "" {
			folderID = folderIDByCategory[candidate.Category]
		}
		page := &types.WikiPage{
			ID:              uuid.NewString(),
			TenantID:        kb.TenantID,
			KnowledgeBaseID: kb.ID,
			Slug:            candidate.PageKey,
			Title:           candidate.Title,
			PageType:        candidate.PageType,
			Status:          types.WikiPageStatusPublished,
			Content:         candidate.Content,
			Summary:         candidate.Summary,
			FolderID:        folderID,
			SortOrder:       candidate.SortOrder,
			CategoryPath:    types.StringArray(categoryPath),
			OutLinks:        outLinks,
			PageMetadata:    types.JSON(metadataJSON),
			Version:         1,
			LastEditSource:  types.WikiEditSourcePipeline,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		normalizeWikiHierarchy(page)
		return page, nil, nil
	}
	// Snapshot the superseded version BEFORE mutating (revert keeps the
	// evidence bindings of that historical version intact).
	revision := &types.WikiPageRevision{
		ID:              uuid.NewString(),
		TenantID:        live.TenantID,
		KnowledgeBaseID: live.KnowledgeBaseID,
		PageID:          live.ID,
		Slug:            live.Slug,
		Version:         live.Version,
		Title:           live.Title,
		PageType:        live.PageType,
		Status:          live.Status,
		Content:         live.Content,
		Summary:         live.Summary,
		Aliases:         live.Aliases,
		EditSource:      live.LastEditSource,
		EditorID:        live.LastEditorID,
		EditedAt:        live.UpdatedAt,
	}
	live.Title = candidate.Title
	live.PageType = candidate.PageType
	live.Status = types.WikiPageStatusPublished
	live.Content = candidate.Content
	live.Summary = candidate.Summary
	// Existing page identity, folder, parent and order survive ordinary updates.
	live.CategoryPath = types.StringArray(categoryPath)
	live.OutLinks = outLinks
	live.PageMetadata = types.JSON(metadataJSON)
	live.LastEditSource = types.WikiEditSourcePipeline
	live.UpdatedAt = now
	var cfg codeWikiRunConfig
	if err := json.Unmarshal(run.ConfigSnapshot, &cfg); err != nil && len(run.ConfigSnapshot) > 0 {
		return nil, nil, err
	}
	if cfg.PublicationGuard != nil && cfg.PublicationGuard.ArchivePages[candidate.PageKey] != "" {
		live.Status = types.WikiPageStatusArchived
		// Preserve the last published document exactly for history. Archived
		// outgoing links are not part of the current knowledge graph.
		live.Content, live.Summary = revision.Content, revision.Summary
		live.OutLinks = nil
	}
	return live, revision, nil
}

// parseCodeWikiOutLinks extracts [[slug]] links with the same normalization
// as the wiki page service (wikiLinkRegex + normalizeSlug are package-level
// in the service package).
func parseCodeWikiOutLinks(content string) types.StringArray {
	matches := wikiLinkRegex.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	var links types.StringArray
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		slug := strings.TrimSpace(match[1])
		if parts := strings.SplitN(slug, "|", 2); len(parts) == 2 {
			slug = strings.TrimSpace(parts[0])
		}
		slug = normalizeSlug(slug)
		if slug != "" && !seen[slug] {
			seen[slug] = true
			links = append(links, slug)
		}
	}
	return links
}

func parseCodeWikiEvidence(blob types.JSON) []*types.CodeWikiEvidenceSpec {
	if len(blob) == 0 {
		return nil
	}
	var specs []*types.CodeWikiEvidenceSpec
	if err := json.Unmarshal(blob, &specs); err != nil {
		return nil
	}
	return specs
}

// GetEvidence resolves one evidence row by id within the authorized KB and
// enriches it with display fields plus the cited excerpt read from the
// local snapshot (never a GitLab round-trip, never an arbitrary path).
func (s *codeWikiService) GetEvidence(ctx context.Context, kbID, evidenceID string) (*types.CodeWikiPageEvidenceView, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListEvidenceByIDs(ctx, kbID, []string{evidenceID})
	if err != nil || len(rows) == 0 {
		return nil, apperrors.NewNotFoundError("code wiki evidence not found")
	}
	views, err := s.enrichEvidence(ctx, kb, rows)
	if err != nil || len(views) == 0 {
		return nil, apperrors.NewNotFoundError("code wiki evidence not found")
	}
	return views[0], nil
}

// ListPageEvidence returns the citations bound to one published page
// version — the wiki browser's source sidebar data. version <= 0 resolves
// to the page's current version.
func (s *codeWikiService) ListPageEvidence(ctx context.Context, kbID, pageID string, version int) ([]*types.CodeWikiPageEvidenceView, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if version <= 0 {
		page, pErr := s.wikiService.GetPageByID(ctx, pageID)
		if pErr != nil || page == nil {
			return nil, apperrors.NewNotFoundError("wiki page not found")
		}
		version = page.Version
	}
	rows, err := s.repo.ListEvidenceByPageVersion(ctx, kbID, pageID, version)
	if err != nil {
		return nil, err
	}
	return s.enrichEvidence(ctx, kb, rows)
}

// enrichEvidence decorates evidence rows with repository display fields,
// the frozen-commit GitLab URL and the local excerpt.
func (s *codeWikiService) enrichEvidence(ctx context.Context, kb *types.KnowledgeBase, rows []*types.CodeWikiPageEvidence) ([]*types.CodeWikiPageEvidenceView, error) {
	views := make([]*types.CodeWikiPageEvidenceView, 0, len(rows))
	bindingCache := make(map[string]*types.CodeWikiRepository)
	snapshotCache := make(map[string]*types.CodeWikiSnapshot)
	for _, row := range rows {
		view := &types.CodeWikiPageEvidenceView{CodeWikiPageEvidence: *row}
		binding, ok := bindingCache[row.RepositoryID]
		if !ok {
			if b, bErr := s.repo.GetRepository(ctx, kb.ID, row.RepositoryID); bErr == nil {
				binding = b
			}
			bindingCache[row.RepositoryID] = binding
		}
		if binding != nil {
			view.RepositoryName = binding.DisplayName
			view.RepositoryRole = binding.Role
			if binding.WebURL != "" && row.Path != "" {
				view.WebURL = fmt.Sprintf("%s/-/blob/%s/%s#L%d-L%d",
					strings.TrimRight(binding.WebURL, "/"), row.CommitSHA, row.Path, row.StartLine, row.EndLine)
			}
		}
		snapshot, ok := snapshotCache[row.SnapshotID]
		if !ok {
			if snap, sErr := s.repo.GetSnapshot(ctx, kb.ID, row.SnapshotID); sErr == nil {
				snapshot = snap
			}
			snapshotCache[row.SnapshotID] = snapshot
		}
		if snapshot != nil && snapshot.LocalPath != "" {
			scope := codebase.SnapshotScope{
				KnowledgeBaseID: kb.ID,
				SnapshotID:      snapshot.ID,
				RepositoryID:    row.RepositoryID,
				CommitSHA:       row.CommitSHA,
				LocalPath:       snapshot.LocalPath,
			}
			if excerpt, rErr := s.registry.Builtin().ReadSource(ctx, scope, row.Path, row.StartLine, row.EndLine); rErr == nil {
				view.Lines = excerpt
			}
		}
		views = append(views, view)
	}
	return views, nil
}

// CleanupKnowledgeBase removes all code wiki state after a KB delete:
// active runs are canceled first (publish on a soft-deleted KB is also
// blocked by the KB re-check), then bindings/snapshots/artifacts/runs and
// staged pages are soft-deleted, evidence rows hard-deleted, and the KB's
// snapshot directory tree removed. Shared MCP services and external
// indexes are never touched (plan §7). All steps are best-effort: a
// missing disk or provider outage must not fail the KB delete task.
func (s *codeWikiService) CleanupKnowledgeBase(ctx context.Context, tenantID uint64, kbID string) error {
	logger.Infof(ctx, "code wiki cleanup: knowledge base %s", kbID)

	// Cancel any active run so a queued worker exits early and publish is
	// refused (double fence with the KB re-check inside the worker).
	if run, err := s.repo.ActiveRun(ctx, kbID); err == nil && run != nil {
		now := time.Now().UTC()
		run.Status = types.CodeWikiRunStatusCanceled
		run.ErrorMessage = "knowledge base deleted"
		run.CancelRequested = true
		run.FinishedAt = &now
		if err := s.repo.UpdateRun(ctx, run); err != nil {
			logger.Warnf(ctx, "code wiki cleanup: cancel active run %s: %v", run.ID, err)
		}
	}

	// Soft-delete bindings (the unique index frees (kb, source, project,
	// ref) immediately).
	bindings, err := s.repo.ListRepositories(ctx, types.CodeWikiRepositoryFilter{KnowledgeBaseID: kbID})
	if err != nil {
		logger.Warnf(ctx, "code wiki cleanup: list bindings: %v", err)
	}
	for _, binding := range bindings {
		if err := s.repo.DeleteRepository(ctx, kbID, binding.ID); err != nil {
			logger.Warnf(ctx, "code wiki cleanup: delete binding %s: %v", binding.ID, err)
		}
	}

	// Remove the snapshot directory tree BEFORE deleting the rows: rows go
	// soft-deleted (historical references stay resolvable until their
	// retention window), but the KB-scoped directory under the data root is
	// exclusively ours and safe to drop now.
	kbDir := filepath.Join(CodeWikiDataRoot(), fmt.Sprint(tenantID), kbID)
	if err := os.RemoveAll(kbDir); err != nil {
		logger.Warnf(ctx, "code wiki cleanup: remove snapshot dir %s: %v", kbDir, err)
	}

	// Hard-delete evidence rows: the pages they cite are going away with
	// the KB, and their excerpts may hold the only copy of source content
	// once the snapshots are gone.
	if err := s.repo.DeleteEvidenceByKB(ctx, kbID); err != nil {
		logger.Warnf(ctx, "code wiki cleanup: delete evidence: %v", err)
	}

	// Provider-owned analysis artifacts (builtin CLI index projects) live in
	// the engine's own cache volume with server-generated project ids that
	// never cross KB boundaries; the CleanupArtifact contract refuses
	// unverifiable ownership, so nothing is deleted provider-side here.
	// Revisit after the stage-0 CLI cleanup verification.

	// Soft-delete snapshots and runs last (their rows keep historical
	// references resolvable through the soft-delete window).
	if err := s.repo.DeleteSnapshotsByKB(ctx, kbID); err != nil {
		logger.Warnf(ctx, "code wiki cleanup: delete snapshots: %v", err)
	}
	if err := s.repo.DeleteRunsByKB(ctx, kbID); err != nil {
		logger.Warnf(ctx, "code wiki cleanup: delete runs: %v", err)
	}
	if err := s.repo.DeleteArtifactsByKB(ctx, kbID); err != nil {
		logger.Warnf(ctx, "code wiki cleanup: delete artifacts: %v", err)
	}

	logger.Infof(ctx, "code wiki cleanup: knowledge base %s done", kbID)
	return nil
}
