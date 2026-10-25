ALTER TABLE code_wiki_runs ADD COLUMN execution_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_wiki_runs ADD COLUMN model_call_limit INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_wiki_runs ADD COLUMN execution_deadline TIMESTAMPTZ;
ALTER TABLE code_wiki_runs ADD COLUMN topic_concurrency INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_wiki_run_events ADD COLUMN topic_key VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE code_wiki_run_events ADD COLUMN attempt_id VARCHAR(36) NOT NULL DEFAULT '';
ALTER TABLE code_wiki_run_events ADD COLUMN step_id VARCHAR(36) NOT NULL DEFAULT '';
CREATE TABLE code_wiki_topics (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    run_id VARCHAR(36) NOT NULL,
    topic_key VARCHAR(255) NOT NULL,
    title VARCHAR(512) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'queued',
    action VARCHAR(512) NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0,
    attempt_id VARCHAR(36) NOT NULL DEFAULT '',
    lease_owner VARCHAR(36) NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMPTZ,
    checkpoint JSONB,
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(run_id, topic_key)
);
CREATE INDEX idx_cw_topics_scope ON code_wiki_topics(tenant_id, knowledge_base_id, run_id);
CREATE TABLE code_wiki_model_slots (
    id VARCHAR(128) PRIMARY KEY,
    model_id VARCHAR(128) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_cw_model_slots ON code_wiki_model_slots(model_id, expires_at);
