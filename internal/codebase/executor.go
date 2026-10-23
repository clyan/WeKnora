package codebase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Builtin (builtin_codebase) adapter: drives the pinned codebase-memory-mcp
// executable through its `cli` subcommand surface. Confirmed commands (docs/
// CODEBASE_MEMORY_DOCKER.md, verified against 0.12.1 packaging):
//
//	codebase-memory-mcp --version
//	codebase-memory-mcp cli list_projects
//	codebase-memory-mcp cli index_repository --repo-path <dir> --name <project>
//	codebase-memory-mcp cli get_architecture --project <project>
//
// The remaining subcommands below (search_code, get_code_snippet, query_graph,
// index_status …) mirror the tool names the same engine exposes over MCP and
// are the ONE place to adjust after the stage-0 CLI contract verification
// required by the implementation plan. Nothing else in the codebase may
// hardcode argv.

const (
	// DefaultBuiltinCLIBin is the executable installed by
	// docker/Dockerfile.app-codebase. WEKNORA_CODEBASE_CLI_PATH overrides it
	// for dev setups (host binary, different version pin).
	DefaultBuiltinCLIBin = "codebase-memory-mcp"

	// builtinMaxOutputBytes caps a single command's captured stdout/stderr.
	// get_architecture on a large monorepo is the biggest legitimate
	// payload; anything beyond this is treated as a contract violation
	// rather than buffered.
	builtinMaxOutputBytes = 32 << 20 // 32 MiB

	// builtinRunTimeout bounds one CLI invocation. Long-running indexing
	// gets its own budget from the sync task; this covers queries.
	builtinRunTimeout = 5 * time.Minute

	// builtinIndexTimeout bounds repository indexing.
	builtinIndexTimeout = 40 * time.Minute

	// BuiltinSupportedVersion is the engine version the integration is
	// validated against (stage 0). Newer versions must be explicitly
	// re-verified before this constant moves.
	BuiltinSupportedVersion = "0.12.1"
)

// ErrBuiltinNotInstalled reports the CLI binary being absent. The
// capability check surfaces this as "builtin code analysis tool not
// installed" so operators know to build the extended image (or switch to a
// custom MCP provider).
var ErrBuiltinNotInstalled = errors.New("codebase: builtin codebase-memory-mcp executable not found (set WEKNORA_CODEBASE_CLI_PATH or install the extended image)")

// BuiltinCLIBinaryPath resolves the executable path. Empty environment
// falls back to DefaultBuiltinCLIBin (PATH lookup).
func BuiltinCLIBinaryPath() string {
	if p := strings.TrimSpace(os.Getenv("WEKNORA_CODEBASE_CLI_PATH")); p != "" {
		return p
	}
	return DefaultBuiltinCLIBin
}

// builtinExecutor runs one CLI command with argv-array execution (no shell),
// bounded output, and context-driven cancellation.
type builtinExecutor struct {
	bin string
}

func newBuiltinExecutor() *builtinExecutor {
	return &builtinExecutor{bin: BuiltinCLIBinaryPath()}
}

// runJSON executes the command and decodes stdout as JSON into out.
func (e *builtinExecutor) runJSON(ctx context.Context, timeout time.Duration, out interface{}, args ...string) error {
	stdout, _, err := e.run(ctx, timeout, args...)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(stdout, out); err != nil {
		return fmt.Errorf("codebase: %s: decode JSON output: %w", describeCmd(args), err)
	}
	return nil
}

// run executes the command and returns bounded stdout/stderr.
func (e *builtinExecutor) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, []byte, error) {
	if timeout <= 0 {
		timeout = builtinRunTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, e.bin, args...)
	// Argument-array execution only — command strings are never passed
	// through a shell, so no quoting-injection surface exists.
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// WaitDelay ensures a killed process's pipes cannot wedge the waiter.
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, stderr.Bytes(), fmt.Errorf("codebase: %s: timed out after %s", describeCmd(args), timeout)
	}
	if runCtx.Err() == context.Canceled {
		return nil, stderr.Bytes(), context.Canceled
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
			return nil, nil, ErrBuiltinNotInstalled
		}
		return nil, stderr.Bytes(), fmt.Errorf("codebase: %s: %w: %s", describeCmd(args), err, truncateTail(stderr.String(), 512))
	}
	if stdout.Len() > builtinMaxOutputBytes {
		return nil, stderr.Bytes(), fmt.Errorf("codebase: %s: output exceeds %d bytes", describeCmd(args), builtinMaxOutputBytes)
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

func describeCmd(args []string) string {
	if len(args) == 0 {
		return "cli"
	}
	return "cli " + args[0]
}

func truncateTail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// --- CLI command surface ----------------------------------------------------
//
// Everything argv-shaped lives here. Stage-0 verification of the 0.12.1
// contract may adjust flags/JSON field names in these helpers only.

// cliVersion returns the engine version string.
func (e *builtinExecutor) cliVersion(ctx context.Context) (string, error) {
	stdout, _, err := e.run(ctx, builtinRunTimeout, "--version")
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(stdout))
	version = strings.TrimPrefix(version, "codebase-memory-mcp ")
	return strings.TrimPrefix(version, "v"), nil
}

// cliListProjects returns the projects known to the engine.
func (e *builtinExecutor) cliListProjects(ctx context.Context) ([]builtinProjectInfo, error) {
	var out struct {
		Projects []builtinProjectInfo `json:"projects"`
	}
	if err := e.runJSON(ctx, builtinRunTimeout, &out, "cli", "list_projects"); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

type builtinProjectInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

// cliIndexRepository indexes one repository directory under a project name.
func (e *builtinExecutor) cliIndexRepository(ctx context.Context, repoPath, project string) error {
	_, _, err := e.run(ctx, builtinIndexTimeout,
		"cli", "index_repository", "--repo-path", repoPath, "--name", project)
	return err
}

// cliIndexStatus returns the index state of one project.
func (e *builtinExecutor) cliIndexStatus(ctx context.Context, project string) (*builtinIndexStatus, error) {
	var out builtinIndexStatus
	if err := e.runJSON(ctx, builtinRunTimeout, &out, "cli", "index_status", "--project", project, "--format", "json"); err != nil {
		return nil, err
	}
	return &out, nil
}

type builtinIndexStatus struct {
	Project   string `json:"project"`
	Indexed   bool   `json:"indexed"`
	Version   string `json:"version,omitempty"`
	Nodes     int    `json:"nodes,omitempty"`
	Edges     int    `json:"edges,omitempty"`
	IndexedAt string `json:"indexed_at,omitempty"`
	Status    string `json:"status"`
}

// cliSearchCode runs a scoped code search.
func (e *builtinExecutor) cliSearchCode(ctx context.Context, project, query string, limit int) ([]SearchHit, error) {
	var out struct {
		Cols       []string            `json:"cols"`
		Rows       [][]json.RawMessage `json:"rows"`
		RawMatches struct {
			Cols []string            `json:"cols"`
			Rows [][]json.RawMessage `json:"rows"`
		} `json:"raw_matches"`
	}
	args := []string{"cli", "search_code", "--project", project, "--pattern", query, "--limit", fmt.Sprint(limit), "--format", "json"}
	if err := e.runJSON(ctx, builtinRunTimeout, &out, args...); err != nil {
		return nil, err
	}
	if len(out.Cols) == 0 {
		return nil, fmt.Errorf("codebase: search_code returned no column schema")
	}
	var hits []SearchHit
	for _, row := range out.Rows {
		cells := tableRow(out.Cols, row)
		var hit SearchHit
		_ = json.Unmarshal(cells["file"], &hit.Path)
		_ = json.Unmarshal(cells["qn"], &hit.Symbol)
		var lines string
		_ = json.Unmarshal(cells["lines"], &lines)
		_, _ = fmt.Sscanf(lines, "%d-%d", &hit.StartLine, &hit.EndLine)
		if hit.EndLine < hit.StartLine {
			hit.EndLine = hit.StartLine
		}
		if hit.Path != "" {
			hits = append(hits, hit)
		}
	}
	for _, row := range out.RawMatches.Rows {
		if len(hits) >= limit {
			break
		}
		cells := tableRow(out.RawMatches.Cols, row)
		var hit SearchHit
		_ = json.Unmarshal(cells["file"], &hit.Path)
		_ = json.Unmarshal(cells["line"], &hit.StartLine)
		_ = json.Unmarshal(cells["content"], &hit.Snippet)
		hit.EndLine = hit.StartLine
		if hit.Path != "" {
			hits = append(hits, hit)
		}
	}
	return hits, nil
}

func tableRow(columns []string, row []json.RawMessage) map[string]json.RawMessage {
	cells := make(map[string]json.RawMessage, len(columns))
	for i, name := range columns {
		if i < len(row) {
			cells[name] = row[i]
		}
	}
	return cells
}

// cliLinkRepositories limits native linking to the caller's frozen snapshot set.
// Never use target_projects=["*"]: the engine store can contain other tenants.
func (e *builtinExecutor) cliLinkRepositories(ctx context.Context, scope SnapshotScope, targets []string) error {
	args, err := json.Marshal(map[string]any{
		"repo_path": scope.LocalPath, "name": scope.ArtifactHandle,
		"mode": "cross-repo-intelligence", "target_projects": targets,
	})
	if err != nil {
		return err
	}
	var result struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := e.runJSON(ctx, builtinRunTimeout, &result, "cli", "index_repository", string(args)); err != nil {
		return err
	}
	if result.Status != "success" {
		return fmt.Errorf("native cross-repository linking: %s: %s", result.Status, result.Error)
	}
	return nil
}

func (e *builtinExecutor) cliCrossRelations(ctx context.Context, project string, limit int) ([]map[string]json.RawMessage, error) {
	query := fmt.Sprintf("MATCH (a)-[r:CROSS_HTTP_CALLS|CROSS_GRPC_CALLS|CROSS_GRAPHQL_CALLS|CROSS_TRPC_CALLS|CROSS_ASYNC_CALLS|CROSS_CHANNEL]->(b) RETURN type(r) AS kind, a.qualified_name AS from_symbol, a.file_path AS from_path, a.start_line AS from_line, b.qualified_name AS to_symbol, b.file_path AS to_path, b.start_line AS to_line LIMIT %d", limit)
	var out struct {
		Columns []string            `json:"columns"`
		Rows    [][]json.RawMessage `json:"rows"`
	}
	if err := e.runJSON(ctx, builtinRunTimeout, &out, "cli", "query_graph", "--project", project, "--query", query, "--format", "json"); err != nil {
		return nil, err
	}
	if len(out.Columns) == 0 {
		return nil, fmt.Errorf("codebase: cross-repository graph returned no column schema")
	}
	var rows []map[string]json.RawMessage
	for _, row := range out.Rows {
		rows = append(rows, tableRow(out.Columns, row))
	}
	return rows, nil
}

// cliGetCodeSnippet returns a source excerpt.
func (e *builtinExecutor) cliGetCodeSnippet(ctx context.Context, project, path string, startLine, endLine int) (string, error) {
	var out struct {
		Content string `json:"content"`
	}
	args := []string{"cli", "get_code_snippet", "--project", project, "--file", path,
		"--start-line", fmt.Sprint(startLine), "--end-line", fmt.Sprint(endLine)}
	if err := e.runJSON(ctx, builtinRunTimeout, &out, args...); err != nil {
		return "", err
	}
	return out.Content, nil
}

// cliGetArchitecture returns the repository structure summary.
func (e *builtinExecutor) cliGetArchitecture(ctx context.Context, project string) (json.RawMessage, error) {
	stdout, _, err := e.run(ctx, builtinRunTimeout, "cli", "get_architecture", "--project", project)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(stdout), nil
}
