package service

import (
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCodeWikiPathAllowedDefaultExcludes(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"src/main.go", true},
		{"web/src/router.js", true},
		{"package.json", true},
		{"go.mod", true},
		{"README.md", true},
		{"Dockerfile", true},
		// build artifacts / deps / caches
		{"node_modules/pkg/index.js", false},
		{"web/node_modules/react.js", false},
		{".git/config", false},
		{"dist/bundle.js", false},
		{"build/output.js", false},
		{"web/dist/app.js", false},
		{"__pycache__/x.pyc", false},
		{".venv/lib.py", false},
		{"coverage/lcov.info", false},
		// minified / sourcemaps / binaries
		{"static/app.min.js", false},
		{"static/app.js.map", false},
		{"bin/tool.exe", false},
		{"lib/native.so", false},
		{"assets/logo.png", false},
		{"docs/arch.pdf", false},
		{"release/app.zip", false},
		// sensitive files
		{".env", false},
		{".env.production", false},
		{"config/secrets.yaml", false},
		{"deploy/server.key", false},
		{"id_rsa", false},
	}
	for _, tt := range cases {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, codeWikiPathAllowed(tt.path, nil, nil))
		})
	}
}

func TestCodeWikiPathAllowedIncludeRoots(t *testing.T) {
	roots := []string{"src", "web/src"}
	require.True(t, codeWikiPathAllowed("src/main.go", roots, nil))
	require.True(t, codeWikiPathAllowed("web/src/app.js", roots, nil))
	require.False(t, codeWikiPathAllowed("docs/guide.md", roots, nil))
	require.False(t, codeWikiPathAllowed("src2/other.go", roots, nil), "prefix must not half-match")
}

func TestCodeWikiPathAllowedUserExcludes(t *testing.T) {
	// basename pattern
	require.False(t, codeWikiPathAllowed("src/generated.go", nil, []string{"generated.go"}))
	// glob basename
	require.False(t, codeWikiPathAllowed("src/mock_test.go", nil, []string{"mock_*"}))
	// path-scoped pattern only matches with slash
	require.False(t, codeWikiPathAllowed("internal/mock/data.go", nil, []string{"internal/mock/*"}))
	require.True(t, codeWikiPathAllowed("other/mock/data.go", nil, []string{"internal/mock/*"}))
	// vendor stays included unless explicitly excluded
	require.True(t, codeWikiPathAllowed("vendor/lib/lib.go", nil, nil))
	require.False(t, codeWikiPathAllowed("vendor/lib/lib.go", nil, []string{"vendor/"}))
}

func TestCodeWikiFilterHashDistinguishesFilters(t *testing.T) {
	base := codeWikiFilterHash(nil, nil)
	withInclude := codeWikiFilterHash([]string{"src"}, nil)
	withExclude := codeWikiFilterHash(nil, []string{"dist/"})
	differentInclude := codeWikiFilterHash([]string{"web"}, nil)

	require.Equal(t, base, codeWikiFilterHash(nil, nil))
	require.NotEqual(t, base, withInclude)
	require.NotEqual(t, base, withExclude)
	require.NotEqual(t, withInclude, differentInclude)
}

func TestCodeWikiPageSlug(t *testing.T) {
	require.Equal(t, "project-overview", codeWikiPageSlug("Project Overview", ""))
	require.Equal(t, "repo-web-portal", codeWikiPageSlug("repo web_portal", ""))
	require.Equal(t, "api", codeWikiPageSlug("API", ""))
	// Non-ASCII-only input degrades to empty — the planner drops such
	// pages rather than emitting a broken slug; models are prompted for
	// ASCII slugs.
	require.Equal(t, "", codeWikiPageSlug("---", ""))
	require.Equal(t, "", codeWikiPageSlug("系统架构", ""))
	require.Equal(t, "", codeWikiPageSlug("", "系统架构"), "title fallback also strips to empty")
	// long slugs are capped
	long := codeWikiPageSlug(makeSlug(200), "")
	require.LessOrEqual(t, len(long), 120)
}

func makeSlug(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestCodeWikiCitationValidationHelpers(t *testing.T) {
	evidence := make([]types.CodeWikiEvidenceSpec, 3)
	for i := range evidence {
		evidence[i] = types.CodeWikiEvidenceSpec{ID: fmt.Sprintf("E%d", i+1)}
	}
	require.True(t, contentCitesEvidence("调用链如 [E1] 所示", evidence))
	require.True(t, contentCitesEvidence("[E3]", evidence))
	require.False(t, contentCitesEvidence("[E9]", evidence), "unknown id is not a valid citation")
	require.False(t, contentCitesEvidence("no citations here", evidence))
}
