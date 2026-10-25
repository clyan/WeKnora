package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Resolve a proposal against actual source topics, not a model-supplied copy of
// their conclusions. The repository fences every referenced revision again.
func (s *codeWikiService) applyTopicPlanChange(ctx context.Context, state *codeWikiRunState, current *types.CodeWikiTopic, cp *types.CodeWikiTopicCheckpoint) error {
	request := cp.PlanChange
	if request == nil {
		return fmt.Errorf("缺少主题调整请求")
	}
	if strings.TrimSpace(request.Reason) == "" {
		return fmt.Errorf("请说明为什么需要调整主题范围")
	}
	var targets []codeWikiPlannedPage
	if json.Unmarshal(request.Targets, &targets) != nil {
		return fmt.Errorf("新主题定义无法读取")
	}
	switch request.Operation {
	case "add":
		if len(targets) < 1 || len(request.Sources) > 0 {
			return fmt.Errorf("新增主题只能复用当前主题的已读证据")
		}
	case "split":
		if len(targets) < 2 || len(request.Sources) > 0 {
			return fmt.Errorf("拆分需要至少两个新范围，来源为当前主题")
		}
	case "merge":
		if len(targets) != 1 || len(request.Sources) < 1 {
			return fmt.Errorf("合并需要一个新范围及至少一个其他来源主题")
		}
	default:
		return fmt.Errorf("请选择 add、split 或 merge")
	}
	repo, err := s.topicRepository()
	if err != nil {
		return err
	}
	update := &types.CodeWikiTopicPlanUpdate{Sources: []types.CodeWikiTopicRef{{Key: current.TopicKey, Revision: current.Revision}}}
	sourceCPs := map[string]*types.CodeWikiTopicCheckpoint{current.TopicKey: cp}
	for _, ref := range request.Sources {
		if ref.Key == types.CodeWikiSummaryTopicKey || sourceCPs[ref.Key] != nil {
			return fmt.Errorf("不能重复合并来源或合并项目概览")
		}
		source, err := repo.GetTopic(ctx, current.TenantID, current.KnowledgeBaseID, current.RunID, ref.Key)
		if err != nil {
			return err
		}
		if source.Revision != ref.Revision || source.Status == "running" || source.Status == "superseded" {
			return fmt.Errorf("来源主题仍在执行或修订已变化，请重新查询后再调整")
		}
		var other types.CodeWikiTopicCheckpoint
		if json.Unmarshal(source.Checkpoint, &other) != nil || other.Version != 1 {
			return fmt.Errorf("来源主题的分析记录无法读取")
		}
		sourceCPs[ref.Key] = &other
		update.Sources = append(update.Sources, ref)
	}
	entries, err := s.repo.ListRunRepositories(ctx, current.RunID)
	if err != nil {
		return err
	}
	summaries := map[string]*codeWikiRepoSummary{}
	for _, entry := range entries {
		summaries[entry.RepositoryID] = &codeWikiRepoSummary{Repo: entry.DisplayName, Role: entry.Role}
	}
	livePages, err := s.repo.ListRawWikiPages(ctx, current.KnowledgeBaseID)
	if err != nil {
		return err
	}
	if state.cfg.PublicationGuard == nil {
		return fmt.Errorf("缺少冻结发布范围，不能调整目录")
	}
	byDomain := map[string]int{}
	for i, target := range targets {
		page, err := normalizeFrontendPage(target, state, summaries)
		if err != nil {
			return err
		}
		targets[i] = page
		if _, ok := byDomain[page.Domain]; ok {
			return fmt.Errorf("同次调整的主题范围标识不能重复")
		}
		byDomain[page.Domain] = i
		// A source path must already have implementation evidence in one of
		// the source checkpoints. Inventory/search locations are insufficient.
		for _, path := range page.SourcePaths {
			read := false
			for _, source := range sourceCPs {
				for _, e := range source.Evidence {
					if e.RepositoryID == page.RepositoryID && e.Path == path && e.Type != "declaration" {
						read = true
					}
				}
			}
			if !read {
				return fmt.Errorf("新主题锚点尚未阅读：%s", path)
			}
		}
		for _, live := range livePages {
			if live.Slug != page.Slug {
				continue
			}
			if state.cfg.PublicationGuard.Pages[live.Slug] != types.CodeWikiPageFingerprint(live) {
				return fmt.Errorf("原文档已变化，不能自动调整该主题")
			}
			page.BaselineVersion, page.BaselineSource = live.Version, live.LastEditSource
			page.CategoryPath, page.PageType, page.SortOrder = live.CategoryPath, live.PageType, live.SortOrder
		}
		seed := newTopicCheckpoint(page)
		seed.Predecessors = append(seed.Predecessors, update.Sources...)
		for _, ref := range update.Sources {
			source := sourceCPs[ref.Key]
			for _, e := range source.Evidence {
				if e.RepositoryID == page.RepositoryID {
					for _, path := range page.SourcePaths {
						if e.Path == path {
							copyTopicEvidence(&seed, e)
						}
					}
				}
			}
		}
		blob, _ := json.Marshal(seed)
		metadata, _ := json.Marshal(page)
		update.Topics = append(update.Topics, &types.CodeWikiTopic{TenantID: current.TenantID, KnowledgeBaseID: current.KnowledgeBaseID, RunID: current.RunID, TopicKey: page.Slug, Title: page.Title, Checkpoint: types.JSON(blob), Action: "等待分析调整后的主题"})
		update.Pages = append(update.Pages, &types.CodeWikiRunPage{TenantID: current.TenantID, KnowledgeBaseID: current.KnowledgeBaseID, RunID: current.RunID, PageKey: page.Slug, Title: page.Title, Scope: page.Scope, Category: page.Category, CategoryPath: page.CategoryPath, PageType: orDefault(page.PageType, types.WikiPageTypeSynthesis), SortOrder: page.SortOrder, BaselineVersion: page.BaselineVersion, BaselineLastEditSource: page.BaselineSource, PlanMetadata: types.JSON(metadata)})
	}
	// Merge preserves every item. Split requires explicit ownership for every
	// discovered item, so an operation cannot disappear between new scopes.
	assigned := map[string]bool{}
	assignments := append([]types.CodeWikiTopicAssignment(nil), request.Assignments...)
	if request.Operation == "merge" {
		for _, ref := range update.Sources {
			key, source := ref.Key, sourceCPs[ref.Key]
			for _, item := range source.Items {
				assignments = append(assignments, types.CodeWikiTopicAssignment{SourceKey: key, ItemKey: item.Key, TargetDomain: targets[0].Domain})
			}
		}
	}
	for _, assignment := range assignments {
		source := sourceCPs[assignment.SourceKey]
		i, ok := byDomain[assignment.TargetDomain]
		if source == nil || !ok {
			return fmt.Errorf("分析项的目标主题或来源不存在")
		}
		var original *types.CodeWikiTopicItem
		for j := range source.Items {
			if source.Items[j].Key == assignment.ItemKey {
				original = &source.Items[j]
				break
			}
		}
		if original == nil {
			return fmt.Errorf("不能转移不存在的分析项")
		}
		var seed types.CodeWikiTopicCheckpoint
		if json.Unmarshal(update.Topics[i].Checkpoint, &seed) != nil {
			return fmt.Errorf("目标主题记录无法读取")
		}
		item := *original
		hash := sha256.Sum256([]byte(assignment.SourceKey + "\x00" + item.Key))
		item.Key = "from-" + hex.EncodeToString(hash[:12])
		item.SourceTopicKey, item.SourceItemKey = assignment.SourceKey, original.Key
		for _, ref := range update.Sources {
			if ref.Key == assignment.SourceKey {
				item.SourceRevision = ref.Revision
			}
		}
		item.EvidenceIDs = nil
		remap := map[string]string{}
		for _, id := range original.EvidenceIDs {
			found := false
			for _, e := range source.Evidence {
				if e.ID == id {
					replacement := copyTopicEvidence(&seed, e)
					remap[id] = replacement
					item.EvidenceIDs = append(item.EvidenceIDs, replacement)
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("转移项目的源码依据缺失")
			}
		}
		item.Conclusion, item.Condition = remapTopicEvidenceRefs(item.Conclusion, remap), remapTopicEvidenceRefs(item.Condition, remap)
		if err := mergeTopicItems(&seed, []types.CodeWikiTopicItem{item}); err != nil {
			return err
		}
		blob, _ := json.Marshal(seed)
		update.Topics[i].Checkpoint = types.JSON(blob)
		assigned[assignment.SourceKey+"\x00"+assignment.ItemKey] = true
	}
	if request.Operation != "add" {
		for _, ref := range update.Sources {
			for _, item := range sourceCPs[ref.Key].Items {
				if !assigned[ref.Key+"\x00"+item.Key] {
					return fmt.Errorf("拆分尚未为已发现项目指定去向：%s", item.Question)
				}
			}
			update.Retire = append(update.Retire, ref.Key)
		}
	}
	// Remove the request from the active checkpoint only in the transaction;
	// feedback on a rejected proposal returns to the same analysis loop.
	cp.PlanChange = nil
	blob, _ := json.Marshal(cp)
	current.Checkpoint = types.JSON(blob)
	return repo.ApplyTopicPlan(ctx, current, state.run.ExecutionEpoch, current.Revision, update)
}

func copyTopicEvidence(cp *types.CodeWikiTopicCheckpoint, e types.CodeWikiEvidenceSpec) string {
	for _, known := range cp.Evidence {
		if known.RepositoryID == e.RepositoryID && known.SnapshotID == e.SnapshotID && known.CommitSHA == e.CommitSHA && known.Path == e.Path && known.StartLine == e.StartLine && known.EndLine == e.EndLine {
			return known.ID
		}
	}
	e.ID = fmt.Sprintf("E%d", len(cp.Evidence)+1)
	cp.Evidence = append(cp.Evidence, e)
	return e.ID
}

var topicEvidenceRefPattern = regexp.MustCompile(`\[E[0-9]+\]`)

func remapTopicEvidenceRefs(text string, remap map[string]string) string {
	return topicEvidenceRefPattern.ReplaceAllStringFunc(text, func(ref string) string {
		if replacement, ok := remap[strings.Trim(ref, "[]")]; ok {
			return "[" + replacement + "]"
		}
		return ref
	})
}
