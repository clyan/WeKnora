package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestFrontendPlanningRepeatedReadsPauseAndResumeWithoutSkipping(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{
		".dumirc.ts": "export default {\n  proxy: {},\n  theme: {}\n};\n",
		"entry.ts":   "export const entry = mount();\n",
	})
	responses := []string{frontendReply(t, frontendPlannerReply{Action: "read", Path: ".dumirc.ts", StartLine: 1, EndLine: 80})}
	for i := 0; i < frontendPlannerIdleLimit; i++ {
		reply := frontendPlannerReply{Action: "read", Path: ".dumirc.ts", StartLine: 2, EndLine: 3, Question: "Recover the exact proxy option for the entry integration topic", Notes: strings.Repeat("verify config ", i+1)}
		if i%2 == 1 {
			reply = frontendPlannerReply{Action: "list", Offset: 0}
		}
		responses = append(responses, frontendReply(t, reply))
	}
	model := &codeWikiOutlineChat{responses: responses}
	state.chat = model
	var saved []byte
	state.checkpoint = func(context.Context) error {
		var err error
		saved, err = json.Marshal(state.cfg)
		return err
	}
	s := &codeWikiService{}
	_, err := s.planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorIs(t, err, errCodeWikiFrontendPlanningStalled)
	require.ErrorIs(t, err, errCodeWikiRunPaused)
	progress := state.cfg.FrontendPlanning.Repositories["front"]
	require.Equal(t, frontendPlannerIdleLimit, progress.IdleSteps)
	require.Zero(t, progress.Cursor)
	require.Empty(t, progress.Pages)
	require.Len(t, progress.ReadSpans, 2, "exact repeats do not grow the checkpoint; covered subranges remain selectable citations")
	require.Contains(t, model.prompts[3], "Recovery: 2 consecutive actions")
	require.Contains(t, model.prompts[1], "eof=true, previously_read=false")
	require.Contains(t, model.prompts[2], "previously_read=true")
	require.NotContains(t, model.prompts[1], "excerpt, may be truncated")
	require.Contains(t, model.prompts[1], `"end_line":4`)

	// Simulate a new process restoring the persisted checkpoint. Resume is a
	// bounded opportunity to recover, not a forced finish of unreviewed files.
	var restored codeWikiRunConfig
	require.NoError(t, json.Unmarshal(saved, &restored))
	state.cfg = restored
	state.modelCallsAtStart = state.run.ModelCalls
	model = &codeWikiOutlineChat{responses: []string{
		frontendReply(t, frontendPlannerReply{Action: "reads", Offset: 0}),
		frontendReply(t, frontendPlannerReply{Action: "read", Path: "entry.ts", StartLine: 1, EndLine: 80}),
		frontendReply(t, frontendPlannerReply{Action: "finish", Pages: []codeWikiPlannedPage{
			frontendTopic("frontend_architecture", "entry", ".dumirc.ts", "entry.ts"),
		}, Coverage: []frontendCoverage{{".dumirc.ts", "documented", "config"}, {"entry.ts", "documented", "entry"}}}),
	}}
	state.chat = model
	pages, err := s.planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Len(t, pages, 1)
	require.Equal(t, 2, state.cfg.FrontendPlanning.Repositories["front"].Cursor)
	require.Contains(t, model.prompts[1], "Successful read ledger offset=0")
	require.Contains(t, state.run.Phase, "已阅读 2 个文件")
}

func TestFrontendPlanningNewRangesDoNotTriggerIdleGuard(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"large.ts": strings.Repeat("run();\n", 12)})
	model := &codeWikiOutlineChat{}
	for i := 1; i <= 12; i++ {
		model.responses = append(model.responses, frontendReply(t, frontendPlannerReply{Action: "read", Path: "large.ts", StartLine: i, EndLine: i}))
	}
	model.responses = append(model.responses, frontendReply(t, frontendPlannerReply{Action: "finish", Pages: []codeWikiPlannedPage{
		frontendTopic("frontend_architecture", "large", "large.ts"),
	}, Coverage: []frontendCoverage{{"large.ts", "documented", "implementation"}}}))
	state.chat = model
	_, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	require.Zero(t, state.cfg.FrontendPlanning.Repositories["front"].IdleSteps)
	require.NotContains(t, strings.Join(model.prompts, "\n"), "Recovery:")
	require.True(t, frontendRangeCovered(state.cfg.FrontendPlanning.Repositories["front"].ReadSpans, codeWikiSourceSpan{"large.ts", 1, 12}))
}

func TestFrontendReadRecordsOnlyReturnedWholeLines(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{
		"large.ts": strings.Repeat(strings.Repeat("x", 4500)+"\n", 3),
		"long.ts":  strings.Repeat("x", 10001),
	})
	s, progress := &codeWikiService{}, &frontendRepoPlan{}
	allowed := map[string]bool{"large.ts": true, "long.ts": true}
	read := frontendPlannerReply{Action: "read", Path: "large.ts", StartLine: 1, EndLine: 3}
	err := s.applyFrontendAction(context.Background(), state, "front", read, summaries, nil, nil, allowed, progress)
	require.NoError(t, err)
	require.Equal(t, []codeWikiSourceSpan{{"large.ts", 1, 2}}, progress.ReadSpans)
	require.Contains(t, progress.ToolResult, "total_lines=3, truncated=true, eof=false")
	page := frontendTopic("frontend_architecture", "large", "large.ts")
	page.SourceSpans = []codeWikiSourceSpan{{"large.ts", 1, 3}}
	err = finishFrontendBatch(state, "front", frontendPlannerReply{Pages: []codeWikiPlannedPage{page}}, summaries, nil, progress)
	require.ErrorContains(t, err, "not actually read")
	read.StartLine = 3
	require.NoError(t, s.applyFrontendAction(context.Background(), state, "front", read, summaries, nil, nil, allowed, progress))
	require.Contains(t, progress.ToolResult, "truncated=false, eof=true")
	require.Equal(t, codeWikiSourceSpan{"large.ts", 3, 3}, progress.ReadSpans[1])
	read.Path, read.StartLine, read.EndLine = "long.ts", 1, 1
	require.ErrorContains(t, s.applyFrontendAction(context.Background(), state, "front", read, summaries, nil, nil, allowed, progress), "exceeds the tool return budget")
	require.Len(t, progress.ReadSpans, 2, "an unreturned long line cannot become evidence")
}

func TestFrontendReadPreservesBlankLinesAndIndentation(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"entry.ts": "\n\n  export const value = 1;\n\n"})
	state.cfg.SourcePolicyVersion = types.CodeWikiSourcePolicyVersion
	progress := &frontendRepoPlan{}
	err := (&codeWikiService{}).applyFrontendAction(context.Background(), state, "front",
		frontendPlannerReply{Action: "read", Path: "entry.ts", StartLine: 3, EndLine: 80},
		summaries, nil, nil, map[string]bool{"entry.ts": true}, progress)
	require.NoError(t, err)
	require.Contains(t, progress.ToolResult, "Source entry.ts:3-4")
	require.Contains(t, progress.ToolResult, "total_lines=4, truncated=false, eof=true")
	require.Contains(t, progress.ToolResult, "\n  export const value = 1;\n")
	require.Equal(t, []codeWikiSourceSpan{{"entry.ts", 3, 4}}, progress.ReadSpans)
}

func TestFrontendPlanningDoesNotSilentlyTruncateToolEvidence(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"entry.ts": "mount();"})
	model := &codeWikiOutlineChat{}
	state.chat = model
	progress := &frontendRepoPlan{ToolResult: strings.Repeat("完整证据", codeWikiMaxContextChars)}
	original := progress.ToolResult
	err := (&codeWikiService{}).planFrontendBatch(context.Background(), state, "front", summaries["front"], summaries,
		[]string{"entry.ts"}, []string{"entry.ts"}, map[string]bool{"entry.ts": true}, progress, "Chinese")
	require.ErrorIs(t, err, errCodeWikiFrontendPlanningContext)
	require.ErrorIs(t, err, errCodeWikiRunPaused)
	require.Empty(t, model.prompts, "no truncated evidence is sent to the model")
	require.Equal(t, original, progress.ToolResult)
}
