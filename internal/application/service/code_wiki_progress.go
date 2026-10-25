package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type codeWikiTopicCursor struct {
	Revision string `json:"revision"`
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
	Offset   int    `json:"offset"`
}

func (s *codeWikiService) GetRunTopicPage(ctx context.Context, kb, run, status, cursor string, limit int) (*types.CodeWikiTopicPage, error) {
	if limit < 1 || limit > 100 {
		return nil, apperrors.NewBadRequestError("每页主题数应为 1–100")
	}
	switch status {
	case "", "queued", "running", "waiting", "paused", "failed", "completed":
	default:
		return nil, apperrors.NewBadRequestError("请选择有效的主题状态")
	}
	if len(cursor) > 2048 {
		return nil, apperrors.NewBadRequestError("翻页位置无效，请刷新主题列表")
	}
	var after codeWikiTopicCursor
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(decoded, &after) != nil || after.RunID != run || after.Status != status || after.Offset < 0 {
			return nil, apperrors.NewBadRequestError("翻页位置无效，请刷新主题列表")
		}
	}
	progress, err := s.GetRunProgress(ctx, kb, run)
	if err != nil {
		return nil, err
	}
	if cursor != "" && after.Revision != progress.Revision {
		return nil, types.ErrCodeWikiProgressChanged
	}
	result := &types.CodeWikiTopicPage{Revision: progress.Revision, Counts: map[string]int{}, Topics: []*types.CodeWikiTopic{}}
	var matching []*types.CodeWikiTopic
	for _, topic := range progress.Topics {
		result.Counts[topic.Status]++
		if status == "" || topic.Status == status {
			matching = append(matching, topic)
		}
	}
	result.Total = len(matching)
	if after.Offset > result.Total {
		return nil, apperrors.NewBadRequestError("翻页位置无效，请刷新主题列表")
	}
	end := after.Offset + limit
	if end > result.Total {
		end = result.Total
	}
	result.Topics = matching[after.Offset:end]
	if end < result.Total {
		blob, _ := json.Marshal(codeWikiTopicCursor{Revision: result.Revision, RunID: run, Status: status, Offset: end})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(blob)
	}
	return result, nil
}

func (s *codeWikiService) GetRunProgress(ctx context.Context, kbID, runID string) (*types.CodeWikiProgressSnapshot, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	repo, ok := s.repo.(interfaces.CodeWikiProgressRepository)
	if !ok {
		return nil, fmt.Errorf("进度查询尚未就绪，请更新服务后重试")
	}
	progress, err := repo.ReadRunProgress(ctx, kb.TenantID, kbID, runID)
	if err != nil {
		return nil, err
	}
	if err := decorateCodeWikiRunDetail(progress.Run, progress.Manifest, progress.Pages, progress.BaselinePages); err != nil {
		return nil, err
	}
	progress.BaselinePages = nil
	return progress, nil
}

func decorateCodeWikiRunDetail(run *types.CodeWikiRun, manifest []*types.CodeWikiRunRepository, pages []*types.CodeWikiRunPage, raw []*types.WikiPage) error {
	var cfg codeWikiRunConfig
	if len(run.ConfigSnapshot) > 0 && json.Unmarshal(run.ConfigSnapshot, &cfg) != nil {
		return fmt.Errorf("任务配置无法读取")
	}
	run.PlanningBatch = nil
	if cfg.FrontendPlanning != nil && cfg.FrontendPlanning.Batch != nil {
		batch := cfg.FrontendPlanning.Batch
		for _, entry := range manifest {
			if !entry.Excluded && entry.RepositoryID == batch.RepositoryID && entry.SnapshotID == batch.SnapshotID {
				run.PlanningBatch = batch
				break
			}
		}
	}
	for _, entry := range manifest {
		ref := cfg.SnapshotReferences[entry.RepositoryID]
		entry.Ref, entry.FilterHash, entry.IncludePaths, entry.ExcludePatterns = ref.Ref, ref.FilterHash, ref.IncludePaths, ref.ExcludePatterns
	}
	for _, page := range pages {
		for _, live := range raw {
			if live.Slug != page.PageKey {
				continue
			}
			if cfg.PublicationGuard != nil && cfg.PublicationGuard.Pages[live.Slug] == types.CodeWikiPageFingerprint(live) {
				content := live.Content
				page.BaselineContent = &content
				if page.GenerationError == "" && page.Status == types.CodeWikiRunPageStatusStaged {
					page.SectionChanges = types.CodeWikiSectionChanges(content, page.Content)
				}
			} else {
				page.BaselineChanged = true
			}
			break
		}
	}
	markCodeWikiSourceCompatibility(run)
	return nil
}
