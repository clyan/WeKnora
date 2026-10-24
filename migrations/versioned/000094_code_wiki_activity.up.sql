CREATE TABLE IF NOT EXISTS code_wiki_run_events (
    id BIGSERIAL PRIMARY KEY,
    knowledge_base_id VARCHAR(36) NOT NULL,
    run_id VARCHAR(36) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    message VARCHAR(512) NOT NULL,
    retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_code_wiki_events_run ON code_wiki_run_events (knowledge_base_id, run_id, id);
ALTER TABLE code_wiki_run_pages ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_wiki_run_pages ADD COLUMN plan_metadata JSONB;
