package codebase

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltinIndexFailureIncludesOperationAndCause(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codebase-memory-mcp")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho 'cannot write graph: no space left on device' >&2\nexit 1\n"), 0o700))
	executor := &builtinExecutor{bin: bin}
	err := executor.cliIndexRepository(context.Background(), t.TempDir(), "test-index")
	require.ErrorContains(t, err, "cli index_repository")
	require.ErrorContains(t, err, "exit status 1")
	require.ErrorContains(t, err, "no space left on device")
}
