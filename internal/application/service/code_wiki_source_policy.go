package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/codefacts"
	"github.com/Tencent/WeKnora/internal/types"
)

func markCodeWikiSourceCompatibility(run *types.CodeWikiRun) {
	if run == nil {
		return
	}
	var cfg codeWikiRunConfig
	run.RequiresSourceRebuild = json.Unmarshal(run.ConfigSnapshot, &cfg) != nil || validateCodeWikiRunPolicy(cfg) != nil
	run.FullSourceReanalysis = cfg.FullSourceReanalysis
}

func codeWikiSourceRole(path string) (string, string) {
	name := strings.ToLower(filepath.ToSlash(path))
	for _, segment := range strings.Split(name, "/") {
		switch segment {
		case "mock", "mocks", "__mocks__", "fixtures", "__fixtures__", "samples", "examples", "test", "tests", "__tests__", "docs", "node_modules", "vendor", "dist", "build", "coverage":
			return "excluded", "非业务实现来源：" + segment
		}
	}
	base := filepath.Base(name)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(base, ".stories.") || strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "readme") {
		return "excluded", "文档、测试或样例不能证明业务行为"
	}
	if strings.HasSuffix(base, ".d.ts") {
		return "declaration", "仅类型声明，不证明行为"
	}
	switch filepath.Ext(base) {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return "implementation", ""
	case ".json":
		if base == "package.json" || strings.HasPrefix(base, "tsconfig") {
			return "configuration", "仅配置声明，需核对使用关系"
		}
	}
	return "unsupported", "当前分析器不支持此文件格式或配置类型"
}

func qualifiedCodeWikiSource(ctx context.Context, state *codeWikiRunState, scope codebase.SnapshotScope, path string) (*codefacts.Result, error) {
	role, reason := codeWikiSourceRole(path)
	if role == "excluded" || role == "unsupported" {
		return nil, fmt.Errorf("%s：%s", path, reason)
	}
	if err := verifySnapshotFileLines(scope.LocalPath, path, 1, 1); err != nil {
		return nil, err
	}
	key := scope.RepositoryID + "\x00" + scope.SnapshotID + "\x00" + path
	if result := state.qualifiedSources[key]; result != nil {
		return result, nil
	}
	info, err := os.Stat(filepath.Join(scope.LocalPath, filepath.FromSlash(path)))
	if err != nil {
		return nil, err
	}
	if info.Size() > 2<<20 {
		return nil, fmt.Errorf("实现文件超过当前分析器大小范围，必须记录未支持")
	}
	raw := readSnapshotFileBounded(scope.LocalPath, path, 2<<20)
	result, err := codefacts.Analyze(ctx, path, raw)
	if err != nil {
		return nil, err
	}
	if result.Kind != "implementation" {
		if state.captureSource != nil {
			if err := state.captureSource(ctx, scope, path, result.Kind, result.Reason, nil); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("%s：%s", path, result.Reason)
	}
	if len(result.Diagnostics) > 0 {
		if state.captureSource != nil {
			if err := state.captureSource(ctx, scope, path, "unsupported", "源码语法无法完整解析", nil); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("%s：源码语法无法完整解析，必须记录未支持", path)
	}
	if role == "declaration" {
		result.Kind = "declaration"
	}
	if role == "configuration" {
		result.Kind = "configuration"
	}
	if state.captureSource != nil {
		if err := state.captureSource(ctx, scope, path, role, reason, result); err != nil {
			return nil, err
		}
	}
	if state.qualifiedSources == nil {
		state.qualifiedSources = make(map[string]*codefacts.Result)
	}
	state.qualifiedSources[key] = result
	return result, nil
}

func validateCodeWikiRunPolicy(cfg codeWikiRunConfig) error {
	if !types.IsValidCodeWikiMode(cfg.Mode) {
		return fmt.Errorf("未知的知识库生成模式")
	}
	if cfg.Mode == types.CodeWikiModeDomain {
		return fmt.Errorf("领域生成尚未开放；不能使用项目代码执行器")
	}
	if cfg.SourcePolicyVersion != types.CodeWikiSourcePolicyVersion || cfg.FactSchemaVersion != types.CodeWikiFactSchemaVersion || cfg.SourceAnalyzerVersion != types.CodeWikiSourceAnalyzerVersion {
		return fmt.Errorf("此任务使用旧的源码证据规则，无法继续或发布；请停止旧任务并创建重建任务，原有内容仍可查看")
	}
	return nil
}
