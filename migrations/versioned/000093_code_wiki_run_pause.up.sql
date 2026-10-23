-- Migration: 000093_code_wiki_run_pause
-- Description: Code Wiki run pause/resume — pause flag on runs and persisted
-- page plans (pending rows with scope) on run pages, so a paused run can
-- resume the remaining pages without re-planning.
-- See docs/CODE_WIKI_IMPLEMENTATION_PLAN.md.
DO $$ BEGIN RAISE NOTICE '[Migration 000093] Applying code wiki run pause'; END $$;

ALTER TABLE code_wiki_runs ADD COLUMN IF NOT EXISTS pause_requested BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN code_wiki_runs.pause_requested IS 'User asked to pause; the worker observes it between pages and transitions the run to paused (resumable)';

ALTER TABLE code_wiki_run_pages ADD COLUMN IF NOT EXISTS scope VARCHAR(1024) NOT NULL DEFAULT '';

COMMENT ON COLUMN code_wiki_run_pages.scope IS 'Planned page scope (persisted plan; resume regenerates pending pages from it)';

DO $$ BEGIN RAISE NOTICE '[Migration 000093] Code wiki run pause applied'; END $$;
