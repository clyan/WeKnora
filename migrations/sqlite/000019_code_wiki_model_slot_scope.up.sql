ALTER TABLE code_wiki_model_slots ADD COLUMN run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE code_wiki_model_slots ADD COLUMN tenant_id INTEGER NOT NULL DEFAULT 0;
