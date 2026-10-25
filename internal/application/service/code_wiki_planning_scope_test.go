package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type planningScopeChat struct {
	codeWikiOutlineModel
	beforeReply func()
}

func (m *planningScopeChat) Chat(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
	m.beforeReply()
	return &types.ChatResponse{Content: moduleGroupReply([]string{"m0"}, "scope")}, nil
}

func TestPlanningScopePersistsBeforeModelAndSurvivesRefresh(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{
		"odd/a.ts": "export const a = 'implementation text';", "odd/b.ts": "export const b=1;", "README.md": "old document",
	})
	state.cfg.TopicVersion, state.cfg.SourcePolicyVersion = 1, 1
	state.provider = &frontendIndexProvider{}
	var persisted []byte
	state.checkpoint = func(context.Context) error {
		var err error
		persisted, err = json.Marshal(state.cfg)
		return err
	}
	manifest := []*types.CodeWikiRunRepository{{RepositoryID: "front", SnapshotID: "snapshot"}}
	var activeID string
	state.chat = &planningScopeChat{beforeReply: func() {
		fresh := &types.CodeWikiRun{ConfigSnapshot: append(types.JSON(nil), persisted...)}
		require.NoError(t, decorateCodeWikiRunDetail(fresh, manifest, nil, nil))
		require.NotNil(t, fresh.PlanningBatch, "scope must be visible while waiting for the model")
		batch := fresh.PlanningBatch
		require.False(t, batch.Completed)
		require.Equal(t, "app/main", batch.RepositoryName)
		require.Equal(t, 2, batch.FileCount)
		require.Len(t, batch.Modules, 1)
		require.Equal(t, []string{"odd/a.ts", "odd/b.ts"}, batch.Modules[0].Files)
		activeID = batch.ID
		public, err := json.Marshal(batch)
		require.NoError(t, err)
		require.NotContains(t, string(public), "implementation text")
		require.NotContains(t, string(public), "README.md")
	}}
	_, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.NoError(t, err)
	fresh := &types.CodeWikiRun{ConfigSnapshot: persisted}
	require.NoError(t, decorateCodeWikiRunDetail(fresh, manifest, nil, nil))
	require.True(t, fresh.PlanningBatch.Completed)
	require.Equal(t, activeID, fresh.PlanningBatch.ID, "polling completion must not change the batch identity")
	// Old runs do not invent a scope. Wrong snapshots do not expose a scope.
	for _, input := range []struct {
		config   types.JSON
		manifest []*types.CodeWikiRunRepository
	}{
		{types.JSON(`{}`), manifest},
		{persisted, []*types.CodeWikiRunRepository{{RepositoryID: "front", SnapshotID: "another-snapshot"}}},
	} {
		fresh.ConfigSnapshot = input.config
		require.NoError(t, decorateCodeWikiRunDetail(fresh, input.manifest, nil, nil))
		require.Nil(t, fresh.PlanningBatch)
	}
}

func TestPlanningScopePersistenceFailurePreventsModelCall(t *testing.T) {
	state, summaries := frontendCatalogFixture(t, map[string]string{"a.ts": "export const a=1;"})
	state.cfg.TopicVersion, state.cfg.SourcePolicyVersion = 1, 1
	state.provider = &frontendIndexProvider{}
	state.checkpoint = func(context.Context) error { return fmt.Errorf("scope checkpoint unavailable") }
	// No model is installed: the request must stop before any call is made.
	_, err := (&codeWikiService{}).planFrontendCatalog(context.Background(), state, summaries, "Chinese")
	require.ErrorContains(t, err, "scope checkpoint unavailable")
	require.Zero(t, state.run.ModelCalls)
}
