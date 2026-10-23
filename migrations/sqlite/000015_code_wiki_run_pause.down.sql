-- Migration: 000015_code_wiki_run_pause (down, SQLite)
ALTER TABLE code_wiki_run_pages DROP COLUMN scope;
ALTER TABLE code_wiki_runs DROP COLUMN pause_requested;
