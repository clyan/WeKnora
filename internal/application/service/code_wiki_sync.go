package service

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/codebase"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// Code wiki sync: build immutable snapshots (GitLab archive at a fixed
// commit → filtered tree on disk) and prepare provider analysis artifacts.
// Flow per plan §4.1; nothing here executes repository code — archives are
// only downloaded and unpacked.

const (
	// codeWikiDefaultDataRoot is the persistent volume suggested by the plan
	// (docker-compose mounts /data/code-wiki). Local dev without /data uses
	// <working-dir>/.local-data/code-wiki (created on demand).
	codeWikiDefaultDataRoot  = "/data/code-wiki"
	codeWikiDevDataRootSuffix = ".local-data/code-wiki"

	// Archive / unpack guards (env-tunable, MB / file counts).
	codeWikiMaxArchiveMBDefault  = 2048  // 2 GiB compressed
	codeWikiMaxUnpackedMBDefault = 6144  // 6 GiB unpacked
	codeWikiMaxFilesDefault      = 250000

	codeWikiConnectTimeout = 15 * time.Second
	codeWikiDownloadBudget = 60 * time.Minute
)

// CodeWikiDataRoot resolves the snapshot storage root: env override → /data
// when writable → local dev directory. The dev fallback is anchored to the
// process working directory so it never depends on where the worker was
// launched from.
func CodeWikiDataRoot() string {
	if root := strings.TrimSpace(os.Getenv("WEKNORA_CODE_WIKI_DATA_DIR")); root != "" {
		return filepath.Clean(root)
	}
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		if probe, err := os.CreateTemp("/data", ".cw-probe-*"); err == nil {
			probe.Close()
			os.Remove(probe.Name())
			return codeWikiDefaultDataRoot
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, codeWikiDevDataRootSuffix)
	}
	return codeWikiDevDataRootSuffix
}

// ensureCodeWikiRoot returns the snapshot root with the directory actually
// present — MkdirTemp fails if the parent does not exist yet (local dev boot).
func ensureCodeWikiRoot() (string, error) {
	root := CodeWikiDataRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("ensure snapshot root %s: %w", root, err)
	}
	return root, nil
}

func codeWikiMaxArchiveBytes() int64 {
	return int64(envIntOrDefault("WEKNORA_CODE_WIKI_MAX_ARCHIVE_MB", codeWikiMaxArchiveMBDefault)) << 20
}

func codeWikiMaxUnpackedBytes() int64 {
	return int64(envIntOrDefault("WEKNORA_CODE_WIKI_MAX_UNPACKED_MB", codeWikiMaxUnpackedMBDefault)) << 20
}

func codeWikiMaxFiles() int {
	return envIntOrDefault("WEKNORA_CODE_WIKI_MAX_FILES", codeWikiMaxFilesDefault)
}

func envIntOrDefault(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// codeWikiDefaultExcludes are directory and file patterns excluded from every
// snapshot regardless of binding configuration (plan §4.1: node_modules、
// dist、构建缓存、生成物、二进制及敏感文件). Vendor code is NOT excluded by
// default — bindings decide via exclude_patterns.
var codeWikiDefaultExcludes = []string{
	"node_modules", ".git", ".svn", ".hg", "dist", "build", "out", "target",
	".next", ".nuxt", ".output", "__pycache__", ".venv", "venv", ".idea",
	".vscode", "coverage", ".cache", ".turbo", ".gradle", "bin/Debug",
	"bin/Release", "obj",
}

var codeWikiDefaultExcludePrefixes = []string{
	".env", ".npmrc", ".pypirc", "id_rsa", "id_ed25519", "credentials",
	"secrets.", "secret_", "*.pem", "*.key", "*.p12", "*.keystore",
}

// code wiki binary/sensitive extensions never land in a snapshot.
var codeWikiExcludedExtensions = map[string]struct{}{
	".exe": {}, ".dll": {}, ".so": {}, ".dylib": {}, ".a": {}, ".o": {},
	".bin": {}, ".woff": {}, ".woff2": {}, ".ttf": {}, ".eot": {},
	".ico": {}, ".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {},
	".svgz": {}, ".pdf": {}, ".zip": {}, ".tar": {}, ".gz": {}, ".tgz": {},
	".bz2": {}, ".xz": {}, ".7z": {}, ".rar": {}, ".jar": {}, ".war": {},
	".class": {}, ".pyc": {}, ".wasm": {}, ".mp4": {}, ".mp3": {}, ".mov": {},
	".sqlite": {}, ".db": {},
}

// codeWikiPathAllowed reports whether a repository-relative path survives
// the binding's include/exclude rules plus the system defaults.
func codeWikiPathAllowed(rel string, includeRoots, excludePatterns []string) bool {
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" {
		return false
	}
	// Include roots: empty = whole repository.
	if len(includeRoots) > 0 {
		matched := false
		for _, root := range includeRoots {
			if rel == root || strings.HasPrefix(rel, root+"/") {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	segments := strings.Split(rel, "/")
	for _, seg := range segments {
		for _, def := range codeWikiDefaultExcludes {
			if seg == def {
				return false
			}
		}
	}
	base := segments[len(segments)-1]
	for _, pattern := range codeWikiDefaultExcludePrefixes {
		// Entries without glob metacharacters are PREFIX patterns
		// (".env" must catch ".env.production"); entries with them are
		// matched as globs ("*.pem" catches "server.pem").
		if strings.ContainsAny(pattern, "*?[") {
			if ok, _ := path.Match(pattern, base); ok {
				return false
			}
			continue
		}
		if strings.HasPrefix(base, pattern) {
			return false
		}
	}
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") || strings.HasSuffix(base, ".map") {
		return false
	}
	if _, bad := codeWikiExcludedExtensions[strings.ToLower(path.Ext(base))]; bad {
		return false
	}
	for _, pattern := range excludePatterns {
		var candidate string
		if strings.Contains(pattern, "/") {
			candidate = rel
		} else {
			candidate = base
		}
		if ok, _ := path.Match(pattern, candidate); ok {
			return false
		}
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(rel, pattern) {
			return false
		}
	}
	return true
}

// codeWikiFilterHash hashes the effective filter configuration so identical
// (commit, filters) inputs reuse one snapshot.
func codeWikiFilterHash(include []string, exclude []string) string {
	input := types.CodeWikiFilterHashInput(include, exclude)
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:16])
}

// SyncRepositories enqueues the sync task for the named bindings (or every
// enabled binding when ids is empty).
func (s *codeWikiService) SyncRepositories(ctx context.Context, kbID string, ids []string) (int, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return 0, err
	}
	bindings, err := s.repo.ListRepositories(ctx, types.CodeWikiRepositoryFilter{
		KnowledgeBaseID: kbID, Enabled: enabledPtr(true),
	})
	if err != nil {
		return 0, err
	}
	if len(bindings) == 0 {
		return 0, apperrors.NewBadRequestError("no enabled repositories to sync")
	}
	selected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	// Mark selected bindings syncing so the UI reflects in-flight state. The
	// compare-and-swap refuses bindings already syncing, so repeated clicks
	// (or a data-source sync racing a manual one) never double-enqueue work.
	targets := make([]string, 0, len(bindings))
	marked := make([]*types.CodeWikiRepository, 0, len(bindings))
	for _, b := range bindings {
		if len(selected) > 0 {
			if _, ok := selected[b.ID]; !ok {
				continue
			}
		}
		won, err := s.repo.TryMarkSyncing(ctx, b.ID)
		if err != nil {
			return 0, err
		}
		if !won {
			continue
		}
		marked = append(marked, b)
		targets = append(targets, b.ID)
	}
	if len(targets) == 0 {
		return 0, apperrors.NewBadRequestError("repository is already syncing")
	}
	payload := &types.CodeWikiSyncPayload{
		TenantID:        kb.TenantID,
		KnowledgeBaseID: kbID,
		RepositoryIDs:   targets,
		Initiator:       types.TaskInitiatorFromContext(ctx),
	}
	if err := s.enqueueCodeWikiTask(ctx, types.TypeCodeWikiSync, payload, 30*time.Minute); err != nil {
		// Restore statuses so the UI doesn't show a stuck "syncing".
		for _, b := range marked {
			b.Status = types.CodeWikiRepoStatusError
			b.ErrorMessage = "failed to enqueue sync task"
			_ = s.repo.UpdateRepositoryStatus(ctx, b)
		}
		return 0, err
	}
	return len(targets), nil
}

func (s *codeWikiService) enqueueCodeWikiTask(ctx context.Context, taskType string, payload interface{}, timeout time.Duration) error {
	blob, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	task := asynq.NewTask(taskType, blob,
		asynq.Queue(types.QueueCodeWiki), asynq.MaxRetry(3), asynq.Timeout(timeout))
	if _, err := s.taskEnqueuer.Enqueue(task); err != nil {
		logger.Errorf(ctx, "code wiki: enqueue %s failed: %v", taskType, err)
		return err
	}
	return nil
}

// RouteDataSourceSync branches a datasource:sync task for a code_wiki KB
// (called from DataSourceService.ProcessSync before any connector fetch).
// Bindings under THIS data source are resolved from the binding table — the
// code-mode data source itself carries no project list.
func (s *codeWikiService) RouteDataSourceSync(ctx context.Context, ds *types.DataSource, syncLogID string) error {
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, ds.KnowledgeBaseID)
	if err != nil || kb == nil || !kb.IsCodeWiki() {
		return fmt.Errorf("knowledge base %s is not a code wiki knowledge base", ds.KnowledgeBaseID)
	}
	if ds.Type != types.ConnectorTypeGitLab {
		return fmt.Errorf("code wiki sync requires a GitLab data source (got %s)", ds.Type)
	}
	bindings, err := s.repo.ListRepositories(ctx, types.CodeWikiRepositoryFilter{
		KnowledgeBaseID: ds.KnowledgeBaseID,
		SourceID:        ds.ID,
		Enabled:         enabledPtr(true),
	})
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		// No bindings under this connection: complete the sync log with an
		// explanatory note instead of leaving it "running" forever.
		if syncLogID != "" {
			if log, lErr := s.syncLogRepo.FindByID(ctx, syncLogID); lErr == nil && log != nil {
				now := time.Now()
				log.Status = types.SyncLogStatusSuccess
				log.FinishedAt = &now
				log.ErrorMessage = "no repositories bound to this connection"
				_ = s.syncLogRepo.Update(ctx, log)
			}
		}
		return nil
	}
	// Compare-and-swap into syncing: bindings already syncing via another
	// submission (manual click racing this data-source sync) are skipped.
	ids := make([]string, 0, len(bindings))
	marked := make([]*types.CodeWikiRepository, 0, len(bindings))
	for _, b := range bindings {
		won, err := s.repo.TryMarkSyncing(ctx, b.ID)
		if err != nil {
			return err
		}
		if !won {
			continue
		}
		marked = append(marked, b)
		ids = append(ids, b.ID)
	}
	if len(ids) == 0 {
		// Everything is already syncing: close the sync log instead of
		// leaving it "running" forever.
		if syncLogID != "" {
			if log, lErr := s.syncLogRepo.FindByID(ctx, syncLogID); lErr == nil && log != nil {
				now := time.Now()
				log.Status = types.SyncLogStatusSuccess
				log.FinishedAt = &now
				log.ErrorMessage = "repositories are already syncing"
				_ = s.syncLogRepo.Update(ctx, log)
			}
		}
		return nil
	}
	payload := &types.CodeWikiSyncPayload{
		TenantID:        ds.TenantID,
		KnowledgeBaseID: ds.KnowledgeBaseID,
		RepositoryIDs:   ids,
		SyncLogID:       syncLogID,
		Initiator:       types.TaskInitiatorFromContext(ctx),
	}
	if err := s.enqueueCodeWikiTask(ctx, types.TypeCodeWikiSync, payload, 30*time.Minute); err != nil {
		for _, b := range marked {
			b.Status = types.CodeWikiRepoStatusError
			b.ErrorMessage = "failed to enqueue sync task"
			_ = s.repo.UpdateRepositoryStatus(ctx, b)
		}
		return err
	}
	return nil
}

// finalizeCodeWikiSyncLog closes the linked data source sync log based on
// the per-binding outcomes.
func (s *codeWikiService) finalizeCodeWikiSyncLog(ctx context.Context, syncLogID string, failed int) {
	if syncLogID == "" {
		return
	}
	log, err := s.syncLogRepo.FindByID(ctx, syncLogID)
	if err != nil || log == nil {
		return
	}
	now := time.Now()
	switch {
	case failed > 0:
		log.Status = types.SyncLogStatusPartial
		log.ErrorMessage = fmt.Sprintf("%d repository(ies) failed to sync; see binding errors for details", failed)
	default:
		log.Status = types.SyncLogStatusSuccess
		log.ErrorMessage = ""
	}
	log.FinishedAt = &now
	if err := s.syncLogRepo.Update(ctx, log); err != nil {
		logger.Warnf(ctx, "code wiki sync: finalize sync log %s failed: %v", syncLogID, err)
	}
}
// ProcessCodeWikiSync is the asynq handler backing TypeCodeWikiSync: it
// syncs the payload's bindings (or all enabled bindings), preparing one
// snapshot + analysis artifact per repository, and finalizes the linked
// data source sync log.
func (s *codeWikiService) ProcessCodeWikiSync(ctx context.Context, task *asynq.Task) error {
	var payload types.CodeWikiSyncPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		logger.Errorf(ctx, "code wiki sync: bad payload: %v", err)
		return err
	}
	ctx = payload.Initiator.Apply(ctx)
	ctx = context.WithValue(ctx, types.TenantIDContextKey, payload.TenantID)

	// Re-validate the KB: it may have been deleted or retyped since submit.
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, payload.KnowledgeBaseID)
	if err != nil || kb == nil || !kb.IsCodeWiki() {
		logger.Warnf(ctx, "code wiki sync: KB %s gone or not code_wiki, canceling (err=%v)", payload.KnowledgeBaseID, err)
		return nil
	}
	bindings, err := s.repo.ListRepositories(ctx, types.CodeWikiRepositoryFilter{KnowledgeBaseID: payload.KnowledgeBaseID})
	if err != nil {
		return err
	}
	selected := make(map[string]struct{}, len(payload.RepositoryIDs))
	for _, id := range payload.RepositoryIDs {
		selected[id] = struct{}{}
	}
	profile, err := s.repo.GetAnalysisProfile(ctx, payload.TenantID, payload.KnowledgeBaseID)
	if err != nil {
		return err
	}
	// Resolve the provider once per sync run so a service disabled between
	// repositories stops the whole run with a clear error.
	provider, err := s.registry.Resolve(ctx, profile)
	if err != nil {
		markCodeWikiSyncError(ctx, s.repo, bindings, selected, err)
		s.finalizeCodeWikiSyncLog(ctx, payload.SyncLogID, len(selected))
		return nil
	}

	var failedCount int
	for _, binding := range bindings {
		if !binding.Enabled {
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[binding.ID]; !ok {
				continue
			}
		}
		if err := s.syncOneRepository(ctx, kb, binding, profile, provider); err != nil {
			logger.Errorf(ctx, "code wiki sync: repository %s (%s) failed: %v", binding.ID, binding.DisplayName, err)
			binding.Status = types.CodeWikiRepoStatusError
			binding.ErrorMessage = types.FormatCodeWikiError(err)
			_ = s.repo.UpdateRepositoryStatus(ctx, binding)
			failedCount++
		}
	}
	s.finalizeCodeWikiSyncLog(ctx, payload.SyncLogID, failedCount)
	// Per-repository failures are recorded on the bindings and surfaced in
	// the UI; the task itself succeeds so one bad repo doesn't block the
	// others or trigger blanket retries of already-synced repos.
	return nil
}

func markCodeWikiSyncError(ctx context.Context, repo interfaces.CodeWikiRepository, bindings []*types.CodeWikiRepository, selected map[string]struct{}, err error) {
	for _, b := range bindings {
		if !b.Enabled {
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[b.ID]; !ok {
				continue
			}
		}
		b.Status = types.CodeWikiRepoStatusError
		b.ErrorMessage = types.FormatCodeWikiError(err)
		_ = repo.UpdateRepositoryStatus(ctx, b)
	}
}

// syncOneRepository runs the full per-binding pipeline: resolve ref → reuse
// or download snapshot → prepare analysis artifact → update binding status.
func (s *codeWikiService) syncOneRepository(
	ctx context.Context,
	kb *types.KnowledgeBase,
	binding *types.CodeWikiRepository,
	profile *types.CodeWikiAnalysisProfile,
	provider codebase.CodeAnalysisProvider,
) error {
	binding.Status = types.CodeWikiRepoStatusSyncing
	binding.ErrorMessage = ""
	_ = s.repo.UpdateRepositoryStatus(ctx, binding)

	ds, err := s.dsService.GetDataSource(ctx, binding.SourceID)
	if err != nil || ds == nil || ds.KnowledgeBaseID != kb.ID {
		return fmt.Errorf("data source %s is no longer available for this knowledge base", binding.SourceID)
	}
	client, err := s.gitLabClientFor(ctx, ds)
	if err != nil {
		return err
	}
	// Step 1: resolve ref → commit SHA. Everything downstream uses the SHA.
	commitSHA, err := client.ResolveCommitSHA(ctx, binding.ProjectID, binding.Ref)
	if err != nil {
		return err
	}
	filterHash := codeWikiFilterHash(binding.IncludePaths, binding.ExcludePatterns)

	// Step 2: reuse an existing snapshot for (repo, commit, filters).
	snapshot, err := s.repo.FindReusableSnapshot(ctx, binding.ID, commitSHA, filterHash)
	snapshotReady := false
	if err == nil && snapshot != nil {
		// Verify the directory still exists (volume loss), else re-download.
		if info, statErr := os.Stat(snapshot.LocalPath); statErr == nil && info.IsDir() {
			snapshotReady = true
		}
	}
	if !snapshotReady {
		snapshot, err = s.downloadSnapshot(ctx, kb, binding, client, commitSHA, filterHash)
		if err != nil {
			return err
		}
	}

	// Step 3: prepare (or verify) the provider artifact for this snapshot
	// under the CURRENT profile version. Existing artifacts are verified —
	// same-named projects are never blindly reused across versions.
	artifact, err := s.repo.FindArtifact(ctx, snapshot.ID, profile.ConfigVersion)
	needPrepare := err != nil || artifact == nil || artifact.Status != types.CodeWikiArtifactStatusReady
	if needPrepare {
		scope := codebase.SnapshotScope{
			KnowledgeBaseID: kb.ID,
			SnapshotID:      snapshot.ID,
			RepositoryID:    binding.ID,
			CommitSHA:       commitSHA,
			FilterHash:      filterHash,
			LocalPath:       snapshot.LocalPath,
		}
		result, prepErr := provider.PrepareArtifact(ctx, scope)
		engineVersion := ""
		if result != nil {
			engineVersion = result.EngineVersion
		}
		if prepErr != nil {
			_ = s.recordArtifact(ctx, kb, binding, snapshot, profile,
				types.CodeWikiArtifactStatusFailed, engineVersion, "", nil, prepErr)
			return fmt.Errorf("prepare analysis artifact: %w", prepErr)
		}
		artifact = s.recordArtifact(ctx, kb, binding, snapshot, profile,
			types.CodeWikiArtifactStatusReady, result.EngineVersion, result.Handle, result.Coverage, nil)
	}

	// Step 4: publish binding readiness.
	now := time.Now().UTC()
	binding.Status = types.CodeWikiRepoStatusReady
	binding.LastSnapshotID = &snapshot.ID
	binding.LastCommitSHA = commitSHA
	binding.LastSyncAt = &now
	binding.ErrorMessage = ""
	if err := s.repo.UpdateRepositoryStatus(ctx, binding); err != nil {
		return err
	}
	logger.Infof(ctx, "code wiki sync: repository %s ready at %s (snapshot=%s artifact=%s)",
		binding.DisplayName, shortSHA(commitSHA), snapshot.ID, artifact.ID)
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func (s *codeWikiService) recordArtifact(
	ctx context.Context,
	kb *types.KnowledgeBase,
	binding *types.CodeWikiRepository,
	snapshot *types.CodeWikiSnapshot,
	profile *types.CodeWikiAnalysisProfile,
	status, engineVersion, handle string,
	coverage types.JSON,
	prepareErr error,
) *types.CodeWikiAnalysisArtifact {
	artifact := &types.CodeWikiAnalysisArtifact{
		ID:              uuid.NewString(),
		TenantID:        kb.TenantID,
		KnowledgeBaseID: kb.ID,
		RepositoryID:    binding.ID,
		SnapshotID:      snapshot.ID,
		ProfileVersion:  profile.ConfigVersion,
		EngineVersion:   engineVersion,
		ProviderHandle:  handle,
		CoverageSummary: coverage,
		Status:          status,
	}
	if prepareErr != nil {
		artifact.ErrorMessage = types.FormatCodeWikiError(prepareErr)
	}
	if err := s.repo.UpsertArtifact(ctx, artifact); err != nil {
		logger.Errorf(ctx, "code wiki: record artifact failed: %v", err)
	}
	return artifact
}

// downloadSnapshot downloads the repository archive at the fixed commit,
// unpacks under limits, applies filters, and lands the immutable snapshot:
// the filtered tree is staged under a temp dir and renamed to its final
// location BEFORE the row is inserted, so the row never exists without its
// directory (the reverse order would need a second path-fixing update on an
// otherwise-immutable row). A concurrent duplicate resolves to the winner's
// row and our directory is removed.
func (s *codeWikiService) downloadSnapshot(
	ctx context.Context,
	kb *types.KnowledgeBase,
	binding *types.CodeWikiRepository,
	client interface {
		DownloadArchive(ctx context.Context, projectID, commitSHA string) (io.ReadCloser, int64, error)
	},
	commitSHA, filterHash string,
) (*types.CodeWikiSnapshot, error) {
	dlCtx, cancel := context.WithTimeout(ctx, codeWikiDownloadBudget)
	defer cancel()

	body, _, err := client.DownloadArchive(dlCtx, binding.ProjectID, commitSHA)
	if err != nil {
		return nil, fmt.Errorf("download archive: %w", err)
	}
	defer body.Close()
	// Bound compressed bytes regardless of any server-reported length.
	limited := io.LimitReader(body, codeWikiMaxArchiveBytes()+1)

	root, err := ensureCodeWikiRoot()
	if err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(root, ".staging-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir under %s: %w", root, err)
	}
	defer os.RemoveAll(staging)

	fileCount, totalBytes, err := unpackCodeWikiArchive(limited, staging, binding.IncludePaths, binding.ExcludePatterns)
	if err != nil {
		return nil, err
	}

	snapshotID := uuid.NewString()
	finalDir := filepath.Join(root, snapshotRelPath(kb.TenantID, kb.ID, snapshotID))
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, finalDir); err != nil {
		return nil, fmt.Errorf("land snapshot directory: %w", err)
	}
	snapshot := &types.CodeWikiSnapshot{
		ID:              snapshotID,
		TenantID:        kb.TenantID,
		KnowledgeBaseID: kb.ID,
		RepositoryID:    binding.ID,
		CommitSHA:       commitSHA,
		FilterHash:      filterHash,
		LocalPath:       finalDir,
		FileCount:       fileCount,
		TotalSizeBytes:  totalBytes,
	}
	if err := s.repo.CreateSnapshot(ctx, snapshot); err != nil {
		// Row lost the race (or DB failed): our directory is orphaned from
		// the winner's — remove it to avoid a name-squatting duplicate.
		os.RemoveAll(finalDir)
		if existing, gErr := s.repo.FindReusableSnapshot(ctx, binding.ID, commitSHA, filterHash); gErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}
	logger.Infof(ctx, "code wiki sync: snapshot %s landed (%d files, %d bytes) for %s@%s",
		snapshot.ID, fileCount, totalBytes, binding.DisplayName, shortSHA(commitSHA))
	return snapshot, nil
}

// unpackCodeWikiArchive streams a tar.gz archive into dst, applying filter
// rules and enforcing size/file-count limits. The GitLab archive root
// directory (<project>-<sha>/) is stripped. Path traversal and non-regular
// files (symlinks/devices) are rejected outright — snapshots contain plain
// files only.
func unpackCodeWikiArchive(r io.Reader, dst string, includeRoots, excludePatterns []string) (int, int64, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, fmt.Errorf("open gzip archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var rootPrefix string
	fileCount := 0
	var totalBytes int64
	maxUnpacked := codeWikiMaxUnpackedBytes()
	maxFiles := codeWikiMaxFiles()

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, fmt.Errorf("read archive entry: %w", err)
		}
		name := path.Clean(header.Name)
		if name == "." || name == "/" {
			continue
		}
		// Determine the archive root on the first directory entry.
		if rootPrefix == "" {
			if idx := strings.IndexByte(name, '/'); idx > 0 {
				rootPrefix = name[:idx+1]
			} else if header.Typeflag == tar.TypeDir {
				rootPrefix = name + "/"
			}
		}
		rel := strings.TrimPrefix(name, rootPrefix)
		if rel == "" {
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue // directories are implied by file paths
		case tar.TypeReg:
			// fallthrough to file handling below
		default:
			return 0, 0, fmt.Errorf("archive entry %q: only regular files are allowed (type %c)", name, header.Typeflag)
		}
		if header.Size < 0 {
			return 0, 0, fmt.Errorf("archive entry %q: negative size", name)
		}
		if !codeWikiPathAllowed(rel, includeRoots, excludePatterns) {
			// Still drain the entry to keep the stream consistent.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return 0, 0, err
			}
			continue
		}
		fileCount++
		if fileCount > maxFiles {
			return 0, 0, fmt.Errorf("archive exceeds the maximum of %d files", maxFiles)
		}
		totalBytes += header.Size
		if totalBytes > maxUnpacked {
			return 0, 0, fmt.Errorf("unpacked archive exceeds %d bytes", maxUnpacked)
		}
		target, err := safeJoin(dst, rel)
		if err != nil {
			return 0, 0, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, 0, err
		}
		if err := writeBoundedFile(target, tr, header.Size); err != nil {
			return 0, 0, err
		}
	}
	return fileCount, totalBytes, nil
}

// safeJoin resolves rel inside dst and refuses traversal escapes.
func safeJoin(dst, rel string) (string, error) {
	clean := filepath.Clean("/" + rel)
	joined := filepath.Join(dst, clean)
	if !strings.HasPrefix(joined, filepath.Clean(dst)+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive path escapes destination: %s", rel)
	}
	return joined, nil
}

func writeBoundedFile(target string, r io.Reader, declared int64) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	written, err := io.Copy(f, io.LimitReader(r, declared+1))
	if err != nil {
		return err
	}
	if written != declared {
		return fmt.Errorf("archive entry %s: truncated (declared %d, got %d)", filepath.Base(target), declared, written)
	}
	return nil
}

// enabledPtr is the filter helper for *bool fields; named distinctly from
// the test package's boolPtr.
func enabledPtr(v bool) *bool { return &v }
