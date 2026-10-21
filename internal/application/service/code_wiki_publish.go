package service

import (
	"context"
	"encoding/json"
	"fmt"
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
	publishable := make([]*types.CodeWikiRunPage, 0, len(pages))
	var failedPages []*types.CodeWikiRunPage
	for _, page := range pages {
		if page.GenerationError != "" {
			failedPages = append(failedPages, page)
			continue
		}
		if page.Status == types.CodeWikiRunPageStatusStaged {
			publishable = append(publishable, page)
		}
	}
	if len(publishable) == 0 {
		return nil, apperrors.NewBadRequestError("run has no publishable pages")
	}
	// With resolve=keep, pages whose live version is human-authored since
	// the baseline are pre-marked skipped so the transaction never sees
	// them as conflicts.
	if resolve == types.CodeWikiConflictKeep {
		for _, page := range publishable {
			live, lErr := s.wikiService.GetPageBySlug(ctx, kbID, page.PageKey)
			if lErr != nil {
				continue
			}
			if live != nil && live.Version != page.BaselineVersion &&
				live.LastEditSource == types.WikiEditSourceUser {
				page.Status = types.CodeWikiRunPageStatusSkipped
				if err := s.repo.UpdateRunPageStatus(ctx, page); err != nil {
					return nil, err
				}
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

	// Resolve target folders BEFORE the transaction — folder creation runs
	// in its own transaction at the service layer and must not nest.
	folderIDByCategory := make(map[string]string, len(codeWikiCategoryFolder))
	for category, path := range codeWikiCategoryFolder {
		folderID, _, fErr := s.wikiService.FindOrCreateFolderPath(ctx, kbID, kb.TenantID, path)
		if fErr != nil {
			logger.Warnf(ctx, "code wiki publish: folder for %s: %v", category, fErr)
			continue
		}
		folderIDByCategory[category] = folderID
	}

	// Evidence specs per page key.
	evidenceByPage := make(map[string][]*types.CodeWikiEvidenceSpec, len(publishable))
	for _, page := range publishable {
		specs := parseCodeWikiEvidence(page.Evidence)
		evidenceByPage[page.PageKey] = specs
	}

	applyPage := func(tx interfaces.TX, candidate *types.CodeWikiRunPage, live *types.WikiPage) (*types.WikiPage, *types.WikiPageRevision, error) {
		return buildPublishedWikiPage(kb, run, candidate, live, folderIDByCategory)
	}
	conflicts, err := s.repo.PublishRun(ctx, run, publishable, evidenceByPage, applyPage)
	if err != nil {
		if err == types.ErrCodeWikiPublishConflict {
			return run, apperrors.NewBadRequestError(fmt.Sprintf(
				"publish paused: %d page(s) were edited after generation (%s); retry with resolve_conflicts=keep to preserve those edits",
				len(conflicts), strings.Join(conflicts, ", ")))
		}
		return nil, err
	}

	// Best-effort link maintenance AFTER the transaction commits — the
	// published rows are already consistent; backlinks/index are derived.
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.wikiService.RebuildLinks(bgCtx, kbID); err != nil {
			logger.Warnf(bgCtx, "code wiki publish: rebuild links failed (non-fatal): %v", err)
		}
	}()

	recordKBActivity(ctx, s.audit, kb.TenantID, kbID, types.AuditActionKBUpdated,
		"code_wiki_run", run.ID, types.AuditOutcomeSuccess,
		map[string]any{"published_pages": len(publishable), "skipped": len(failedPages)})
	logger.Infof(ctx, "code wiki publish: run %s published %d pages", run.ID, len(publishable))
	return run, nil
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
	categoryPath := codeWikiCategoryFolder[candidate.Category]
	if categoryPath == nil {
		categoryPath = []string{"代码文档"}
	}
	metadata := map[string]interface{}{
		"code_wiki_category":    candidate.Category,
		"code_wiki_run_id":      run.ID,
		"code_wiki_profile_version": run.ProfileVersion,
	}
	metadataJSON, _ := json.Marshal(metadata)
	outLinks := parseCodeWikiOutLinks(candidate.Content)

	if live == nil {
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
			FolderID:        folderIDByCategory[candidate.Category],
			CategoryPath:    types.StringArray(categoryPath),
			OutLinks:        outLinks,
			PageMetadata:    types.JSON(metadataJSON),
			Version:         1,
			LastEditSource:  types.WikiEditSourcePipeline,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
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
	live.FolderID = folderIDByCategory[candidate.Category]
	live.CategoryPath = types.StringArray(categoryPath)
	live.OutLinks = outLinks
	live.PageMetadata = types.JSON(metadataJSON)
	live.LastEditSource = types.WikiEditSourcePipeline
	live.UpdatedAt = now
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
	row := rows[0]
	view := &types.CodeWikiPageEvidenceView{CodeWikiPageEvidence: *row}

	if binding, bErr := s.repo.GetRepository(ctx, kbID, row.RepositoryID); bErr == nil && binding != nil {
		view.RepositoryName = binding.DisplayName
		view.RepositoryRole = binding.Role
		if binding.WebURL != "" && row.Path != "" {
			view.WebURL = fmt.Sprintf("%s/-/blob/%s/%s#L%d-L%d",
				strings.TrimRight(binding.WebURL, "/"), row.CommitSHA, row.Path, row.StartLine, row.EndLine)
		}
	}
	if snapshot, sErr := s.repo.GetSnapshot(ctx, kbID, row.SnapshotID); sErr == nil && snapshot != nil && snapshot.LocalPath != "" {
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
	return view, nil
}
