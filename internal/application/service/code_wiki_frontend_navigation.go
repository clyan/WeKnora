package service

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Hints change only reading order, never topic type or evidence eligibility.
// Every unaccounted file remains in the queue, including unfamiliar layouts.
func frontendNavigationPriority(path string) int {
	base := strings.ToLower(filepath.Base(path))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if base == "package.json" {
		return 0
	}
	if strings.HasPrefix(base, ".") || strings.Contains(stem, "config") || strings.HasSuffix(stem, "rc") {
		return 4
	}
	switch filepath.Ext(base) {
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		if strings.HasSuffix(base, ".d.ts") {
			return 3
		}
		if stem == "main" || stem == "app" || stem == "entry" || stem == "bootstrap" || strings.Contains(stem, "router") || strings.HasPrefix(stem, "routes") {
			return 1
		}
		return 2
	}
	return 5
}

func frontendNavigationOrder(files []string) []string {
	ordered := append([]string(nil), files...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return frontendNavigationPriority(ordered[i]) < frontendNavigationPriority(ordered[j])
	})
	return ordered
}

func frontendStaticResourceReason(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp", ".avif", ".woff", ".woff2", ".ttf", ".eot", ".mp3", ".mp4", ".webm", ".map":
		return "图像、字体、媒体或映射资源；不展开资源内容，使用关系从调用方实现取证"
	case ".css", ".scss", ".sass", ".less":
		return "样式资源；不将样式内容作为操作、权限或接口参数证据，动态使用条件从实现取证"
	}
	return ""
}

func prepareFrontendNavigation(ctx context.Context, state *codeWikiRunState, id string, files []string, progress *frontendRepoPlan) (prepareErr error) {
	done := state.beginActivity(ctx, "整理源码阅读顺序并识别静态资源 · "+state.repositoryLabel(id))
	defer func() { done(prepareErr) }()
	if progress.Decisions == nil {
		progress.Decisions = map[string]string{}
	}
	if progress.ExcludedPaths == nil {
		progress.ExcludedPaths = map[string]string{}
	}
	if len(progress.Decisions) != progress.Cursor {
		return fmt.Errorf("planning checkpoint disagrees with accounted inventory")
	}
	var excluded []string
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, accounted := progress.Decisions[path]; accounted {
			continue
		}
		reason := frontendStaticResourceReason(path)
		if state.cfg.SourcePolicyVersion > 0 {
			if role, why := codeWikiSourceRole(path); role == "excluded" {
				reason = why
			}
			if reason == "" {
				// Cheap navigation filter first; only plausible generated glyph
				// scripts invoke the parser. Comments/filenames alone cannot exclude.
				ext := strings.ToLower(filepath.Ext(path))
				if ext == ".js" || ext == ".mjs" || ext == ".cjs" {
					prefix := readSnapshotSourceBounded(state.scopes[id].LocalPath, path, 4096)
					if strings.Contains(prefix, "_iconfont_svg_string_") && strings.Contains(prefix, "<svg") {
						if err := verifySnapshotFileLines(state.scopes[id].LocalPath, path, 1, 1); err != nil {
							return err
						}
						raw := readSnapshotSourceBounded(state.scopes[id].LocalPath, path, (2<<20)+1)
						if len(raw) <= 2<<20 {
							parsed, err := analyzeCachedCodeWikiSource(ctx, state, state.scopes[id], path, raw)
							if err != nil {
								return err
							}
							if parsed.Kind == "asset" && len(parsed.Diagnostics) == 0 {
								reason = parsed.Reason
							}
						}
					}
				}
			}
		}
		if reason != "" {
			progress.ExcludedPaths[path] = reason
			excluded = append(excluded, path)
		}
	}
	// Record inventory capture before accounting for exclusions. No implementation
	// is claimed analyzed, and no per-file model call is needed for known assets.
	for offset := 0; offset < len(excluded); {
		batch := frontendInventoryBatch(excluded, offset)
		if state.captureInventory != nil {
			if err := state.captureInventory(ctx, state.scopes[id], batch); err != nil {
				return err
			}
		}
		for _, path := range batch {
			progress.Decisions[path] = "irrelevant: " + progress.ExcludedPaths[path]
			progress.Cursor++
		}
		offset += len(batch)
	}
	if len(excluded) > 0 {
		recorded := state.beginActivity(ctx, fmt.Sprintf("已记录 %d 个静态资源或不合格来源的排除原因，继续分析实现源码", len(excluded)))
		recorded(nil)
	}
	if state.checkpoint != nil && len(excluded) > 0 {
		return state.checkpoint(ctx)
	}
	return nil
}
