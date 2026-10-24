package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

type CodeWikiSection struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Hash  string `json:"hash"`
	Start int    `json:"-"`
	End   int    `json:"-"`
}

type CodeWikiSectionPatch struct {
	SectionID    string `json:"section_id"`
	ExpectedHash string `json:"expected_hash"`
	Replacement  string `json:"replacement"`
}

type CodeWikiSectionChange struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Candidate review uses the exact stored bytes; no Markdown formatter runs.
func CodeWikiSectionChanges(before, after string) []CodeWikiSectionChange {
	old, err := CodeWikiSections(before)
	if err != nil {
		return nil
	}
	next, err := CodeWikiSections(after)
	if err != nil {
		return nil
	}
	if len(old) != len(next) {
		return nil
	}
	var changes []CodeWikiSectionChange
	for i, section := range old {
		if section.ID != next[i].ID {
			return nil
		}
		if section.Hash != next[i].Hash {
			changes = append(changes, CodeWikiSectionChange{ID: section.ID, Title: section.Title, Before: before[section.Start:section.End], After: after[next[i].Start:next[i].End]})
		}
	}
	return changes
}

func CodeWikiTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Preserve raw offsets and line endings. Headings inside fenced code are not
// section boundaries. Unsupported/duplicate section identities fail closed.
func CodeWikiSections(content string) ([]CodeWikiSection, error) {
	sections := []CodeWikiSection{{ID: "intro", Title: "引言", Start: 0}}
	seen := map[string]bool{"intro": true}
	var fence codeWikiFence
	offset := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		if !fence.consume(line) && !fence.open() && codeWikiATXLevel(line) == 2 {
			title := strings.TrimSpace(strings.TrimLeft(strings.TrimLeft(line, " "), "#"))
			id := "section-" + CodeWikiTextHash(title)[:16]
			if seen[id] {
				return nil, fmt.Errorf("duplicate section heading %q requires an explicit migration", title)
			}
			seen[id] = true
			sections[len(sections)-1].End = offset
			sections = append(sections, CodeWikiSection{ID: id, Title: title, Start: offset})
		}
		offset += len(line)
	}
	if fence.open() {
		return nil, fmt.Errorf("unclosed Markdown code fence")
	}
	sections[len(sections)-1].End = len(content)
	for i := range sections {
		sections[i].Hash = CodeWikiTextHash(content[sections[i].Start:sections[i].End])
	}
	return sections, nil
}

// Fence length matters: three backticks inside a four-backtick block do not
// close it. Keep this scanner shared by section splitting and anchor checks.
type codeWikiFence struct {
	marker byte
	length int
}

func (f *codeWikiFence) open() bool { return f.length > 0 }
func (f *codeWikiFence) consume(line string) bool {
	text := strings.TrimRight(line, "\r\n")
	indent := len(text) - len(strings.TrimLeft(text, " "))
	if indent > 3 {
		return false
	}
	text = text[indent:]
	if len(text) < 3 || (text[0] != '`' && text[0] != '~') {
		return false
	}
	n := 0
	for n < len(text) && text[n] == text[0] {
		n++
	}
	if n < 3 {
		return false
	}
	if f.open() {
		if text[0] == f.marker && n >= f.length && strings.TrimSpace(text[n:]) == "" {
			f.length = 0
		}
		return true
	}
	if text[0] == '`' && strings.Contains(text[n:], "`") {
		return false
	}
	f.marker, f.length = text[0], n
	return true
}
func codeWikiATXLevel(line string) int {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 {
		return 0
	}
	text := strings.TrimRight(line[indent:], "\r\n")
	n := len(text) - len(strings.TrimLeft(text, "#"))
	if n < 1 || n > 6 || (len(text) > n && text[n] != ' ' && text[n] != '\t') {
		return 0
	}
	return n
}

var codeWikiExplicitAnchor = regexp.MustCompile(`(?i)<(?:h[1-6]\b|[^>]*\b(?:id|name)\s*=)|\{#[^}]+\}`)

func codeWikiHeadingLines(content string) []string {
	var headings []string
	var fence codeWikiFence
	previous := ""
	for _, line := range strings.Split(content, "\n") {
		if fence.consume(line) || fence.open() {
			previous = ""
			continue
		}
		trimmed := strings.TrimSpace(line)
		if previous != "" && trimmed != "" && (strings.Trim(trimmed, "=") == "" || strings.Trim(trimmed, "-") == "") {
			headings = append(headings, previous, line)
		} else if codeWikiATXLevel(line) != 0 || codeWikiExplicitAnchor.MatchString(line) {
			headings = append(headings, line)
		}
		previous = line
	}
	return headings
}

func ApplyCodeWikiSectionPatches(content string, allowed map[string]string, patches []CodeWikiSectionPatch) (string, error) {
	sections, err := CodeWikiSections(content)
	if err != nil {
		return "", err
	}
	byID := make(map[string]CodeWikiSection, len(sections))
	for _, section := range sections {
		byID[section.ID] = section
	}
	replacements := make(map[string]string, len(patches))
	for _, patch := range patches {
		section, ok := byID[patch.SectionID]
		if !ok || allowed[patch.SectionID] == "" || allowed[patch.SectionID] != section.Hash || patch.ExpectedHash != section.Hash {
			return "", fmt.Errorf("section patch is outside its frozen hash/impact whitelist: %s", patch.SectionID)
		}
		if _, duplicate := replacements[patch.SectionID]; duplicate {
			return "", fmt.Errorf("duplicate section patch")
		}
		old := content[section.Start:section.End]
		if !reflect.DeepEqual(codeWikiHeadingLines(old), codeWikiHeadingLines(patch.Replacement)) {
			return "", fmt.Errorf("section patch changes headings or anchors; explicit migration required")
		}
		replacements[patch.SectionID] = patch.Replacement
	}
	var result strings.Builder
	for _, section := range sections {
		if replacement, ok := replacements[section.ID]; ok {
			result.WriteString(replacement)
		} else {
			result.WriteString(content[section.Start:section.End])
		}
	}
	if strings.TrimSpace(result.String()) == "" {
		return "", fmt.Errorf("patch would erase the page")
	}
	if err := ValidateCodeWikiSectionChanges(content, result.String(), allowed); err != nil {
		return "", err
	}
	return result.String(), nil
}

// Independently recheck the final candidate inside the publish transaction.
func ValidateCodeWikiSectionChanges(before, after string, allowed map[string]string) error {
	old, err := CodeWikiSections(before)
	if err != nil {
		return err
	}
	next, err := CodeWikiSections(after)
	if err != nil {
		return err
	}
	if len(old) != len(next) || !reflect.DeepEqual(codeWikiHeadingLines(before), codeWikiHeadingLines(after)) {
		return fmt.Errorf("candidate changes the fixed section structure")
	}
	for i, section := range old {
		if section.ID != next[i].ID {
			return fmt.Errorf("candidate reorders sections")
		}
		if section.Hash != next[i].Hash && allowed[section.ID] != section.Hash {
			return fmt.Errorf("candidate changes an unaffected section: %s", section.Title)
		}
	}
	return nil
}
