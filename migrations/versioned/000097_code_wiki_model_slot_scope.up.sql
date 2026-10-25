ALTER TABLE code_wiki_model_slots ADD COLUMN run_id VARCHAR(36) NOT NULL DEFAULT '';
ALTER TABLE code_wiki_model_slots ADD COLUMN tenant_id BIGINT NOT NULL DEFAULT 0;
