package types

import "time"

// Fact records are immutable within a run. A published run pins the version;
// human explanations and reviews are separate records, never implementation facts.
type CodeWikiFactRecord struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	TenantID        uint64    `json:"tenant_id" gorm:"not null;uniqueIndex:idx_code_wiki_fact_identity,priority:1"`
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"not null;uniqueIndex:idx_code_wiki_fact_identity,priority:2"`
	RunID           string    `json:"run_id" gorm:"not null;uniqueIndex:idx_code_wiki_fact_identity,priority:3"`
	RecordType      string    `json:"record_type" gorm:"not null;uniqueIndex:idx_code_wiki_fact_identity,priority:4"`
	RecordKey       string    `json:"record_key" gorm:"not null;uniqueIndex:idx_code_wiki_fact_identity,priority:5"`
	SchemaVersion   int       `json:"schema_version" gorm:"not null"`
	Payload         JSON      `json:"payload" gorm:"type:jsonb;not null"`
	CreatedAt       time.Time `json:"created_at"`
}

func (CodeWikiFactRecord) TableName() string { return "code_wiki_fact_records" }

type CodeWikiFactScope struct {
	TenantID        uint64
	KnowledgeBaseID string
	RunID           string
}

type CodeWikiSourceDecision struct {
	RepositoryID string `json:"repository_id"`
	SnapshotID   string `json:"snapshot_id"`
	CommitSHA    string `json:"commit_sha"`
	Path         string `json:"path"`
	Role         string `json:"role"`
	Reason       string `json:"reason,omitempty"`
	Parsed       bool   `json:"parsed"`
}

type CodeWikiFact struct {
	Kind       string      `json:"kind"`
	Subject    string      `json:"subject"`
	State      string      `json:"state"` // syntax_candidate, code_supported, unresolved
	Attributes JSON        `json:"attributes"`
	Evidence   StringArray `json:"evidence"`
	Conditions StringArray `json:"conditions,omitempty"`
	Unknowns   StringArray `json:"unknowns,omitempty"`
}

type CodeWikiFactEvidence struct {
	RepositoryID string `json:"repository_id"`
	SnapshotID   string `json:"snapshot_id"`
	CommitSHA    string `json:"commit_sha"`
	Path         string `json:"path"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	SourceRole   string `json:"source_role"`
}

type CodeWikiFactRelation struct {
	From       string      `json:"from"`
	To         string      `json:"to"`
	Kind       string      `json:"kind"`
	State      string      `json:"state"`
	Evidence   StringArray `json:"evidence"`
	Conditions StringArray `json:"conditions,omitempty"`
}

type CodeWikiFactDocumentLink struct {
	PageKey   string      `json:"page_key"`
	SectionID string      `json:"section_id"`
	Facts     StringArray `json:"facts"`
}

type CodeWikiFactQuestion struct {
	Question string      `json:"question"`
	Facts    StringArray `json:"facts,omitempty"`
}

type CodeWikiFactReview struct {
	FactKey     string    `json:"fact_key"`
	ActorID     string    `json:"actor_id"`
	Conclusion  string    `json:"conclusion"`
	Explanation string    `json:"explanation,omitempty"`
	RecordedAt  time.Time `json:"recorded_at"`
}
