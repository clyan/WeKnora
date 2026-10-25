package codebase

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltinPlanningStructureFencesAndRejectsTruncation(t *testing.T) {
	scope := SnapshotScope{KnowledgeBaseID: "kb", SnapshotID: "snapshot", RepositoryID: "repo", LocalPath: t.TempDir()}
	scope.ArtifactHandle = scope.ProjectID()
	bin := filepath.Join(t.TempDir(), "engine")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' \"$PLANNING_RESULT\"\n"), 0700))
	provider := &BuiltinProvider{exec: &builtinExecutor{bin: bin}}
	columns := []string{"kind", "name", "path", "line", "identity"}
	set := func(identity string, truncated bool) {
		blob, _ := json.Marshal(map[string]any{"columns": columns, "rows": [][]any{{`["Function"]`, "entry", "custom.ts", "9", identity}}, "truncated": truncated})
		t.Setenv("PLANNING_RESULT", string(blob))
	}
	set(scope.ProjectID()+".entry", false)
	hints, err := provider.PlanningStructure(context.Background(), scope, "symbols", QueryOptions{Limit: 2})
	require.NoError(t, err)
	require.Len(t, hints, 1)
	require.Equal(t, 9, hints[0].Line)
	set("foreign.entry", false)
	_, err = provider.PlanningStructure(context.Background(), scope, "symbols", QueryOptions{Limit: 2})
	require.ErrorContains(t, err, "another project")
	set(scope.ProjectID()+".entry", true)
	_, err = provider.PlanningStructure(context.Background(), scope, "symbols", QueryOptions{Limit: 2})
	require.ErrorContains(t, err, "truncated")
	scope.ArtifactHandle = "some-user-index"
	_, err = provider.PlanningStructure(context.Background(), scope, "symbols", QueryOptions{Limit: 2})
	require.ErrorContains(t, err, "owned frozen snapshot")
}
