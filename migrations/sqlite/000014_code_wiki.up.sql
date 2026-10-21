-- Migration: 000014_code_wiki (SQLite)
-- Code Wiki knowledge base — multi-repository project wiki with pluggable
-- analysis providers. See docs/CODE_WIKI_IMPLEMENTATION_PLAN.md.
-- Mirrors versioned/000092.

ALTER TABLE knowledge_bases ADD COLUMN code_wiki_config TEXT;

CREATE TABLE IF NOT EXISTS code_wiki_analysis_profiles (
    id                 TEXT PRIMARY KEY,
    tenant_id          INTEGER NOT NULL,
    knowledge_base_id  TEXT NOT NULL,
    provider_kind      TEXT NOT NULL DEFAULT 'builtin_codebase',
    mcp_service_id     TEXT,
    tool_mappings      TEXT,
    snapshot_delivery  TEXT NOT NULL DEFAULT 'local',
    shared_root        TEXT NOT NULL DEFAULT '',
    external_index_ref TEXT,
    capability_check   TEXT,
    meets_minimum      BOOLEAN NOT NULL DEFAULT 0,
    config_version     INTEGER NOT NULL DEFAULT 1,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at         TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_profiles_kb
    ON code_wiki_analysis_profiles (knowledge_base_id)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS code_wiki_repositories (
    id                TEXT PRIMARY KEY,
    tenant_id         INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    source_id         TEXT NOT NULL,
    project_id        TEXT NOT NULL,
    display_name      TEXT NOT NULL DEFAULT '',
    web_url           TEXT NOT NULL DEFAULT '',
    role              TEXT NOT NULL DEFAULT 'other',
    ref               TEXT NOT NULL DEFAULT '',
    include_paths     TEXT DEFAULT '[]',
    exclude_patterns  TEXT DEFAULT '[]',
    enabled           BOOLEAN NOT NULL DEFAULT 1,
    status            TEXT NOT NULL DEFAULT 'idle',
    last_snapshot_id  TEXT,
    last_commit_sha   TEXT NOT NULL DEFAULT '',
    last_sync_at      TIMESTAMP,
    error_message     TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at        TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_repos_unique
    ON code_wiki_repositories (knowledge_base_id, source_id, project_id, ref)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_code_wiki_repos_kb
    ON code_wiki_repositories (knowledge_base_id, source_id);

CREATE TABLE IF NOT EXISTS code_wiki_snapshots (
    id                TEXT PRIMARY KEY,
    tenant_id         INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    repository_id     TEXT NOT NULL,
    commit_sha        TEXT NOT NULL,
    filter_hash       TEXT NOT NULL DEFAULT '',
    local_path        TEXT NOT NULL DEFAULT '',
    file_count        INTEGER NOT NULL DEFAULT 0,
    total_size_bytes  INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at        TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_snapshots_reuse
    ON code_wiki_snapshots (repository_id, commit_sha, filter_hash)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_code_wiki_snapshots_repo
    ON code_wiki_snapshots (repository_id, created_at DESC);

CREATE TABLE IF NOT EXISTS code_wiki_analysis_artifacts (
    id                TEXT PRIMARY KEY,
    tenant_id         INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    repository_id     TEXT NOT NULL,
    snapshot_id       TEXT NOT NULL,
    profile_version   INTEGER NOT NULL,
    engine_version    TEXT NOT NULL DEFAULT '',
    provider_handle   TEXT NOT NULL DEFAULT '',
    coverage_summary  TEXT,
    status            TEXT NOT NULL DEFAULT 'pending',
    error_message     TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_artifacts_unique
    ON code_wiki_analysis_artifacts (snapshot_id, profile_version);
CREATE INDEX IF NOT EXISTS idx_code_wiki_artifacts_repo
    ON code_wiki_analysis_artifacts (repository_id, status);
CREATE INDEX IF NOT EXISTS idx_code_wiki_artifacts_snapshot
    ON code_wiki_analysis_artifacts (snapshot_id, status);

CREATE TABLE IF NOT EXISTS code_wiki_runs (
    id                TEXT PRIMARY KEY,
    tenant_id         INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending',
    phase             TEXT NOT NULL DEFAULT '',
    model_id          TEXT NOT NULL DEFAULT '',
    profile_version   INTEGER NOT NULL DEFAULT 0,
    capabilities      TEXT,
    config_snapshot   TEXT,
    total_pages       INTEGER NOT NULL DEFAULT 0,
    completed_pages   INTEGER NOT NULL DEFAULT 0,
    failed_pages      INTEGER NOT NULL DEFAULT 0,
    model_calls       INTEGER NOT NULL DEFAULT 0,
    tokens_used       INTEGER NOT NULL DEFAULT 0,
    error_message     TEXT NOT NULL DEFAULT '',
    cancel_requested  BOOLEAN NOT NULL DEFAULT 0,
    idempotency_key   TEXT NOT NULL DEFAULT '',
    lease_owner       TEXT NOT NULL DEFAULT '',
    lease_expires_at  TIMESTAMP,
    started_at        TIMESTAMP,
    finished_at       TIMESTAMP,
    published_at      TIMESTAMP,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_runs_one_active
    ON code_wiki_runs (knowledge_base_id)
    WHERE status NOT IN ('published', 'superseded', 'failed', 'canceled');
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_runs_one_published
    ON code_wiki_runs (knowledge_base_id)
    WHERE status = 'published';
CREATE INDEX IF NOT EXISTS idx_code_wiki_runs_kb
    ON code_wiki_runs (knowledge_base_id, created_at DESC);

CREATE TABLE IF NOT EXISTS code_wiki_run_repositories (
    id                TEXT PRIMARY KEY,
    tenant_id         INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    run_id            TEXT NOT NULL,
    repository_id     TEXT NOT NULL,
    snapshot_id       TEXT NOT NULL DEFAULT '',
    artifact_id       TEXT NOT NULL DEFAULT '',
    role              TEXT NOT NULL DEFAULT 'other',
    display_name      TEXT NOT NULL DEFAULT '',
    commit_sha        TEXT NOT NULL DEFAULT '',
    excluded          BOOLEAN NOT NULL DEFAULT 0,
    exclude_reason    TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_run_repos_unique
    ON code_wiki_run_repositories (run_id, repository_id);
CREATE INDEX IF NOT EXISTS idx_code_wiki_run_repos_run
    ON code_wiki_run_repositories (run_id);

CREATE TABLE IF NOT EXISTS code_wiki_run_pages (
    id                         TEXT PRIMARY KEY,
    tenant_id                  INTEGER NOT NULL,
    knowledge_base_id          TEXT NOT NULL,
    run_id                     TEXT NOT NULL,
    page_key                   TEXT NOT NULL,
    title                      TEXT NOT NULL DEFAULT '',
    page_type                  TEXT NOT NULL DEFAULT 'synthesis',
    category                   TEXT NOT NULL DEFAULT '',
    category_path              TEXT DEFAULT '[]',
    content                    TEXT NOT NULL DEFAULT '',
    summary                    TEXT NOT NULL DEFAULT '',
    evidence                   TEXT,
    baseline_version           INTEGER NOT NULL DEFAULT 0,
    baseline_last_edit_source  TEXT NOT NULL DEFAULT '',
    validation_status          TEXT NOT NULL DEFAULT 'pending',
    validation_errors          TEXT,
    status                     TEXT NOT NULL DEFAULT 'staged',
    generation_error           TEXT NOT NULL DEFAULT '',
    created_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                 TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_run_pages_unique
    ON code_wiki_run_pages (run_id, page_key);
CREATE INDEX IF NOT EXISTS idx_code_wiki_run_pages_run
    ON code_wiki_run_pages (run_id, status);

CREATE TABLE IF NOT EXISTS code_wiki_page_evidence (
    id                TEXT PRIMARY KEY,
    tenant_id         INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    page_id           TEXT NOT NULL,
    page_version      INTEGER NOT NULL,
    run_id            TEXT NOT NULL,
    evidence_key      TEXT NOT NULL DEFAULT '',
    repository_id     TEXT NOT NULL,
    snapshot_id       TEXT NOT NULL,
    commit_sha        TEXT NOT NULL DEFAULT '',
    path              TEXT NOT NULL,
    start_line        INTEGER NOT NULL DEFAULT 0,
    end_line          INTEGER NOT NULL DEFAULT 0,
    symbol            TEXT NOT NULL DEFAULT '',
    type              TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_code_wiki_evidence_page_version
    ON code_wiki_page_evidence (page_id, page_version);
CREATE INDEX IF NOT EXISTS idx_code_wiki_evidence_run
    ON code_wiki_page_evidence (run_id);
