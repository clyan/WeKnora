package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

func hasCodeWikiSummary(pages []codeWikiPlannedPage) bool {
	for _, page := range pages {
		if page.Slug == types.CodeWikiSummaryTopicKey {
			return true
		}
	}
	return false
}

// Only an unambiguous, byte-identical delete/add pair within one repository
// can relocate a source anchor automatically. Similar names never suffice.
func codeWikiSourceMoves(changes []types.CodeWikiSourceChange) map[string]string {
	removed, added := map[string][]string{}, map[string][]string{}
	for _, change := range changes {
		if change.ScopeChanged {
			continue
		}
		if change.Kind == "deleted" && change.BeforeHash != "" {
			key := change.RepositoryID + "\x00" + change.BeforeHash
			removed[key] = append(removed[key], change.RepositoryID+"\x00"+change.Path)
		}
		if change.Kind == "added" && change.AfterHash != "" {
			key := change.RepositoryID + "\x00" + change.AfterHash
			added[key] = append(added[key], change.Path)
		}
	}
	moves := map[string]string{}
	for key, before := range removed {
		if len(before) == 1 && len(added[key]) == 1 {
			moves[before[0]] = added[key][0]
		}
	}
	return moves
}

// File deletion is a program fact. Archiving is justified only when every
// owned source is deleted under the same scope and no replacement was added.
// Partial deletions, changed filters and ambiguous relocations need reanalysis.
func codeWikiSourcesDeleted(page codeWikiPlannedPage, changes []types.CodeWikiSourceChange) bool {
	if len(page.SourcePaths) == 0 {
		return false
	}
	deleted := map[string]bool{}
	for _, change := range changes {
		if change.RepositoryID != page.RepositoryID {
			continue
		}
		if change.ScopeChanged || change.Kind == "added" {
			return false
		}
		if change.Kind == "deleted" && change.BeforeHash != "" && change.AfterHash == "" {
			deleted[change.Path] = true
		}
	}
	for _, path := range page.SourcePaths {
		if !deleted[path] {
			return false
		}
	}
	return true
}

func removeDeletedCodeWikiRoots(page *codeWikiPlannedPage, changes []types.CodeWikiSourceChange) {
	deleted := map[string]bool{}
	for _, change := range changes {
		if change.RepositoryID == page.RepositoryID && change.Kind == "deleted" && !change.ScopeChanged {
			deleted[change.Path] = true
		}
	}
	var surviving []string
	for _, path := range page.SourcePaths {
		if !deleted[path] {
			surviving = append(surviving, path)
		}
	}
	if len(surviving) > 0 && len(surviving) < len(page.SourcePaths) {
		page.SourcePaths = surviving
		page.SourceSpans, page.IndexHints = nil, nil
	}
}

// Retain readable text when a reviewed archive removes a wiki link. No other
// bytes are normalized, so an unrelated document need not be regenerated.
func removeArchivedCodeWikiLinks(content string, archives map[string]string) string {
	return wikiLinkRegex.ReplaceAllStringFunc(content, func(link string) string {
		match := wikiLinkRegex.FindStringSubmatch(link)
		parts := strings.SplitN(match[1], "|", 2)
		if archives[normalizeSlug(strings.TrimSpace(parts[0]))] == "" {
			return link
		}
		if len(parts) == 2 {
			return parts[1]
		}
		return parts[0]
	})
}

func relocateCodeWikiSources(page *codeWikiPlannedPage, moves map[string]string) {
	for i, source := range page.SourcePaths {
		if target := moves[page.RepositoryID+"\x00"+source]; target != "" {
			page.SourcePaths[i] = target
		}
	}
	sort.Strings(page.SourcePaths)
	// Old line locations are discovery hints, not evidence for the new path.
	page.SourceSpans, page.IndexHints = nil, nil
}

// Dependencies are navigation, not facts. Keep the full transitive set in the
// frozen page plan; batching, model names and completion order never key it.
func frontendDependencyPaths(roots []string, candidates map[string]frontendIndexCandidate) []string {
	seen := map[string]bool{}
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		queue = append(queue, candidates[p].Dependencies...)
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// A missing dependency baseline is uncertainty, never an unaffected verdict.
// Cross-repository structure is currently sampled by the provider: disclose
// that limitation and conservatively include the other side in review.
func codeWikiTopicImpact(page codeWikiPlannedPage, changes []types.CodeWikiSourceChange, evidence []types.CodeWikiEvidenceSpec) types.CodeWikiPageImpact {
	impact := types.CodeWikiPageImpact{Kind: "unaffected"}
	paths := map[string]bool{}
	for _, p := range append(append([]string(nil), page.SourcePaths...), page.DependencyPaths...) {
		paths[page.RepositoryID+"\x00"+p] = true
	}
	for _, e := range evidence {
		paths[e.RepositoryID+"\x00"+e.Path] = true
	}
	for _, change := range changes {
		key := change.RepositoryID + "\x00" + change.Path
		kind := ""
		switch {
		case change.ScopeChanged:
			kind = "scope_changed"
		case paths[key]:
			kind = "dependency_changed"
		case change.RepositoryID != page.RepositoryID:
			kind = "cross_repository_review"
		case page.DependencyVersion != 1 || change.Kind == "added":
			kind = "dependency_gap_review"
		default:
			role, _ := codeWikiSourceRole(change.Path)
			if role != "implementation" && role != "declaration" {
				kind = "dependency_gap_review"
			}
		}
		if kind != "" {
			if impact.Kind == "unaffected" || kind == "scope_changed" {
				impact.Kind = kind
			}
			impact.ChangedSources = append(impact.ChangedSources, change.RepositoryID+":"+change.Path)
		}
	}
	sort.Strings(impact.ChangedSources)
	return impact
}

func (s *codeWikiService) prepareCodeWikiTopicImpact(ctx context.Context, state *codeWikiRunState, pages []codeWikiPlannedPage, raw []*types.WikiPage) error {
	guard := state.cfg.PublicationGuard
	prior, err := s.repo.ListRunPages(ctx, guard.BaselineRunID)
	if err != nil {
		return err
	}
	byKey := map[string]*types.CodeWikiRunPage{}
	for _, page := range prior {
		byKey[page.PageKey] = page
	}
	live := map[string]*types.WikiPage{}
	for _, page := range raw {
		live[page.Slug] = page
	}
	repo, err := s.topicRepository()
	if err != nil {
		return err
	}
	topics, err := repo.ListTopics(ctx, state.run.TenantID, state.run.KnowledgeBaseID, guard.BaselineRunID)
	if err != nil {
		return err
	}
	checkpoints := map[string]types.CodeWikiTopicCheckpoint{}
	for _, topic := range topics {
		var cp types.CodeWikiTopicCheckpoint
		if topic.Status == "completed" && json.Unmarshal(topic.Checkpoint, &cp) == nil && cp.Version == 1 {
			checkpoints[topic.TopicKey] = cp
		}
	}
	guard.Sections = map[string][]types.CodeWikiSection{}
	guard.Impact = map[string]types.CodeWikiPageImpact{}
	guard.ArchivePages = map[string]string{}
	for _, page := range pages {
		if page.Slug != types.CodeWikiSummaryTopicKey && codeWikiSourcesDeleted(page, state.cfg.SourceChanges) {
			if current := live[page.Slug]; current != nil {
				guard.ArchivePages[page.Slug] = types.CodeWikiPageFingerprint(current)
			}
		}
	}
	changed := false
	for _, page := range pages {
		if page.Slug == types.CodeWikiSummaryTopicKey {
			continue
		}
		if current := live[page.Slug]; current != nil && (current.LastEditSource == types.WikiEditSourceUser || current.LastEditSource == types.WikiEditSourceRevert) {
			return fmt.Errorf("主题 %s 已由人工维护，不能在增量规划中重新接管", page.Title)
		}
		old := byKey[page.Slug]
		impact := types.CodeWikiPageImpact{Kind: "new_source"}
		if old != nil {
			var baseline codeWikiPlannedPage
			if json.Unmarshal(old.PlanMetadata, &baseline) != nil {
				return fmt.Errorf("主题 %s 的源码范围基线无法读取", page.Title)
			}
			cp, ok := checkpoints[page.Slug]
			for _, item := range cp.Items {
				if item.Status == "unresolved" || item.Status == "unsupported" {
					baseline.DependencyVersion = 0
				}
			}
			// Union old and new edges: deletions and changed imports must not
			// erase their former consumers before impact is computed.
			baseline.DependencyPaths = append(baseline.DependencyPaths, page.DependencyPaths...)
			impact = codeWikiTopicImpact(baseline, state.cfg.SourceChanges, cp.Evidence)
			if !ok {
				impact.Kind = "dependency_gap_review"
			}
		}
		if guard.ArchivePages[page.Slug] != "" {
			impact.Kind = "source_deleted"
		} else if current := live[page.Slug]; current != nil {
			for _, target := range parseCodeWikiOutLinks(current.Content) {
				if guard.ArchivePages[target] != "" && impact.Kind == "unaffected" {
					impact.Kind = "link_cleanup"
				}
			}
		}
		guard.Impact[page.Slug] = impact
		if impact.Kind != "unaffected" {
			changed = true
			if current := live[page.Slug]; current != nil {
				id, title := "replace", "重新分析并更新整个受影响主题"
				if impact.Kind == "source_deleted" {
					id, title = "archive", "源码已全部删除，归档主题并保留历史版本"
				} else if impact.Kind == "link_cleanup" {
					title = "仅清理指向已归档主题的链接，其他内容原样保留"
				}
				guard.Sections[page.Slug] = []types.CodeWikiSection{{ID: id, Title: title, Hash: types.CodeWikiPageFingerprint(current)}}
			} else {
				identity := guard.NewPages[page.Slug]
				if identity == "" {
					return fmt.Errorf("新增主题缺少已校验的范围身份：%s", page.Title)
				}
				guard.Sections[page.Slug] = []types.CodeWikiSection{{ID: "create", Title: page.Title, Hash: identity}}
			}
		}
	}
	if current := live[types.CodeWikiSummaryTopicKey]; current != nil {
		kind := "unaffected"
		if changed {
			kind = "dependency_changed"
			guard.Sections[current.Slug] = []types.CodeWikiSection{{ID: "replace", Title: "根据受影响主题更新阅读导引", Hash: types.CodeWikiPageFingerprint(current)}}
		}
		guard.Impact[current.Slug] = types.CodeWikiPageImpact{Kind: kind}
	}
	return nil
}
