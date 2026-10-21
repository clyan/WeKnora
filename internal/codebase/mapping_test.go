package codebase

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestValidateToolMappings(t *testing.T) {
	tests := []struct {
		name    string
		mapping []types.CodeWikiToolMapping
		wantErr bool
	}{
		{
			name: "valid search mapping with context variables",
			mapping: []types.CodeWikiToolMapping{{
				Capability: types.CodeWikiCapCodeSearch,
				Tool:       "search_code",
				Input:      map[string]string{"query": "{{query}}", "project": "{{handle}}", "literal": "static"},
				Output:     types.CodeWikiMappingOutput{ResultsPath: "results", PathField: "path", StartLineField: "line"},
			}},
		},
		{
			name: "text-only output is valid",
			mapping: []types.CodeWikiToolMapping{{
				Capability: types.CodeWikiCapCodeSearch,
				Tool:       "grep",
				Output:     types.CodeWikiMappingOutput{TextField: "text"},
			}},
		},
		{
			name: "unknown capability rejected",
			mapping: []types.CodeWikiToolMapping{{
				Capability: "teleport",
				Tool:       "t",
				Output:     types.CodeWikiMappingOutput{TextField: "text"},
			}},
			wantErr: true,
		},
		{
			name: "duplicate capability rejected",
			mapping: []types.CodeWikiToolMapping{
				{Capability: types.CodeWikiCapCodeSearch, Tool: "a", Output: types.CodeWikiMappingOutput{TextField: "text"}},
				{Capability: types.CodeWikiCapCodeSearch, Tool: "b", Output: types.CodeWikiMappingOutput{TextField: "text"}},
			},
			wantErr: true,
		},
		{
			name: "empty tool rejected",
			mapping: []types.CodeWikiToolMapping{{
				Capability: types.CodeWikiCapCodeSearch,
				Output:     types.CodeWikiMappingOutput{TextField: "text"},
			}},
			wantErr: true,
		},
		{
			name: "unknown context variable rejected",
			mapping: []types.CodeWikiToolMapping{{
				Capability: types.CodeWikiCapCodeSearch,
				Tool:       "search",
				Input:      map[string]string{"q": "{{env HOME}}"},
				Output:     types.CodeWikiMappingOutput{TextField: "text"},
			}},
			wantErr: true,
		},
		{
			name: "embedded variable (not whole-value) rejected",
			mapping: []types.CodeWikiToolMapping{{
				Capability: types.CodeWikiCapCodeSearch,
				Tool:       "search",
				Input:      map[string]string{"q": "prefix {{query}} suffix"},
				Output:     types.CodeWikiMappingOutput{TextField: "text"},
			}},
			wantErr: true,
		},
		{
			name: "no output selection rejected",
			mapping: []types.CodeWikiToolMapping{{
				Capability: types.CodeWikiCapCodeSearch,
				Tool:       "search",
			}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolMappings(tt.mapping)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRenderInputResolvesContextVariables(t *testing.T) {
	mc := mappingContext{
		Repo: "repo-uuid", Commit: "abc123", Query: "router",
		Handle: "cw-kb-snap", Limit: 10, StartLine: 3, EndLine: 9,
	}
	got := renderInput(map[string]string{
		"q":       "{{query}}",
		"repo":    "{{repo}}",
		"commit":  "{{commit}}",
		"project": "{{handle}}",
		"limit":   "{{limit}}",
		"literal": "static-value",
	}, mc)
	require.Equal(t, "router", got["q"])
	require.Equal(t, "repo-uuid", got["repo"])
	require.Equal(t, "abc123", got["commit"])
	require.Equal(t, "cw-kb-snap", got["project"])
	require.Equal(t, 10, got["limit"])
	require.Equal(t, "static-value", got["literal"])
	// Unknown variables resolve defensively to "" (validation already
	// rejected them; nothing executes here).
	require.Equal(t, "", renderValue("{{nope}}", mc))
}

func TestLookupPath(t *testing.T) {
	doc := map[string]interface{}{
		"hits": map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{"path": "a.go", "line": float64(3)},
				map[string]interface{}{"path": "b.go"},
			},
		},
		"text": "blob",
	}
	v, ok := lookupPath(doc, "hits.items.0.path")
	require.True(t, ok)
	require.Equal(t, "a.go", coerceString(v))

	v, ok = lookupPath(doc, "hits.items.1.line")
	require.False(t, ok, "missing field")

	v, ok = lookupPath(doc, "text")
	require.True(t, ok)
	require.Equal(t, "blob", coerceString(v))

	_, ok = lookupPath(doc, "hits.items.99")
	require.False(t, ok, "out-of-range index")

	require.Equal(t, 3, coerceInt(float64(3)))
	require.Equal(t, 7, coerceInt("7"))
	require.Equal(t, 0, coerceInt(nil))
}

func TestSnapshotScopeProjectIDIsDeterministicAndHexOnly(t *testing.T) {
	a := SnapshotScope{KnowledgeBaseID: "6f1a2b3c-1111-2222-3333-444455556666", SnapshotID: "aaaaBBBBcccc-dddd-eeee-ffff-000011112222"}
	b := SnapshotScope{KnowledgeBaseID: "6f1a2b3c-1111-2222-3333-444455556666", SnapshotID: "aaaaBBBBcccc-dddd-eeee-ffff-000011112222"}
	require.Equal(t, a.ProjectID(), b.ProjectID(), "same scope derives the same project")

	c := SnapshotScope{KnowledgeBaseID: "6f1a2b3c-1111-2222-3333-444455556667", SnapshotID: a.SnapshotID}
	require.NotEqual(t, a.ProjectID(), c.ProjectID(), "different KB derives a different project")

	project := a.ProjectID()
	require.True(t, len(project) > len("cw-"), "project name has content")
	for i := len("cw-"); i < len(project); i++ {
		c := project[i]
		require.True(t, (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || c == '-',
			"project name is hex-only after the cw- prefix, got %q", project)
	}
}

func TestSafeSnapshotPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	inside, err := safeSnapshotPath(root, "src/main.go")
	require.NoError(t, err)
	require.Contains(t, inside, root)

	_, err = safeSnapshotPath(root, "../escape.go")
	require.Error(t, err)

	_, err = safeSnapshotPath(root, "a/../../escape.go")
	require.Error(t, err)
}
