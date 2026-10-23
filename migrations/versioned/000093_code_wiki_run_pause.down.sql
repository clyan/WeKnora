-- Migration: 000093_code_wiki_run_pause (down)
DO $$ BEGIN RAISE NOTICE '[Migration 000093] Reverting code wiki run pause'; END $$;

ALTER TABLE code_wiki_run_pages DROP COLUMN IF EXISTS scope;
ALTER TABLE code_wiki_runs DROP COLUMN IF EXISTS pause_requested;

DO $$ BEGIN RAISE NOTICE '[Migration 000093] Code wiki run pause reverted'; END $$;
