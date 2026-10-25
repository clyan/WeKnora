package service

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type codeWikiExportRepository struct {
	interfaces.CodeWikiRepository
	pages     []*types.WikiPage
	requested []string
}

func (r *codeWikiExportRepository) ListRawWikiPages(context.Context, string) ([]*types.WikiPage, error) {
	return r.pages, nil
}
func (r *codeWikiExportRepository) ListEvidenceByPageVersion(_ context.Context, _ string, pageID string, _ int) ([]*types.CodeWikiPageEvidence, error) {
	r.requested = append(r.requested, pageID)
	return nil, nil
}

func TestCodeWikiOKFOnlyExportsPublishedKnowledge(t *testing.T) {
	repo := &codeWikiExportRepository{pages: []*types.WikiPage{
		{ID: "published", Slug: "published", KnowledgeBaseID: "kb", Status: types.WikiPageStatusPublished, Content: "published fact"},
		{ID: "draft", Slug: "draft", KnowledgeBaseID: "kb", Status: "draft", Content: "unpublished material"},
		{ID: "archived", Slug: "archived", KnowledgeBaseID: "kb", Status: "archived", Content: "archived material"},
	}}
	s := &codeWikiService{repo: repo, kbService: codeWikiPatchKB{}}
	data, err := s.ExportOKF(context.Background(), "kb")
	require.NoError(t, err)
	require.Equal(t, []string{"published"}, repo.requested)
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	for _, file := range reader.File {
		r, err := file.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		require.NotContains(t, string(body), "unpublished material")
		require.NotContains(t, string(body), "archived material")
	}
	repo.pages = nil
	_, err = s.ExportOKF(context.Background(), "kb")
	require.ErrorContains(t, err, "no published knowledge")
}

func TestCodeWikiOKFExportPortableSourcesAndNoInventedVerification(t *testing.T) {
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	pages := []*types.WikiPage{
		{ID: "page", KnowledgeBaseID: "kb", Slug: "../index", Title: "订单 [页面]", Content: "提交 [E1]；关联 [[api|订单接口]] 和 [[missing]]。", Version: 3, UpdatedAt: now, PageMetadata: types.JSON(`{"code_wiki_category":"frontend_page"}`)},
		{ID: "api", KnowledgeBaseID: "kb", Slug: "api", Title: "API", Content: "前端调用视角", Version: 1, UpdatedAt: now, PageMetadata: types.JSON(`{"code_wiki_category":"frontend_api"}`)},
	}
	evidence := map[string][]*types.CodeWikiPageEvidence{"page": {{PageID: "page", PageVersion: 3, KnowledgeBaseID: "kb", EvidenceKey: "E1", RepositoryID: "front", SnapshotID: "snapshot", CommitSHA: "sha", Path: "src/api.ts", StartLine: 1, EndLine: 3}}}
	data, err := buildCodeWikiOKF("前端", pages, evidence)
	require.NoError(t, err)
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	files := map[string]string{}
	for _, file := range reader.File {
		require.False(t, strings.Contains(file.Name, ".."))
		require.False(t, strings.HasPrefix(file.Name, "/"))
		r, err := file.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		files[file.Name] = string(body)
		if strings.HasSuffix(file.Name, "index.md") || file.Name == "log.md" {
			continue
		}
		parts := strings.SplitN(string(body), "---\n", 3)
		require.Len(t, parts, 3)
		var metadata map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(parts[1]), &metadata))
		require.NotEmpty(t, metadata["type"])
		require.NotContains(t, metadata, "verified", "line checks are not semantic certification")
	}
	require.Contains(t, files["index.md"], `okf_version: "0.2"`)
	require.Contains(t, files["log.md"], "## 2026-10-25")
	entry := files["pages/"+okfFileID("../index")+".md"]
	require.Contains(t, entry, "[^E1]")
	require.Contains(t, entry, "/apis/"+okfFileID("api")+".md")
	require.NotContains(t, entry, "[[")
	require.Contains(t, entry, "关联知识尚未导出")
	require.Contains(t, entry, "sources:")
	require.Contains(t, files["references/sources/"+okfFileID("page\x00E1")+".md"], "commit: sha")
	evidence["page"][0].PageVersion = 2
	_, err = buildCodeWikiOKF("前端", pages, evidence)
	require.ErrorContains(t, err, "does not match")
}
