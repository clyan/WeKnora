-- Migration: 000015_code_wiki_run_pause (SQLite)
-- Code Wiki run pause/resume. Mirrors versioned/000093.

ALTER TABLE code_wiki_runs ADD COLUMN pause_requested BOOLEAN NOT NULL DEFAULT 0;

ALTER TABLE code_wiki_run_pages ADD COLUMN scope TEXT NOT NULL DEFAULT '';
