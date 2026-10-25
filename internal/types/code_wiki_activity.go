package types

import "time"

// Planning scope is navigation metadata, never proof of analyzed behavior.
type CodeWikiPlanningBatch struct {
	ID             string                        `json:"id"`
	RepositoryID   string                        `json:"repository_id"`
	RepositoryName string                        `json:"repository_name"`
	SnapshotID     string                        `json:"snapshot_id"`
	FileCount      int                           `json:"file_count"`
	Completed      bool                          `json:"completed"`
	Modules        []CodeWikiPlanningBatchModule `json:"modules"`
}

type CodeWikiPlanningBatchModule struct {
	ID        string   `json:"id"`
	Directory string   `json:"directory"`
	Files     []string `json:"files"`
}

// Public execution facts only; never model reasoning or source/credential dumps.
type CodeWikiRunEvent struct {
	TopicKey        string     `json:"topic_key,omitempty" gorm:"type:varchar(255)"`
	AttemptID       string     `json:"attempt_id,omitempty" gorm:"type:varchar(36)"`
	StepID          string     `json:"step_id,omitempty" gorm:"type:varchar(36)"`
	ID              int64      `json:"id" gorm:"primaryKey;autoIncrement"`
	KnowledgeBaseID string     `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	RunID           string     `json:"run_id" gorm:"type:varchar(36);index"`
	Kind            string     `json:"kind" gorm:"type:varchar(32)"`
	Message         string     `json:"message" gorm:"type:varchar(512)"`
	RetryAt         *time.Time `json:"retry_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (CodeWikiRunEvent) TableName() string { return "code_wiki_run_events" }
