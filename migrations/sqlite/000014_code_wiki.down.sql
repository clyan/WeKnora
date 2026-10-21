-- Migration: 000014_code_wiki (down, SQLite)
DROP TABLE IF EXISTS code_wiki_page_evidence;
DROP TABLE IF EXISTS code_wiki_run_pages;
DROP TABLE IF EXISTS code_wiki_run_repositories;
DROP TABLE IF EXISTS code_wiki_runs;
DROP TABLE IF EXISTS code_wiki_analysis_artifacts;
DROP TABLE IF EXISTS code_wiki_snapshots;
DROP TABLE IF EXISTS code_wiki_repositories;
DROP TABLE IF EXISTS code_wiki_analysis_profiles;
ALTER TABLE knowledge_bases DROP COLUMN code_wiki_config;
