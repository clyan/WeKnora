-- Run identity provides versioning. Record types retain separate provenance
-- for source decisions, facts, evidence, relations, document maps and reviews.
CREATE TABLE IF NOT EXISTS code_wiki_fact_records (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    run_id VARCHAR(36) NOT NULL,
    record_type VARCHAR(32) NOT NULL,
    record_key VARCHAR(200) NOT NULL,
    schema_version INTEGER NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_fact_identity
    ON code_wiki_fact_records (tenant_id, knowledge_base_id, run_id, record_type, record_key);
