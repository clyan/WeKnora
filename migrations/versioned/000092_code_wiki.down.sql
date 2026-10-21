-- Migration: 000092_code_wiki (down)
DO $$ BEGIN RAISE NOTICE '[Migration 000092] Reverting code wiki schema'; END $$;

DROP TABLE IF EXISTS code_wiki_page_evidence;
DROP TABLE IF EXISTS code_wiki_run_pages;
DROP TABLE IF EXISTS code_wiki_run_repositories;
DROP TABLE IF EXISTS code_wiki_runs;
DROP TABLE IF EXISTS code_wiki_analysis_artifacts;
DROP TABLE IF EXISTS code_wiki_snapshots;
DROP TABLE IF EXISTS code_wiki_repositories;
DROP TABLE IF EXISTS code_wiki_analysis_profiles;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS code_wiki_config;
