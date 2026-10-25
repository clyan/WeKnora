package types

import "time"

// A topic is a resumable task, not a general fact entity or a copy of the code graph.
type CodeWikiTopic struct {
	ID              string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64     `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string     `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	RunID           string     `json:"run_id" gorm:"type:varchar(36);index:idx_cw_topic_identity,unique"`
	TopicKey        string     `json:"topic_key" gorm:"type:varchar(255);index:idx_cw_topic_identity,unique"`
	Title           string     `json:"title" gorm:"type:varchar(512)"`
	Status          string     `json:"status" gorm:"type:varchar(32);default:'queued'"`
	Action          string     `json:"action,omitempty" gorm:"type:varchar(512)"`
	Revision        int        `json:"revision" gorm:"default:0"`
	AttemptID       string     `json:"attempt_id,omitempty" gorm:"type:varchar(36)"`
	LeaseOwner      string     `json:"-" gorm:"type:varchar(36)"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	Checkpoint      JSON       `json:"checkpoint,omitempty" gorm:"type:jsonb"`
	ErrorMessage    string     `json:"error_message,omitempty" gorm:"type:text"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (CodeWikiTopic) TableName() string { return "code_wiki_topics" }

// One progress response is read from one database snapshot. Private checkpoints
// and publication baselines are excluded from its serialized representation.
type CodeWikiProgressSnapshot struct {
	Run           *CodeWikiRun             `json:"run"`
	Manifest      []*CodeWikiRunRepository `json:"manifest"`
	Pages         []*CodeWikiRunPage       `json:"pages"`
	Topics        []*CodeWikiTopic         `json:"topics"`
	Events        []*CodeWikiRunEvent      `json:"events"`
	Revision      string                   `json:"progress_revision"`
	BaselinePages []*WikiPage              `json:"-"`
}

type CodeWikiTopicItem struct {
	SourceNodeKey   string                      `json:"source_node_key,omitempty"`
	ConsistencyPeer *CodeWikiTopicItemReference `json:"consistency_peer,omitempty"`
	ComparedItemKey string                      `json:"compared_item_key,omitempty"`
	SourceTopicKey  string                      `json:"source_topic_key,omitempty"`
	SourceRevision  int                         `json:"source_revision,omitempty"`
	SourceItemKey   string                      `json:"source_item_key,omitempty"`
	Key             string                      `json:"key"`
	Kind            string                      `json:"kind"`
	Question        string                      `json:"question"`
	Status          string                      `json:"status"` // pending, analyzed, unresolved, unsupported
	Conclusion      string                      `json:"conclusion,omitempty"`
	Condition       string                      `json:"condition,omitempty"`
	EvidenceIDs     []string                    `json:"evidence_ids,omitempty"`
}

type CodeWikiTopicItemReference struct {
	TopicKey string `json:"topic_key"`
	Revision int    `json:"revision"`
	ItemKey  string `json:"item_key"`
}

type CodeWikiTopicSection struct {
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	ItemKeys []string `json:"item_keys"`
}

type CodeWikiTopicAction struct {
	TopicKey     string `json:"topic_key,omitempty"`
	Action       string `json:"action"`
	RepositoryID string `json:"repository_id,omitempty"`
	Path         string `json:"path,omitempty"`
	StartLine    int    `json:"start_line,omitempty"`
	EndLine      int    `json:"end_line,omitempty"`
	Query        string `json:"query,omitempty"`
	Offset       int    `json:"offset,omitempty"`
}

type CodeWikiTopicCheckpoint struct {
	PlanChange   *CodeWikiTopicPlanRequest `json:"plan_change,omitempty"`
	Predecessors []CodeWikiTopicRef        `json:"predecessors,omitempty"`
	Successors   []string                  `json:"successors,omitempty"`
	Validation   *CodeWikiTopicValidation  `json:"validation,omitempty"`
	OverviewGaps []string                  `json:"overview_gaps,omitempty"`
	Reviews      []CodeWikiTopicReview     `json:"reviews,omitempty"`
	Version      int                       `json:"version"`
	Items        []CodeWikiTopicItem       `json:"items"`
	Evidence     []CodeWikiEvidenceSpec    `json:"evidence"`
	Sections     []CodeWikiTopicSection    `json:"sections,omitempty"`
	LastAction   *CodeWikiTopicAction      `json:"last_action,omitempty"`
	Feedback     string                    `json:"feedback,omitempty"`
	Rounds       int                       `json:"rounds"`
	RetryAt      *time.Time                `json:"retry_at,omitempty"`
	Retries      int                       `json:"retries,omitempty"`
}

type CodeWikiTopicRef struct {
	Key      string `json:"key"`
	Revision int    `json:"revision"`
}

type CodeWikiTopicAssignment struct {
	SourceKey    string `json:"source_key"`
	ItemKey      string `json:"item_key"`
	TargetDomain string `json:"target_domain"`
}

// The model proposes scope changes; service validation resolves source-backed
// targets into rows before a single fenced repository transaction applies them.
type CodeWikiTopicPlanRequest struct {
	Operation   string                    `json:"operation"`
	Reason      string                    `json:"reason"`
	Sources     []CodeWikiTopicRef        `json:"sources,omitempty"`
	Targets     JSON                      `json:"targets"`
	Assignments []CodeWikiTopicAssignment `json:"assignments,omitempty"`
}

type CodeWikiTopicPlanUpdate struct {
	Sources []CodeWikiTopicRef
	Retire  []string
	Topics  []*CodeWikiTopic
	Pages   []*CodeWikiRunPage
}

type CodeWikiTopicReview struct {
	ID               string    `json:"id"`
	ActorID          string    `json:"actor_id"`
	AnalysisRevision int       `json:"analysis_revision"`
	Kind             string    `json:"kind"`   // clarification, correction, sample
	Result           string    `json:"result"` // noted, passed, needs_changes
	Note             string    `json:"note"`
	CreatedAt        time.Time `json:"created_at"`
}

type CodeWikiTopicReviewRequest struct {
	Revision int    `json:"revision"`
	Kind     string `json:"kind"`
	Result   string `json:"result"`
	Note     string `json:"note"`
}

type CodeWikiTopicDetail struct {
	Predecessors []CodeWikiTopicRef     `json:"predecessors,omitempty"`
	Successors   []string               `json:"successors,omitempty"`
	Coverage     CodeWikiTopicCoverage  `json:"coverage"`
	Topic        *CodeWikiTopic         `json:"topic"`
	Items        []CodeWikiTopicItem    `json:"items,omitempty"`
	Evidence     []CodeWikiEvidenceSpec `json:"evidence,omitempty"`
	Reviews      []CodeWikiTopicReview  `json:"reviews,omitempty"`
	Total        int                    `json:"total"`
	NextOffset   *int                   `json:"next_offset"`
}

type CodeWikiTopicValidation struct {
	Source    string `json:"source"`
	Citations string `json:"citations"`
	Coverage  string `json:"coverage"`
}

type CodeWikiTopicCoverage struct {
	Discovered int                      `json:"discovered"`
	Analyzed   int                      `json:"analyzed"`
	Pending    int                      `json:"pending"`
	Unknown    int                      `json:"unknown"`
	Excluded   int                      `json:"excluded"`
	Documented int                      `json:"documented"`
	Validation *CodeWikiTopicValidation `json:"validation,omitempty"`
}

type CodeWikiTopicPage struct {
	Topics     []*CodeWikiTopic `json:"topics"`
	Total      int              `json:"total"`
	Counts     map[string]int   `json:"counts"`
	NextCursor string           `json:"next_cursor,omitempty"`
	Revision   string           `json:"progress_revision"`
}

type CodeWikiTopicSource struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Source    string `json:"source"`
	NextStart *int   `json:"next_start"`
}

// Shared model-request slots are leased independently of topic leases.
type CodeWikiModelSlot struct {
	RunID     string `gorm:"type:varchar(36)"`
	TenantID  uint64
	ID        string    `gorm:"type:varchar(128);primaryKey"`
	ModelID   string    `gorm:"type:varchar(128);index"`
	ExpiresAt time.Time `gorm:"index"`
}

func (CodeWikiModelSlot) TableName() string { return "code_wiki_model_slots" }
