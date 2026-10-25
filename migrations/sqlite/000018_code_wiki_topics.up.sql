ALTER TABLE code_wiki_runs ADD COLUMN execution_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_wiki_runs ADD COLUMN model_call_limit INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_wiki_runs ADD COLUMN execution_deadline DATETIME;
ALTER TABLE code_wiki_runs ADD COLUMN topic_concurrency INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_wiki_run_events ADD COLUMN topic_key TEXT NOT NULL DEFAULT '';
ALTER TABLE code_wiki_run_events ADD COLUMN attempt_id TEXT NOT NULL DEFAULT '';
ALTER TABLE code_wiki_run_events ADD COLUMN step_id TEXT NOT NULL DEFAULT '';
CREATE TABLE code_wiki_topics (
    id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, knowledge_base_id TEXT NOT NULL,
    run_id TEXT NOT NULL, topic_key TEXT NOT NULL, title TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued', action TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0, attempt_id TEXT NOT NULL DEFAULT '',
    lease_owner TEXT NOT NULL DEFAULT '', lease_expires_at DATETIME,
    checkpoint JSON, error_message TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(run_id, topic_key)
);
CREATE INDEX idx_cw_topics_scope ON code_wiki_topics(tenant_id, knowledge_base_id, run_id);
CREATE TABLE code_wiki_model_slots (id TEXT PRIMARY KEY, model_id TEXT NOT NULL, expires_at DATETIME NOT NULL);
CREATE INDEX idx_cw_model_slots ON code_wiki_model_slots(model_id, expires_at);
