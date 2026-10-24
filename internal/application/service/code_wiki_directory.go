package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// A bounded file inventory gives the planner real anchors instead of directory
// names. The sample is not a claim of complete repository coverage.
func codeWikiPlanningFiles(ctx context.Context, root string, budget int) ([]string, error) {
	info, err := os.Lstat(root)
	if err != nil || !filepath.IsAbs(root) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("snapshot root is missing or unsafe")
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if len(paths) >= 100 || len(rel)+4 > budget {
			return fs.SkipAll
		}
		paths = append(paths, rel)
		budget -= len(rel) + 4
		return nil
	})
	return paths, err
}

func normalizeCodeWikiV1Outline(pages []codeWikiPlannedPage, catalog []types.CodeWikiDirectoryTopic,
	state *codeWikiRunState, summaries map[string]*codeWikiRepoSummary,
) ([]codeWikiPlannedPage, error) {
	out := make([]codeWikiPlannedPage, 0, len(pages))
	for _, page := range pages {
		normalized, err := normalizeCodeWikiV1Page(page, catalog, state, summaries)
		if err != nil {
			return nil, fmt.Errorf("page %q (repository %q): %w", page.Title, page.RepositoryID, err)
		}
		out = append(out, normalized)
	}
	return out, nil
}

func codeWikiTopicCatalog(summaries map[string]*codeWikiRepoSummary) []types.CodeWikiDirectoryTopic {
	var repositories []*types.CodeWikiRunRepository
	for id, summary := range summaries {
		repositories = append(repositories, &types.CodeWikiRunRepository{RepositoryID: id, DisplayName: summary.Repo, Role: summary.Role})
	}
	return types.CodeWikiDirectoryTopics(repositories)
}

func codeWikiFallbackOutline(state *codeWikiRunState, summaries map[string]*codeWikiRepoSummary) []codeWikiPlannedPage {
	if state.cfg.DirectoryVersion != types.CodeWikiDirectoryVersionV1 {
		return defaultCodeWikiOutline(summaries)
	}
	var pages []codeWikiPlannedPage
	for _, topic := range codeWikiTopicCatalog(summaries) {
		// Minimal supported starting points; further template topics are chosen
		// by analysis rather than emitted just to fill the directory.
		if !strings.HasSuffix(topic.Slug, "-a") {
			continue
		}
		pages = append(pages, codeWikiPlannedPage{Slug: topic.Slug, Title: topic.Title, Category: topic.Category,
			CategoryPath: topic.CategoryPath, SortOrder: topic.SortOrder, RepositoryID: topic.RepositoryID, Scope: topic.Title})
		if len(pages) >= state.cfg.MaxPages {
			break
		}
	}
	return pages
}

func normalizeCodeWikiV1Page(page codeWikiPlannedPage, catalog []types.CodeWikiDirectoryTopic,
	state *codeWikiRunState, summaries map[string]*codeWikiRepoSummary,
) (codeWikiPlannedPage, error) {
	for _, topic := range catalog {
		if topic.Slug == page.Slug {
			page.Title, page.Category, page.CategoryPath = topic.Title, topic.Category, topic.CategoryPath
			page.SortOrder, page.RepositoryID = topic.SortOrder, topic.RepositoryID
			return page, nil
		}
	}
	// Variable instances require an actual supplied source anchor. A title or
	// invented route cannot create an arbitrary directory/page identity.
	scope, ok := state.scopes[page.RepositoryID]
	if !ok || len(page.SourcePaths) == 0 || strings.TrimSpace(page.Title) == "" {
		return page, fmt.Errorf("non-template page requires a supplied repository and source anchor")
	}
	page.SourcePaths = append([]string(nil), page.SourcePaths...)
	sort.Strings(page.SourcePaths)
	for _, path := range page.SourcePaths {
		if err := verifySnapshotFileLines(scope.LocalPath, path, 1, 1); err != nil {
			return page, fmt.Errorf("source anchor %q must be an existing file, not a directory: %w", path, err)
		}
	}
	if page.Domain == "" || len(page.Domain) > 100 || strings.ContainsAny(page.Domain, "/\\\x00") {
		return page, fmt.Errorf("invalid document domain")
	}
	switch page.InstanceKind {
	case "workflow":
		page.Category, page.CategoryPath, page.SortOrder = types.CodeWikiPageCategoryWorkflow, types.StringArray{"功能链路", page.Domain}, 4001
	case "contract":
		page.Category, page.CategoryPath, page.SortOrder = types.CodeWikiPageCategoryInteraction, types.StringArray{"接口与跨端契约"}, 3001
	case "module":
		summary := summaries[page.RepositoryID]
		if summary == nil {
			return page, fmt.Errorf("module repository summary is unavailable")
		}
		var name, group string
		switch summary.Role {
		case "frontend":
			name, group = "前端", "业务模块"
		case "backend":
			name, group = "后端", "业务服务"
		default:
			return page, fmt.Errorf("module instances require a frontend or backend repository")
		}
		page.Category, page.CategoryPath, page.SortOrder = types.CodeWikiPageCategoryRepository,
			types.StringArray{"仓库文档", name + " · " + types.CodeWikiDirectoryLabel(summary.Repo), group}, 2500
	default:
		return page, fmt.Errorf("unsupported document instance kind")
	}
	identity := sha256.Sum256([]byte(page.RepositoryID + "\x00" + page.InstanceKind + "\x00" + page.SourcePaths[0]))
	page.Slug = page.InstanceKind + "-" + hex.EncodeToString(identity[:12])
	return page, nil
}
