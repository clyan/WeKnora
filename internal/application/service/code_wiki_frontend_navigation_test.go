package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestFrontendNavigationPrioritizesImplementationWithoutDroppingUnusualFiles(t *testing.T) {
	files := map[string]string{
		".dumirc.ts": "export default {};", ".eslintrc.js": "module.exports = {};",
		".prettierrc.js": "module.exports = {};", "package.json": `{"main":"unusual/start.ts"}`,
		"unusual/start.ts": "mount();", "routing/router.tsx": "export const routes = [];",
		"odd/order.screen": "submitOrder();",
	}
	for i := 0; i < 40; i++ {
		files[fmt.Sprintf("unusual/%02d.tsx", i)] = "export const View = () => <button/>;"
	}
	state, summaries := frontendCatalogFixture(t, files)
	state.cfg.MaxModelCalls = 1
	model := &codeWikiOutlineChat{responses: []string{frontendReply(t, frontendPlannerReply{Action: "read", Path: "unusual/start.ts", StartLine: 1, EndLine: 1})}}
	state.chat = model
	_, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorIs(t, err, errCodeWikiRunPaused)
	current := strings.Split(strings.Split(model.prompts[0], "Current batch:\n")[1], "\nCarried understanding:")[0]
	var batch []string
	require.NoError(t, json.Unmarshal([]byte(current), &batch))
	require.Equal(t, "package.json", batch[0])
	require.Equal(t, "routing/router.tsx", batch[1])
	require.NotContains(t, batch, ".dumirc.ts")
	require.NotContains(t, batch, ".eslintrc.js")
	all, err := frontendInventory(context.Background(), state.scopes["front"].LocalPath)
	require.NoError(t, err)
	ordered := frontendNavigationOrder(all)
	require.ElementsMatch(t, all, ordered)
	require.Contains(t, ordered, "odd/order.screen", "unknown formats/layouts stay visible and must be accounted")
}

func TestFrontendNavigationAccountsAssetsAndRetainsPublicImplementation(t *testing.T) {
	glyph := `window._iconfont_svg_string_1 = '<svg><symbol>` + strings.Repeat("glyph", 1100) + `</symbol></svg>';`
	state, summaries := frontendCatalogFixture(t, map[string]string{
		"entry.ts":               "import { send } from './public/lib/gateway'; export const run = () => send();",
		"public/lib/gateway.js":  "export const send = () => fetch('/orders');",
		"public/lib/iconfont.js": glyph, "public/icon.svg": "<svg/>", "styles/main.css": ".button {color:red}",
		"docs/old.md": "old business story", "src/mock/data.ts": "export const simulated = 1;",
	})
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	var captured []string
	state.captureInventory = func(_ context.Context, _ codebase.SnapshotScope, paths []string) error {
		captured = append(captured, paths...)
		return nil
	}
	model := &codeWikiOutlineChat{responses: []string{
		frontendReply(t, frontendPlannerReply{Action: "read", Path: "entry.ts", StartLine: 1, EndLine: 1}),
		frontendReply(t, frontendPlannerReply{Action: "read", Path: "public/lib/gateway.js", StartLine: 1, EndLine: 1}),
		frontendReply(t, frontendPlannerReply{Action: "finish", Pages: []codeWikiPlannedPage{
			frontendTopic("frontend_api", "order-call", "entry.ts", "public/lib/gateway.js"),
		}, Coverage: []frontendCoverage{{"entry.ts", "documented", "entry import"}, {"public/lib/gateway.js", "documented", "request implementation"}}}),
	}}
	state.chat = model
	pages, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages, 1)
	progress := state.cfg.FrontendPlanning.Repositories["front"]
	require.Len(t, progress.ExcludedPaths, 5)
	require.Equal(t, 7, progress.Cursor)
	require.Len(t, progress.Decisions, 7)
	require.Len(t, captured, 7, "automatic exclusions are captured, not silently lost")
	require.Contains(t, progress.Decisions["public/lib/iconfont.js"], "SVG")
	require.NotContains(t, strings.Join(model.prompts, "\n"), "glyphglyph", "asset payload never enters model context")
	require.NotContains(t, progress.ExcludedPaths, "public/lib/gateway.js")
	_, err = (&codeWikiService{}).readSource(context.Background(), state, state.scopes["front"], "public/lib/iconfont.js", 1, 1)
	require.ErrorContains(t, err, "SVG", "other source tools cannot use generated glyph payload as business evidence")
}

func TestFrontendNavigationResumesAccountedFilesWithoutAssumingPrefixOrder(t *testing.T) {
	state, _ := frontendCatalogFixture(t, map[string]string{"a.ts": "run();", "entry.ts": "mount();", "icon.svg": "<svg/>"})
	progress := &frontendRepoPlan{Cursor: 1, Decisions: map[string]string{"a.ts": "documented: prior batch"}}
	files := []string{"a.ts", "entry.ts", "icon.svg"}
	require.NoError(t, prepareFrontendNavigation(context.Background(), state, "front", files, progress))
	require.Equal(t, 2, progress.Cursor)
	require.Equal(t, "documented: prior batch", progress.Decisions["a.ts"])
	require.NotContains(t, progress.Decisions, "entry.ts", "reprioritization must not skip an unaccounted implementation")
	require.NoError(t, prepareFrontendNavigation(context.Background(), state, "front", files, progress))
	require.Equal(t, 2, progress.Cursor, "repeated preparation cannot double count assets")
}

func TestFrontendRepeatedReadsAccumulateAcrossNewConfigurationReads(t *testing.T) {
	files := map[string]string{".dumirc.ts": "export default {};"}
	responses := []string{frontendReply(t, frontendPlannerReply{Action: "read", Path: ".dumirc.ts", StartLine: 1, EndLine: 1})}
	for i := 0; i < frontendPlannerIdleLimit; i++ {
		path := fmt.Sprintf("config-%d.ts", i)
		files[path] = "export const config = {};"
		responses = append(responses,
			frontendReply(t, frontendPlannerReply{Action: "read", Path: path, StartLine: 1, EndLine: 1}),
			frontendReply(t, frontendPlannerReply{Action: "read", Path: ".dumirc.ts", StartLine: 1, EndLine: 80}),
		)
	}
	state, summaries := frontendCatalogFixture(t, files)
	model := &codeWikiOutlineChat{responses: responses}
	state.chat = model
	var saved []byte
	state.checkpoint = func(context.Context) error { var err error; saved, err = json.Marshal(state.cfg); return err }
	_, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorIs(t, err, errCodeWikiFrontendPlanningStalled)
	progress := state.cfg.FrontendPlanning.Repositories["front"]
	require.Equal(t, frontendPlannerIdleLimit, progress.RepeatedReads)
	require.Equal(t, 1, progress.IdleSteps, "new configurations reset consecutive idle but not accumulated repeats")
	require.Len(t, progress.ReadSpans, 9)
	require.Zero(t, progress.Cursor)
	require.Empty(t, progress.Pages)
	require.Contains(t, model.prompts[len(model.prompts)-1], "Reading another new configuration does not reset")
	var restored codeWikiRunConfig
	require.NoError(t, json.Unmarshal(saved, &restored))
	require.Equal(t, frontendPlannerIdleLimit, restored.FrontendPlanning.Repositories["front"].RepeatedReads)
}

func TestFrontendExplicitQuestionAllowsSourceRecovery(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"entry.ts": "mount();\n"})
	s, progress := &codeWikiService{}, &frontendRepoPlan{}
	read := frontendPlannerReply{Action: "read", Path: "entry.ts", StartLine: 1, EndLine: 80}
	allowed := map[string]bool{"entry.ts": true}
	require.NoError(t, s.applyFrontendAction(context.Background(), state, "front", read, summaries, nil, nil, allowed, progress))
	require.NoError(t, s.applyFrontendAction(context.Background(), state, "front", read, summaries, nil, nil, allowed, progress))
	require.Contains(t, progress.ToolResult, "reread deferred")
	require.NotContains(t, progress.ToolResult, "mount();")
	read.Question = "Recover the exact entry call after inspecting its dependency"
	require.NoError(t, s.applyFrontendAction(context.Background(), state, "front", read, summaries, nil, nil, allowed, progress))
	require.Contains(t, progress.ToolResult, "mount();")
	require.Contains(t, progress.ToolResult, "previously_read=true")
	require.Len(t, progress.ReadSpans, 1)
}
