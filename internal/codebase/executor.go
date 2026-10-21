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
	return strings.TrimSpace(string(stdout)), nil
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
	if err := e.runJSON(ctx, builtinRunTimeout, &out, "cli", "index_status", "--project", project); err != nil {
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
}

// cliSearchCode runs a scoped code search.
func (e *builtinExecutor) cliSearchCode(ctx context.Context, project, query string, limit int) ([]SearchHit, error) {
	var out struct {
		Results []struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
			Symbol    string `json:"symbol"`
			Snippet   string `json:"snippet"`
		} `json:"results"`
	}
	args := []string{"cli", "search_code", "--project", project, "--query", query, "--limit", fmt.Sprint(limit)}
	if err := e.runJSON(ctx, builtinRunTimeout, &out, args...); err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(out.Results))
	for _, r := range out.Results {
		hits = append(hits, SearchHit{Path: r.Path, StartLine: r.StartLine, EndLine: r.EndLine, Symbol: r.Symbol, Snippet: r.Snippet})
	}
	return hits, nil
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
