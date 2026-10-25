package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var codeWikiLocalEvidenceRef = regexp.MustCompile(`\[E[0-9]+\]`)

// Different wording is only a reason to check, never proof of a contradiction.
// Questions preserve both origins and are inserted before staging, under the
// run lock. A small batch bounds the next model context; later finish attempts
// discover remaining comparisons instead of silently dropping them.
func CodeWikiConsistencyQuestions(currentKey string, cp *CodeWikiTopicCheckpoint, peers []*CodeWikiTopic) []CodeWikiTopicItem {
	known := map[string]bool{}
	for _, item := range cp.Items {
		known[item.Key] = true
	}
	normalize := func(value string) string {
		return strings.Join(strings.Fields(codeWikiLocalEvidenceRef.ReplaceAllString(value, "")), " ")
	}
	var questions []CodeWikiTopicItem
	for _, peer := range peers {
		if peer.TopicKey == currentKey || peer.TopicKey == CodeWikiSummaryTopicKey || peer.Status != "completed" {
			continue
		}
		var other CodeWikiTopicCheckpoint
		if json.Unmarshal(peer.Checkpoint, &other) != nil || other.Version != 1 {
			continue
		}
		for _, item := range cp.Items {
			if item.Status != "analyzed" || item.SourceNodeKey == "" {
				continue
			}
			for _, related := range other.Items {
				if related.Status != "analyzed" || related.SourceNodeKey != item.SourceNodeKey || (normalize(item.Conclusion) == normalize(related.Conclusion) && normalize(item.Condition) == normalize(related.Condition)) {
					continue
				}
				hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s", item.Key, item.SourceNodeKey, peer.TopicKey, peer.Revision, related.Key)))
				key := "consistency-" + hex.EncodeToString(hash[:12])
				if known[key] {
					continue
				}
				questions = append(questions, CodeWikiTopicItem{Key: key, Kind: "consistency_check", Status: "pending", ComparedItemKey: item.Key, ConsistencyPeer: &CodeWikiTopicItemReference{TopicKey: peer.TopicKey, Revision: peer.Revision, ItemKey: related.Key}, Question: fmt.Sprintf("核对公共实现：本主题的“%s”与“%s”主题的同一源码节点记录不同。请回读源码和双方结论，说明是互补、范围/条件差异，还是需要纠正；不能直接覆盖另一方。", item.Question, peer.Title)})
				known[key] = true
				if len(questions) >= 8 {
					return questions
				}
			}
		}
	}
	return questions
}
