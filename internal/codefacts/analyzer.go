package codefacts

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed analyzer.cjs
var analyzerScript string

const Version = "typescript-6.0.3/v2"

type Result struct {
	Version     string       `json:"version"`
	Source      string       `json:"source"`
	Kind        string       `json:"kind"`
	Reason      string       `json:"reason"`
	Nodes       []Node       `json:"nodes"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type Node struct {
	Kind        string     `json:"kind"`
	StartOffset int        `json:"start_offset"`
	EndOffset   int        `json:"end_offset"`
	StartLine   int        `json:"start_line"`
	EndLine     int        `json:"end_line"`
	Owner       string     `json:"owner,omitempty"`
	Enclosing   []Branch   `json:"enclosing,omitempty"`
	Name        string     `json:"name,omitempty"`
	Callee      string     `json:"callee,omitempty"`
	Module      string     `json:"module,omitempty"`
	Bindings    string     `json:"bindings,omitempty"`
	Initializer string     `json:"initializer,omitempty"`
	Expression  string     `json:"expression,omitempty"`
	WhenTrue    string     `json:"when_true,omitempty"`
	WhenFalse   string     `json:"when_false,omitempty"`
	Parameters  []string   `json:"parameters,omitempty"`
	Arguments   []string   `json:"arguments,omitempty"`
	Properties  []Property `json:"properties,omitempty"`
}
type Branch struct {
	Expression string `json:"expression"`
	Branch     string `json:"branch"`
}
type Property struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Spread bool   `json:"spread"`
}
type Diagnostic struct {
	StartLine int    `json:"start_line"`
	Message   string `json:"message"`
}

// ModulePath resolves only platform-owned installations, never target project dependencies.
func ModulePath() (string, error) {
	paths := []string{os.Getenv("CODE_WIKI_TYPESCRIPT_MODULE"), "/opt/weknora-code-analysis/node_modules/typescript"}
	// Development fallback is anchored to this application's compiled source
	// location, never the worker's cwd or the analyzed repository's go.mod.
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		paths = append(paths, filepath.Clean(filepath.Join(filepath.Dir(file), "../../frontend/node_modules/typescript")))
	}
	for _, path := range paths {
		if path != "" && filepath.IsAbs(path) {
			if _, err := os.Stat(filepath.Join(path, "lib/typescript.js")); err == nil {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("源码分析器不可用：需要平台安装的 TypeScript 6.0.3；不会使用目标仓库依赖")
}

func Analyze(ctx context.Context, path, source string) (*Result, error) {
	if len(source) > 2<<20 {
		return nil, fmt.Errorf("source exceeds analyzer limit")
	}
	module, err := ModulePath()
	if err != nil {
		return nil, err
	}
	result, err := invoke(ctx, module, path, source, false)
	if err != nil {
		return nil, err
	}
	if result.Kind != "implementation" {
		return result, nil
	}
	filtered, err := invoke(ctx, module, path, result.Source, true)
	if err != nil {
		return nil, err
	}
	if len(result.Diagnostics) == 0 && len(filtered.Diagnostics) > 0 && !strings.HasSuffix(path, ".json") {
		return nil, fmt.Errorf("comment filtering could not preserve source syntax")
	}
	// Diagnostics from the actual source are authoritative; filtering metadata may leave JSON placeholders.
	filtered.Diagnostics = result.Diagnostics
	return filtered, nil
}

// CheckRuntime verifies the parser version without importing or executing any project code.
func CheckRuntime(ctx context.Context) error {
	module, err := ModulePath()
	if err != nil {
		return err
	}
	_, err = invoke(ctx, module, "health.ts", "const analyzerReady = true;", false)
	return err
}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 12<<20 {
		b.exceeded = true
		return 0, fmt.Errorf("analysis output too large")
	}
	return b.Buffer.Write(p)
}

func invoke(ctx context.Context, module, path, source string, filtered bool) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	input, _ := json.Marshal(map[string]any{"path": path, "source": source, "filtered": filtered})
	cmd := exec.CommandContext(ctx, "node", "--max-old-space-size=256", "-e", analyzerScript, module)
	cmd.Stdin = bytes.NewReader(input)
	// Remove host Node injection hooks and module search paths.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "NODE_OPTIONS=") && !strings.HasPrefix(entry, "NODE_PATH=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	var out boundedBuffer
	var stderr boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("源码分析器执行失败：%w", err)
	}
	var result Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return nil, err
	}
	if result.Version != Version {
		return nil, fmt.Errorf("source analyzer version mismatch")
	}
	return &result, nil
}
