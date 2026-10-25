package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Tencent/WeKnora/internal/types"
)

func codeWikiOverviewReady(topics []*types.CodeWikiTopic) bool {
	count := 0
	for _, topic := range topics {
		if topic.TopicKey == types.CodeWikiSummaryTopicKey || topic.Status == "superseded" {
			continue
		}
		count++
		if topic.Status != "completed" && topic.Status != "failed" {
			return false
		}
	}
	return count > 0
}

// Summarize accepted conclusions without another model call or source scan.
// This is a reading guide, not a fresh interpretation of the whole repository.
func (s *codeWikiService) buildTopicOverview(ctx context.Context, state *codeWikiRunState, cp *types.CodeWikiTopicCheckpoint, topics []*types.CodeWikiTopic) error {
	if !codeWikiOverviewReady(topics) {
		return fmt.Errorf("仍有主题未结束，暂不汇总概览")
	}
	pages := make(map[string]codeWikiPlannedPage, len(state.pages))
	for _, page := range state.pages {
		pages[page.Slug] = page
	}
	groupTitles := map[string]string{}
	for _, group := range types.CodeWikiFrontendCategories() {
		groupTitles[group.Category] = group.Title
	}
	sections := map[string]*types.CodeWikiTopicSection{}
	*cp = types.CodeWikiTopicCheckpoint{Version: 1, Reviews: cp.Reviews}
	var gaps []string
	for _, topic := range topics {
		if topic.TopicKey == types.CodeWikiSummaryTopicKey || topic.Status == "superseded" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		page, ok := pages[topic.TopicKey]
		if !ok {
			return fmt.Errorf("主题与当前目录不一致，暂不能汇总")
		}
		if topic.Status == "failed" {
			gaps = append(gaps, "- "+topic.Title+"：分析未完成；没有将其计划内容视为成果。")
			continue
		}
		var source types.CodeWikiTopicCheckpoint
		if json.Unmarshal(topic.Checkpoint, &source) != nil || source.Version != 1 {
			return fmt.Errorf("主题分析版本无法用于汇总")
		}
		section := sections[page.Category]
		if section == nil {
			section = &types.CodeWikiTopicSection{Title: groupTitles[page.Category]}
			sections[page.Category] = section
		}
		section.Content += "\n### " + topic.Title + "\n\n[[" + topic.TopicKey + "|阅读专题]]\n\n"
		evidence := map[string]types.CodeWikiEvidenceSpec{}
		for _, e := range source.Evidence {
			evidence[e.ID] = e
		}
		selected, unknown := 0, 0
		for _, item := range source.Items {
			if item.Status == "unresolved" || item.Status == "unsupported" {
				unknown++
				continue
			}
			// The guide quotes a small sample; complete parameter/operation
			// inventories remain in the linked document and paged analysis view.
			if item.Status != "analyzed" || selected >= 2 {
				continue
			}
			if len(item.EvidenceIDs) == 0 {
				return fmt.Errorf("已查明结论缺少依据，暂不能汇总")
			}
			copyItem := item
			hash := sha256.Sum256([]byte(topic.TopicKey + "\x00" + item.Key))
			copyItem.Key = "summary-" + hex.EncodeToString(hash[:12])
			copyItem.SourceTopicKey, copyItem.SourceRevision, copyItem.SourceItemKey = topic.TopicKey, topic.Revision, item.Key
			copyItem.EvidenceIDs = nil
			remapped := map[string]string{}
			for _, id := range item.EvidenceIDs {
				e, ok := evidence[id]
				if !ok {
					return fmt.Errorf("结论引用的依据不存在，暂不能汇总")
				}
				newID := ""
				for _, known := range cp.Evidence {
					if known.RepositoryID == e.RepositoryID && known.SnapshotID == e.SnapshotID && known.Path == e.Path && known.StartLine == e.StartLine && known.EndLine == e.EndLine {
						newID = known.ID
						break
					}
				}
				if newID == "" {
					newID = fmt.Sprintf("E%d", len(cp.Evidence)+1)
					e.ID = newID
					cp.Evidence = append(cp.Evidence, e)
				}
				copyItem.EvidenceIDs = append(copyItem.EvidenceIDs, newID)
				remapped[id] = newID
			}
			copyItem.Conclusion = remapTopicEvidenceRefs(copyItem.Conclusion, remapped)
			copyItem.Condition = remapTopicEvidenceRefs(copyItem.Condition, remapped)
			section.Content += "- " + copyItem.Conclusion
			if copyItem.Condition != "" {
				section.Content += "（条件：" + copyItem.Condition + "）"
			}
			for _, id := range copyItem.EvidenceIDs {
				section.Content += " [" + id + "]"
			}
			section.Content += "\n"
			section.ItemKeys = append(section.ItemKeys, copyItem.Key)
			cp.Items = append(cp.Items, copyItem)
			selected++
		}
		if unknown > 0 {
			gaps = append(gaps, fmt.Sprintf("- [[%s|%s]]：%d 项待澄清或暂不支持；详见专题分析记录。", topic.TopicKey, topic.Title, unknown))
		}
		// A topic with only unknowns is linked under gaps instead of being
		// dressed up as an evidenced category conclusion.
		if selected == 0 {
			gaps = append(gaps, "- [["+topic.TopicKey+"|"+topic.Title+"]]：尚无可摘录的已查明结论。")
		}
	}
	var categories []string
	for category, section := range sections {
		if len(section.ItemKeys) > 0 {
			categories = append(categories, category)
		}
	}
	sort.Strings(categories)
	for _, category := range categories {
		section := sections[category]
		section.Content = "本节摘录已查明结论，完整操作、条件与参数请阅读关联专题。\n" + section.Content
		cp.Sections = append(cp.Sections, *section)
	}
	cp.OverviewGaps = gaps
	if len(cp.Items) == 0 {
		return fmt.Errorf("没有可汇总的已查明结论；请先完成或重试其他主题")
	}
	return nil
}
