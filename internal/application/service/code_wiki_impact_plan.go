package service

import (
	"context"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Build a deterministic read-only evidence projection before any model call.
// New/deleted files and cross-side changes remain visible even when no existing
// page cites them; absence of a citation match never proves absence of impact.
func (s *codeWikiService) buildCodeWikiImpactPlan(ctx context.Context, raw []*types.WikiPage, cfg *codeWikiRunConfig) error {
	guard := cfg.PublicationGuard
	if guard == nil || !guard.ReviewRequired {
		return nil
	}
	guard.Impact = make(map[string]types.CodeWikiPageImpact, len(guard.Sections))
	changes := make(map[string]types.CodeWikiSourceChange, len(cfg.SourceChanges))
	for _, change := range cfg.SourceChanges {
		changes[change.RepositoryID+"\x00"+change.Path] = change
	}
	for _, page := range raw {
		sections, ok := guard.Sections[page.Slug]
		if !ok {
			continue
		}
		impact := types.CodeWikiPageImpact{Kind: "joint_review"}
		rows, err := s.repo.ListEvidenceByPageVersion(ctx, page.KnowledgeBaseID, page.ID, page.Version)
		if err != nil {
			return err
		}
		sources, sectionIDs := map[string]bool{}, map[string]bool{}
		for _, row := range rows {
			change, changed := changes[row.RepositoryID+"\x00"+row.Path]
			if !changed {
				continue
			}
			if impact.Kind != "scope_changed" {
				impact.Kind = "citation_changed"
			}
			if change.ScopeChanged {
				impact.Kind = "scope_changed"
			}
			sources[row.RepositoryID+":"+row.Path] = true
			for _, section := range sections {
				if strings.Contains(page.Content[section.Start:section.End], "["+row.EvidenceKey+"]") {
					sectionIDs[section.ID] = true
				}
			}
		}
		for source := range sources {
			impact.ChangedSources = append(impact.ChangedSources, source)
		}
		sort.Strings(impact.ChangedSources)
		for _, section := range sections {
			if sectionIDs[section.ID] {
				impact.SectionIDs = append(impact.SectionIDs, section.ID)
			}
		}
		guard.Impact[page.Slug] = impact
	}
	return nil
}
