package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

// The current provider cannot certify exhaustive cross-side impact. Freeze a
// reviewable plan and require a technical reviewer to resolve that uncertainty;
// never interpret a bounded relation sample as proof of non-impact.
func (s *codeWikiService) ApproveRunImpact(ctx context.Context, kbID, runID string, req *types.CodeWikiImpactReviewRequest) error {
	run, err := s.GetRun(ctx, kbID, runID)
	if err != nil {
		return err
	}
	var cfg codeWikiRunConfig
	if err := json.Unmarshal(run.ConfigSnapshot, &cfg); err != nil {
		return err
	}
	guard := cfg.PublicationGuard
	if guard == nil || !guard.ReviewRequired || run.Status != types.CodeWikiRunStatusPaused || guard.PlanToken != req.PlanToken {
		return fmt.Errorf("run is not awaiting this impact plan")
	}
	pages, err := s.repo.ListRunPages(ctx, runID)
	if err != nil {
		return err
	}
	planned := make(map[string]bool, len(pages))
	for _, page := range pages {
		planned[page.PageKey] = true
	}
	guard.AllowedSections = make(map[string]map[string]string, len(req.Sections))
	for slug, ids := range req.Sections {
		if !planned[slug] {
			return fmt.Errorf("page is outside the persisted plan: %s", slug)
		}
		approved := make(map[string]string, len(ids))
		for _, id := range ids {
			if _, duplicate := approved[id]; duplicate {
				return fmt.Errorf("duplicate section selection")
			}
			for _, section := range guard.Sections[slug] {
				if section.ID == id {
					approved[id] = section.Hash
					break
				}
			}
			if approved[id] == "" {
				return fmt.Errorf("unknown section identity: %s", id)
			}
		}
		guard.AllowedSections[slug] = approved
	}
	guard.ReviewRequired, guard.ReviewApproved = false, true
	blob, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.repo.ApproveRunImpact(ctx, kbID, runID, req.PlanToken, types.JSON(blob)); err != nil {
		return err
	}
	s.recordRunEvent(ctx, run, "impact_approved", "影响范围已确认；未选中的页面与章节将保持原样", nil)
	return s.ResumeRun(ctx, kbID, runID)
}

func (s *codeWikiService) rawCodeWikiBaseline(ctx context.Context, state *codeWikiRunState, slug string) (*types.WikiPage, error) {
	pages, err := s.repo.ListRawWikiPages(ctx, state.run.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	for _, page := range pages {
		if page.Slug != slug {
			continue
		}
		if state.cfg.PublicationGuard.Pages[slug] != types.CodeWikiPageFingerprint(page) {
			return nil, fmt.Errorf("raw page baseline changed: %s", slug)
		}
		return page, nil
	}
	return nil, fmt.Errorf("page baseline missing: %s", slug)
}

func (s *codeWikiService) preserveCodeWikiPage(ctx context.Context, state *codeWikiRunState, page codeWikiPlannedPage) error {
	if state.cfg.PublicationGuard.NewPages[page.Slug] != "" {
		if err := s.stageRunPage(ctx, state.run, page, "", nil, ""); err != nil {
			return err
		}
		rows, err := s.repo.ListRunPages(ctx, state.run.ID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.PageKey == page.Slug {
				row.Status = types.CodeWikiRunPageStatusSkipped
				return s.repo.UpdateRunPageStatus(ctx, row)
			}
		}
		return fmt.Errorf("declined new page candidate missing")
	}
	live, err := s.rawCodeWikiBaseline(ctx, state, page.Slug)
	if err != nil {
		return err
	}
	if err := s.stageRunPage(ctx, state.run, page, live.Content, nil, ""); err != nil {
		return err
	}
	rows, err := s.repo.ListRunPages(ctx, state.run.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.PageKey == page.Slug {
			row.Status = types.CodeWikiRunPageStatusPreserved
			return s.repo.UpdateRunPageStatus(ctx, row)
		}
	}
	return fmt.Errorf("preserved candidate missing")
}
