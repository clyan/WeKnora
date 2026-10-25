package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CodeWikiPublicationGuard is frozen before generation. Hash the raw stored
// row, never the reader's normalized markdown. The transaction checks the
// complete baseline and rejects writes outside its explicit page whitelist.
type CodeWikiPublicationGuard struct {
	TopicUpdates    bool                          `json:"topic_updates,omitempty"`
	ArchivePages    map[string]string             `json:"archive_pages,omitempty"`
	PlanToken       string                        `json:"plan_token,omitempty"`
	ReviewRequired  bool                          `json:"review_required,omitempty"`
	ReviewApproved  bool                          `json:"review_approved,omitempty"`
	Sections        map[string][]CodeWikiSection  `json:"sections,omitempty"`
	Impact          map[string]CodeWikiPageImpact `json:"impact,omitempty"`
	NewPages        map[string]string             `json:"new_pages,omitempty"`
	AllowedSections map[string]map[string]string  `json:"allowed_sections,omitempty"`
	BaselineRunID   string                        `json:"baseline_run_id,omitempty"`
	Pages           map[string]string             `json:"pages"`
	AllowedPages    []string                      `json:"allowed_pages"`
}

// These are evidence matches, not a completeness certificate or a model's
// claim of non-impact. Unmatched pages still require cross-repository review.
type CodeWikiPageImpact struct {
	Kind           string   `json:"kind"`
	ChangedSources []string `json:"changed_sources,omitempty"`
	SectionIDs     []string `json:"section_ids,omitempty"`
}

type CodeWikiImpactReviewRequest struct {
	PlanToken string              `json:"plan_token"`
	Sections  map[string][]string `json:"sections"`
}

// CodeWikiPageFingerprint includes directory/order, provenance and timestamps,
// as well as content. Even version-neutral concurrent maintenance is detected.
func CodeWikiPageFingerprint(page *WikiPage) string {
	data, _ := json.Marshal(page)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CodeWikiSourceChange records a net tree change between immutable snapshots.
// A rename is deliberately represented as delete+add until identity is proven.
type CodeWikiSourceChange struct {
	BeforeStartLine int    `json:"before_start_line,omitempty"`
	AfterStartLine  int    `json:"after_start_line,omitempty"`
	ScopeChanged    bool   `json:"scope_changed,omitempty"`
	RepositoryID    string `json:"repository_id"`
	Path            string `json:"path"`
	Kind            string `json:"kind"`
	BeforeHash      string `json:"before_hash,omitempty"`
	AfterHash       string `json:"after_hash,omitempty"`
}

type CodeWikiFrozenReference struct {
	TreeHash        string      `json:"tree_hash,omitempty"`
	Ref             string      `json:"ref"`
	FilterHash      string      `json:"filter_hash"`
	IncludePaths    StringArray `json:"include_paths,omitempty"`
	ExcludePatterns StringArray `json:"exclude_patterns,omitempty"`
}
