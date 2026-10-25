package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const frontendPlannerIdleLimit = 8

var errCodeWikiFrontendPlanningStalled = fmt.Errorf("规划多次未取得新进展，已保存进度，可继续：%w", errCodeWikiRunPaused)
var errCodeWikiFrontendPlanningContext = fmt.Errorf("目录规划上下文容量不足，进度已保存：%w", errCodeWikiRunPaused)

// Compare coverage rather than requests: alternating full-file and subrange
// reads, or a range covered by several earlier reads, must not bypass the guard.
func frontendRangeCovered(spans []codeWikiSourceSpan, target codeWikiSourceSpan) bool {
	var ranges []codeWikiSourceSpan
	for _, span := range spans {
		if span.Path == target.Path {
			ranges = append(ranges, span)
		}
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].StartLine < ranges[j].StartLine })
	next := target.StartLine
	for _, span := range ranges {
		if span.StartLine > next {
			return false
		}
		if span.EndLine >= next {
			next = span.EndLine + 1
		}
		if next > target.EndLine {
			return true
		}
	}
	return false
}

func frontendReadFileCount(spans []codeWikiSourceSpan) int {
	paths := map[string]bool{}
	for _, span := range spans {
		paths[span.Path] = true
	}
	return len(paths)
}

func frontendReadPage(spans []codeWikiSourceSpan, offset int) string {
	end := offset
	// Complete JSON rows only; never cut a path or fabricate a complete ledger.
	for end < len(spans) && end-offset < 12 {
		candidate, _ := json.Marshal(spans[offset : end+1])
		if len(candidate) > 2500 && end > offset {
			break
		}
		end++
	}
	data, _ := json.Marshal(spans[offset:end])
	return fmt.Sprintf("Successful read ledger offset=%d, next_offset=%d, total_ranges=%d (navigation only, not source text):\n%s", offset, end, len(spans), data)
}

func frontendReadContext(progress *frontendRepoPlan, batch []string) string {
	offset := len(progress.ReadSpans) - 8
	if offset < 0 {
		offset = 0
	}
	result := "\nAuthoritative successful reads (use reads to paginate older ranges):\n" + frontendReadPage(progress.ReadSpans, offset)
	if progress.RepeatedReads > 0 {
		result += fmt.Sprintf("\nRepeated reads since last accepted topic/batch: %d/%d. Reading another new configuration does not reset this count. Follow actual entry/import/caller links and save an evidenced topic; an explicitly requested source recovery still counts as a reread.\n", progress.RepeatedReads, frontendPlannerIdleLimit)
	}
	if progress.IdleSteps >= 2 {
		var unread []string
		for _, path := range batch {
			read := false
			for _, span := range progress.ReadSpans {
				read = read || span.Path == path
			}
			if !read {
				unread = append(unread, path)
			}
		}
		paths, _ := json.Marshal(unread)
		result += fmt.Sprintf("\nRecovery: %d consecutive actions added no new source coverage, topic or completed inventory. Reuse prior evidence; inspect a different relevant file or unread range, propose supported topics, or finish with honest per-file coverage. Do not invent facts or mark unread implementation irrelevant just to advance. Remaining unknowns stay explicit. Repeated navigation or changing notes is not evidence. After %d such actions planning pauses with its checkpoint. Current batch files not yet read:\n%s", progress.IdleSteps, frontendPlannerIdleLimit, paths)
	}
	return result
}

// Return whole source lines only. Evidence must never claim the unreturned tail
// of a large range; a single overlong line cannot be silently accepted.
func frontendSourceExcerpt(source string, start, budget int) (string, int, error) {
	lines := strings.Split(source, "\n")
	size, end := 0, 0
	for end < len(lines) {
		next := len(lines[end])
		if end > 0 {
			next++
		}
		if size+next > budget {
			break
		}
		size += next
		end++
	}
	if end == 0 {
		return "", 0, fmt.Errorf("source line %d exceeds the tool return budget; record this unsupported boundary instead of claiming it was read", start)
	}
	return strings.Join(lines[:end], "\n"), start + end - 1, nil
}
