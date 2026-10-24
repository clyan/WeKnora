package types

import "time"

// Public execution facts only; never model reasoning or source/credential dumps.
type CodeWikiRunEvent struct {
	ID              int64      `json:"id" gorm:"primaryKey;autoIncrement"`
	KnowledgeBaseID string     `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	RunID           string     `json:"run_id" gorm:"type:varchar(36);index"`
	Kind            string     `json:"kind" gorm:"type:varchar(32)"`
	Message         string     `json:"message" gorm:"type:varchar(512)"`
	RetryAt         *time.Time `json:"retry_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (CodeWikiRunEvent) TableName() string { return "code_wiki_run_events" }
