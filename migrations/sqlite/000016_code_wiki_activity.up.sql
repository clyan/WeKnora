CREATE TABLE IF NOT EXISTS code_wiki_run_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    knowledge_base_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    message TEXT NOT NULL,
    retry_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_code_wiki_events_run ON code_wiki_run_events (knowledge_base_id, run_id, id);
ALTER TABLE code_wiki_run_pages ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_wiki_run_pages ADD COLUMN plan_metadata TEXT;
