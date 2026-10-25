package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"gopkg.in/yaml.v3"
)

var okfWikiLink = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)
var okfEvidenceKey = regexp.MustCompile(`^E[0-9]+$`)

func okfGroup(category string) (string, string) {
	switch category {
	case "frontend_page":
		return "pages", "Frontend Page"
	case "frontend_api":
		return "apis", "Frontend API Module"
	case "frontend_component":
		return "components", "Frontend Component"
	case "frontend_architecture":
		return "architecture", "Frontend Architecture"
	case "frontend_workflow":
		return "architecture/flows", "Frontend Flow"
	case "frontend_convention":
		return "conventions", "Convention"
	case "frontend_question":
		return "questions", "Open Question"
	default:
		return "references", "Reference"
	}
}

func okfFileID(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:16])
}

func okfLabel(value string) string {
	return strings.NewReplacer("[", "\\[", "]", "\\]", "\n", " ", "\r", " ").Replace(value)
}

func okfDocument(metadata map[string]any, body string) ([]byte, error) {
	frontmatter, err := yaml.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(frontmatter) + "---\n\n" + body + "\n"), nil
}

// ExportOKF exports only current published knowledge, never staged candidates,
// credentials, local paths or raw source snapshots. Export is a read operation.
func (s *codeWikiService) ExportOKF(ctx context.Context, kbID string) ([]byte, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	pages, err := s.repo.ListRawWikiPages(ctx, kbID)
	if err != nil {
		return nil, err
	}
	current := make([]*types.WikiPage, 0, len(pages))
	for _, page := range pages {
		if page.Status == types.WikiPageStatusPublished {
			current = append(current, page)
		}
	}
	if len(current) == 0 {
		return nil, apperrors.NewBadRequestError("no published knowledge to export")
	}
	if len(current) > 10000 {
		return nil, apperrors.NewBadRequestError("knowledge bundle exceeds export entry limit")
	}
	evidence := make(map[string][]*types.CodeWikiPageEvidence, len(current))
	for _, page := range current {
		rows, err := s.repo.ListEvidenceByPageVersion(ctx, kbID, page.ID, page.Version)
		if err != nil {
			return nil, err
		}
		evidence[page.ID] = rows
	}
	return buildCodeWikiOKF(kb.Name, current, evidence)
}

func buildCodeWikiOKF(title string, pages []*types.WikiPage, evidence map[string][]*types.CodeWikiPageEvidence) ([]byte, error) {
	sort.Slice(pages, func(i, j int) bool { return pages[i].Slug < pages[j].Slug })
	files := map[string][]byte{}
	paths := map[string]string{}
	typesBySlug := map[string]string{}
	groups := map[string][]*types.WikiPage{}
	inputBytes := 0
	for _, page := range pages {
		inputBytes += len(page.Content)
		if inputBytes > 64<<20 {
			return nil, fmt.Errorf("knowledge bundle exceeds 64 MiB export limit")
		}
		var metadata map[string]any
		if len(page.PageMetadata) > 0 && json.Unmarshal(page.PageMetadata, &metadata) != nil {
			return nil, fmt.Errorf("invalid knowledge metadata for %s", page.Slug)
		}
		category, _ := metadata["code_wiki_category"].(string)
		group, kind := okfGroup(category)
		if _, duplicate := paths[page.Slug]; duplicate {
			return nil, fmt.Errorf("duplicate knowledge slug")
		}
		paths[page.Slug] = group + "/" + okfFileID(page.Slug) + ".md"
		typesBySlug[page.Slug] = kind
		groups[group] = append(groups[group], page)
	}
	for _, page := range pages {
		if strings.Contains(page.Content, "\x00") {
			return nil, fmt.Errorf("knowledge contains invalid markdown bytes")
		}
		body := okfWikiLink.ReplaceAllStringFunc(page.Content, func(link string) string {
			key := strings.TrimSuffix(strings.TrimPrefix(link, "[["), "]]")
			slug, label, hasLabel := strings.Cut(key, "|")
			if !hasLabel {
				label = slug
			}
			if target, ok := paths[slug]; ok {
				return "[" + okfLabel(label) + "](/" + target + ")"
			}
			return okfLabel(label) + "（关联知识尚未导出）"
		})
		var sources []map[string]any
		keys := map[string]bool{}
		for _, row := range evidence[page.ID] {
			if row.PageID != page.ID || row.PageVersion != page.Version || row.KnowledgeBaseID != page.KnowledgeBaseID {
				return nil, fmt.Errorf("source does not match exported page version")
			}
			if !okfEvidenceKey.MatchString(row.EvidenceKey) || keys[row.EvidenceKey] {
				return nil, fmt.Errorf("invalid or duplicate source key")
			}
			keys[row.EvidenceKey] = true
			sourcePath := "references/sources/" + okfFileID(page.ID+"\x00"+row.EvidenceKey) + ".md"
			sourceTitle := fmt.Sprintf("%s @ %s : %d–%d", row.Path, row.CommitSHA, row.StartLine, row.EndLine)
			source, err := okfDocument(map[string]any{"type": "Source Reference", "title": sourceTitle,
				"weknora": map[string]any{"repository_id": row.RepositoryID, "snapshot_id": row.SnapshotID, "commit": row.CommitSHA, "path": row.Path, "start_line": row.StartLine, "end_line": row.EndLine}},
				"# 代码来源\n\n"+sourceTitle+"\n\n本文件记录冻结代码来源定位，不包含源码副本。")
			if err != nil {
				return nil, err
			}
			files[sourcePath] = source
			sources = append(sources, map[string]any{"id": row.EvidenceKey, "resource": "/" + sourcePath, "title": sourceTitle})
			body = strings.ReplaceAll(body, "["+row.EvidenceKey+"]", "[^"+row.EvidenceKey+"]")
			body += "\n\n[^" + row.EvidenceKey + "]: [" + okfLabel(sourceTitle) + "](/" + sourcePath + ")"
		}
		actor := "process:weknora-wiki-publication"
		if (page.LastEditSource == types.WikiEditSourceUser || page.LastEditSource == types.WikiEditSourceRevert) && page.LastEditorID != "" {
			actor = "human:" + page.LastEditorID
		}
		metadata := map[string]any{"type": typesBySlug[page.Slug], "title": page.Title, "description": orDefault(page.Summary, page.Title), "status": "stable",
			"generated": map[string]any{"by": actor, "at": page.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")},
			"weknora":   map[string]any{"page_id": page.ID, "version": page.Version, "slug": page.Slug, "evidence_scope": "source locations only; semantic and runtime completeness are not certified"}}
		if len(sources) > 0 {
			metadata["sources"] = sources
		}
		// Citation validation is not semantic verification. Do not emit verified.
		data, err := okfDocument(metadata, body)
		if err != nil {
			return nil, err
		}
		files[paths[page.Slug]] = data
	}
	root := "---\nokf_version: \"0.2\"\n---\n\n# " + okfLabel(title) + "\n\n本包为当前已发布知识；前端 API 为调用视角。按目录定位条目，再沿来源和知识链接阅读。\n"
	var groupNames []string
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, group := range groupNames {
		root += "\n- [" + group + "](" + group + "/index.md)"
		index := "# " + group + "\n"
		for _, page := range groups[group] {
			index += "\n- [" + okfLabel(page.Title) + "](/" + paths[page.Slug] + ") — " + okfLabel(orDefault(page.Summary, page.Title))
		}
		files[group+"/index.md"] = []byte(index + "\n")
	}
	files["index.md"] = []byte(root + "\n")
	// Current revision timestamps only; no fabricated historical change events.
	logPages := append([]*types.WikiPage(nil), pages...)
	sort.Slice(logPages, func(i, j int) bool { return logPages[i].UpdatedAt.After(logPages[j].UpdatedAt) })
	log := "# 已发布知识版本记录\n\n仅记录当前导出版本的更新时间，完整修订历史保留在平台中。\n"
	date := ""
	for _, page := range logPages {
		day := page.UpdatedAt.UTC().Format("2006-01-02")
		if date != day {
			log += "\n## " + day + "\n"
			date = day
		}
		log += fmt.Sprintf("\n- [%s](/%s) · v%d", okfLabel(page.Title), paths[page.Slug], page.Version)
	}
	files["log.md"] = []byte(log + "\n")
	var names []string
	total := 0
	for name, data := range files {
		names = append(names, name)
		total += len(data)
	}
	if total > 64<<20 {
		return nil, fmt.Errorf("knowledge bundle exceeds 64 MiB export limit")
	}
	sort.Strings(names)
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
