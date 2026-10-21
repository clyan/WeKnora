-- Migration: 000092_code_wiki
-- Description: Code Wiki knowledge base — multi-repository project wiki with
-- pluggable analysis providers (builtin codebase CLI / custom MCP), immutable
-- code snapshots, provider-versioned analysis artifacts, generation runs,
-- staged pages and line-precise code evidence.
-- See docs/CODE_WIKI_IMPLEMENTATION_PLAN.md.
DO $$ BEGIN RAISE NOTICE '[Migration 000092] Applying code wiki schema'; END $$;

-- Per-KB code-wiki configuration. Existing KBs are unaffected (column stays
-- NULL for document/faq/wiki types).
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS code_wiki_config JSONB;

COMMENT ON COLUMN knowledge_bases.code_wiki_config IS 'Code wiki KB settings: languages, page/model budgets, profile version pointer (only for type=code_wiki)';

-- ---------------------------------------------------------------------------
-- 1) code_wiki_analysis_profiles — KB-level analysis provider config
-- One row per KB. Credentials/connection settings live on the referenced
-- MCP service; this row holds provider kind, tool mappings, snapshot
-- delivery method and the latest capability check. ConfigVersion freezes
-- into runs and analysis artifacts.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_wiki_analysis_profiles (
    id                 VARCHAR(36) PRIMARY KEY,
    tenant_id          BIGINT NOT NULL,
    knowledge_base_id  VARCHAR(36) NOT NULL,
    provider_kind      VARCHAR(32) NOT NULL DEFAULT 'builtin_codebase',
    mcp_service_id     VARCHAR(36),
    tool_mappings      JSONB,
    snapshot_delivery  VARCHAR(32) NOT NULL DEFAULT 'local',
    shared_root        VARCHAR(1024) NOT NULL DEFAULT '',
    external_index_ref JSONB,
    capability_check   JSONB,
    meets_minimum      BOOLEAN NOT NULL DEFAULT FALSE,
    config_version     INT NOT NULL DEFAULT 1,
    created_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at         TIMESTAMP WITH TIME ZONE
);

-- Exactly one profile per KB.
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_profiles_kb
    ON code_wiki_analysis_profiles (knowledge_base_id)
    WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- 2) code_wiki_repositories — repository bindings
-- The single source of truth for which GitLab projects participate in a
-- code wiki KB. Code-mode GitLab data sources do NOT keep a parallel
-- settings.projects list; the binding table owns the project set.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_wiki_repositories (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    source_id         VARCHAR(36) NOT NULL,
    project_id        VARCHAR(512) NOT NULL,
    display_name      VARCHAR(512) NOT NULL DEFAULT '',
    web_url           VARCHAR(1024) NOT NULL DEFAULT '',
    role              VARCHAR(32) NOT NULL DEFAULT 'other',
    ref               VARCHAR(255) NOT NULL DEFAULT '',
    include_paths     JSONB DEFAULT '[]'::JSONB,
    exclude_patterns  JSONB DEFAULT '[]'::JSONB,
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    status            VARCHAR(32) NOT NULL DEFAULT 'idle',
    last_snapshot_id  VARCHAR(36),
    last_commit_sha   VARCHAR(64) NOT NULL DEFAULT '',
    last_sync_at      TIMESTAMP WITH TIME ZONE,
    error_message     TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMP WITH TIME ZONE
);

-- One binding per (KB, source, project, ref); renaming the display name or
-- changing filters does not create a duplicate binding.
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_repos_unique
    ON code_wiki_repositories (knowledge_base_id, source_id, project_id, ref)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_code_wiki_repos_kb
    ON code_wiki_repositories (knowledge_base_id, source_id);

-- ---------------------------------------------------------------------------
-- 3) code_wiki_snapshots — immutable SOURCE inputs
-- One row per (repository, commit, filter configuration): the filtered
-- tree on local disk. Provider indexes live separately in
-- code_wiki_analysis_artifacts so one snapshot can be analyzed by multiple
-- providers / mapping versions.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_wiki_snapshots (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    repository_id     VARCHAR(36) NOT NULL,
    commit_sha        VARCHAR(64) NOT NULL,
    filter_hash       VARCHAR(64) NOT NULL DEFAULT '',
    local_path        VARCHAR(1024) NOT NULL DEFAULT '',
    file_count        INT NOT NULL DEFAULT 0,
    total_size_bytes  BIGINT NOT NULL DEFAULT 0,
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMP WITH TIME ZONE
);

-- Snapshot reuse: re-syncing an unchanged (repo, commit, filters) input
-- finds the existing snapshot instead of re-downloading.
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_snapshots_reuse
    ON code_wiki_snapshots (repository_id, commit_sha, filter_hash)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_code_wiki_snapshots_repo
    ON code_wiki_snapshots (repository_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- 4) code_wiki_analysis_artifacts — provider-versioned analysis products
-- One row per (snapshot, profile config version): the external index /
-- graph handle plus coverage. Switching providers or mappings creates new
-- artifacts; old artifacts keep resolving for historical evidence.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_wiki_analysis_artifacts (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    repository_id     VARCHAR(36) NOT NULL,
    snapshot_id       VARCHAR(36) NOT NULL,
    profile_version   INT NOT NULL,
    engine_version    VARCHAR(32) NOT NULL DEFAULT '',
    provider_handle   VARCHAR(255) NOT NULL DEFAULT '',
    coverage_summary  JSONB,
    status            VARCHAR(32) NOT NULL DEFAULT 'pending',
    error_message     TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- One artifact per (snapshot, profile version); re-preparation reuses the row.
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_artifacts_unique
    ON code_wiki_analysis_artifacts (snapshot_id, profile_version);

CREATE INDEX IF NOT EXISTS idx_code_wiki_artifacts_repo
    ON code_wiki_analysis_artifacts (repository_id, status);

CREATE INDEX IF NOT EXISTS idx_code_wiki_artifacts_snapshot
    ON code_wiki_analysis_artifacts (snapshot_id, status);

-- ---------------------------------------------------------------------------
-- 5) code_wiki_runs — generation batches
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_wiki_runs (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    status            VARCHAR(32) NOT NULL DEFAULT 'pending',
    phase             VARCHAR(255) NOT NULL DEFAULT '',
    model_id          VARCHAR(128) NOT NULL DEFAULT '',
    profile_version   INT NOT NULL DEFAULT 0,
    capabilities      JSONB,
    config_snapshot   JSONB,
    total_pages       INT NOT NULL DEFAULT 0,
    completed_pages   INT NOT NULL DEFAULT 0,
    failed_pages      INT NOT NULL DEFAULT 0,
    model_calls       INT NOT NULL DEFAULT 0,
    tokens_used       BIGINT NOT NULL DEFAULT 0,
    error_message     TEXT NOT NULL DEFAULT '',
    cancel_requested  BOOLEAN NOT NULL DEFAULT FALSE,
    idempotency_key   VARCHAR(128) NOT NULL DEFAULT '',
    lease_owner       VARCHAR(128) NOT NULL DEFAULT '',
    lease_expires_at  TIMESTAMP WITH TIME ZONE,
    started_at        TIMESTAMP WITH TIME ZONE,
    finished_at       TIMESTAMP WITH TIME ZONE,
    published_at      TIMESTAMP WITH TIME ZONE,
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- At most one active (non-terminal) run per KB: submissions while a run is
-- in flight are rejected rather than queued blindly.
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_runs_one_active
    ON code_wiki_runs (knowledge_base_id)
    WHERE status NOT IN ('published', 'superseded', 'failed', 'canceled');

-- At most one published run per KB: the published run defines what readers
-- currently see; publishing a new run supersedes the previous one.
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_runs_one_published
    ON code_wiki_runs (knowledge_base_id)
    WHERE status = 'published';

CREATE INDEX IF NOT EXISTS idx_code_wiki_runs_kb
    ON code_wiki_runs (knowledge_base_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- 6) code_wiki_run_repositories — frozen per-run manifest
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_wiki_run_repositories (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    run_id            VARCHAR(36) NOT NULL,
    repository_id     VARCHAR(36) NOT NULL,
    snapshot_id       VARCHAR(36) NOT NULL DEFAULT '',
    artifact_id       VARCHAR(36) NOT NULL DEFAULT '',
    role              VARCHAR(32) NOT NULL DEFAULT 'other',
    display_name      VARCHAR(512) NOT NULL DEFAULT '',
    commit_sha        VARCHAR(64) NOT NULL DEFAULT '',
    excluded          BOOLEAN NOT NULL DEFAULT FALSE,
    exclude_reason    VARCHAR(512) NOT NULL DEFAULT '',
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_run_repos_unique
    ON code_wiki_run_repositories (run_id, repository_id);

CREATE INDEX IF NOT EXISTS idx_code_wiki_run_repos_run
    ON code_wiki_run_repositories (run_id);

-- ---------------------------------------------------------------------------
-- 7) code_wiki_run_pages — staged candidate pages
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_wiki_run_pages (
    id                         VARCHAR(36) PRIMARY KEY,
    tenant_id                  BIGINT NOT NULL,
    knowledge_base_id          VARCHAR(36) NOT NULL,
    run_id                     VARCHAR(36) NOT NULL,
    page_key                   VARCHAR(255) NOT NULL,
    title                      VARCHAR(512) NOT NULL DEFAULT '',
    page_type                  VARCHAR(32) NOT NULL DEFAULT 'synthesis',
    category                   VARCHAR(32) NOT NULL DEFAULT '',
    category_path              JSONB DEFAULT '[]'::JSONB,
    content                    TEXT NOT NULL DEFAULT '',
    summary                    TEXT NOT NULL DEFAULT '',
    evidence                   JSONB,
    baseline_version           INT NOT NULL DEFAULT 0,
    baseline_last_edit_source  VARCHAR(16) NOT NULL DEFAULT '',
    validation_status          VARCHAR(32) NOT NULL DEFAULT 'pending',
    validation_errors          JSONB,
    status                     VARCHAR(32) NOT NULL DEFAULT 'staged',
    generation_error           TEXT NOT NULL DEFAULT '',
    created_at                 TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at                 TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- One candidate page per (run, page identity).
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_wiki_run_pages_unique
    ON code_wiki_run_pages (run_id, page_key);

CREATE INDEX IF NOT EXISTS idx_code_wiki_run_pages_run
    ON code_wiki_run_pages (run_id, status);

-- ---------------------------------------------------------------------------
-- 8) code_wiki_page_evidence — published citations bound to page versions
-- Inserted transactionally at publish; historical versions keep their own
-- rows so reverting a page restores the references it was generated from.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS code_wiki_page_evidence (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    page_id           VARCHAR(36) NOT NULL,
    page_version      INT NOT NULL,
    run_id            VARCHAR(36) NOT NULL,
    evidence_key      VARCHAR(16) NOT NULL DEFAULT '',
    repository_id     VARCHAR(36) NOT NULL,
    snapshot_id       VARCHAR(36) NOT NULL,
    commit_sha        VARCHAR(64) NOT NULL DEFAULT '',
    path              VARCHAR(1024) NOT NULL,
    start_line        INT NOT NULL DEFAULT 0,
    end_line          INT NOT NULL DEFAULT 0,
    symbol            VARCHAR(512) NOT NULL DEFAULT '',
    type              VARCHAR(32) NOT NULL DEFAULT '',
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_code_wiki_evidence_page_version
    ON code_wiki_page_evidence (page_id, page_version);

CREATE INDEX IF NOT EXISTS idx_code_wiki_evidence_run
    ON code_wiki_page_evidence (run_id);
