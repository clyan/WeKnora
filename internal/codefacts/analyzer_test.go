package codefacts

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImplementationViewRemovesCommentsWithoutMovingLines(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	source := strings.Repeat("// outdated administrator permission\n", 501) + `/** obsolete business meaning */
export const save = (value: string) => {
  const literal = "/* preserve string */";
  const regex = /\/\//;
  const params = { /* not an actual field */ id: value, ...(value ? { enabled: true } : {}) };
  return request('/orders', { method: 'POST', data: params }); // stale endpoint description
};
export const View = () => <button disabled={false} onClick={() => save("x")}>Save</button>;
`
	result, err := Analyze(context.Background(), "unusual/screen.tsx", source)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, strings.Count(source, "\n"), strings.Count(result.Source, "\n"))
	require.NotContains(t, result.Source, "outdated")
	require.NotContains(t, result.Source, "obsolete")
	require.NotContains(t, result.Source, "stale endpoint")
	require.Contains(t, result.Source, "/* preserve string */")
	require.Contains(t, result.Source, `/\/\//`)
	var request, button bool
	for _, node := range result.Nodes {
		if node.Kind == "call" && node.Callee == "request" {
			request = true
			require.Greater(t, node.StartLine, 500)
			require.NotContains(t, strings.Join(node.Arguments, " "), "not an actual")
		}
		if node.Kind == "component" && node.Name == "button" {
			button = true
		}
	}
	require.True(t, request)
	require.True(t, button)
}

func TestMockImportAndSimulationAreNotImplementationEvidence(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	for _, source := range []string{`import { http } from 'msw'; export const handlers = [];`, `export const createMockRecords = () => [{ id: 1 }];`} {
		result, err := Analyze(context.Background(), "src/normal.ts", source)
		require.NoError(t, err)
		require.NotEqual(t, "implementation", result.Kind)
		require.NotEmpty(t, result.Reason)
	}
}

func TestPackageDescriptionDoesNotBecomeBusinessEvidence(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	result, err := Analyze(context.Background(), "package.json", `{"name":"app","description":"obsolete business claims","dependencies":{"lib":"1.3.1"}}`)
	require.NoError(t, err)
	require.NotContains(t, result.Source, "obsolete business")
	require.Contains(t, result.Source, "1.3.1")
}

func TestAnalyzerDoesNotExecuteProjectCodeOrNodeHooks(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	// If Node inherited this hook it would fail before the parser starts.
	t.Setenv("NODE_OPTIONS", "--require /nonexistent/project-setup.cjs")
	t.Setenv("NODE_PATH", "/nonexistent/project/node_modules")
	require.NoError(t, CheckRuntime(context.Background()))
	result, err := Analyze(context.Background(), "page.ts", "throw new Error('must not execute'); export const value = import('./missing-plugin');")
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Contains(t, result.Source, "must not execute")
}

func TestAnalyzerRetainsReactUsageConditionsAndObjectConstruction(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	result, err := Analyze(context.Background(), "arbitrary/workbench.tsx", `
import { Button as Action } from 'ui';
export const Screen = ({ status, id }) => status === 1 && <Action disabled={loading} onClick={() => save({id, ...form, id: selectedId})}>Save</Action>;
`)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	var component, call, object, exported bool
	for _, node := range result.Nodes {
		switch {
		case node.Kind == "export" && strings.Contains(node.Bindings, "Screen"):
			exported = true
		case node.Kind == "component" && node.Name == "Action":
			component = true
			require.Equal(t, "Screen", node.Owner)
			require.Contains(t, node.Enclosing, Branch{Expression: "status === 1", Branch: "truthy"})
		case node.Kind == "call" && node.Callee == "save":
			call = true
			require.Contains(t, node.Arguments, "{id, ...form, id: selectedId}")
		case node.Kind == "object" && len(node.Properties) == 3:
			object = true
			require.Equal(t, []Property{{Name: "id", Value: "id"}, {Value: "form", Spread: true}, {Name: "id", Value: "selectedId"}}, node.Properties)
		}
	}
	require.True(t, component)
	require.True(t, call)
	require.True(t, object)
	require.True(t, exported, "direct exported declarations must retain export identity")
}

func TestAnalyzerMissingNodeReportsRuntimeFailure(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	t.Setenv("PATH", t.TempDir())
	require.ErrorContains(t, CheckRuntime(context.Background()), "源码分析器执行失败")
}

func TestAnalyzerSupportsJavaScriptTypeScriptAndTheirJSXForms(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	for _, path := range []string{"custom/source.js", "custom/source.ts", "custom/source.jsx", "custom/source.tsx"} {
		source := "// old business claim\nexport const send = data => fetch('/orders', {method: 'POST', body: data});"
		if strings.HasSuffix(path, "jsx") || strings.HasSuffix(path, "tsx") {
			source += "\nexport const Screen = () => <button onClick={() => send(value)}>Save</button>;"
		}
		result, err := Analyze(context.Background(), path, source)
		require.NoError(t, err, path)
		require.Empty(t, result.Diagnostics, path)
		require.NotContains(t, result.Source, "old business claim")
		require.NotEmpty(t, Discover(result.Nodes).Candidates, path)
	}
}

func TestAnalyzerPreservesCommentSymbolsInJSXAndTemplates(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	source := "export const View = () => <div>// visible text\n<span>/* visible text */</span>{/* obsolete explanation */}</div>;\nexport const template = `/* visible template */`;\nconst label = /* obsolete label explanation */ 'label';\nconst empty = {/* obsolete object explanation */};\nfunction emptyFunction() {/* obsolete block explanation */}"
	result, err := Analyze(context.Background(), "view.tsx", source)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Contains(t, result.Source, "// visible text")
	require.Contains(t, result.Source, "/* visible text */")
	require.Contains(t, result.Source, "/* visible template */")
	require.NotContains(t, result.Source, "obsolete")
	require.Contains(t, result.Source, "'label'")
	require.Contains(t, result.Source, "</div>")
	require.Equal(t, strings.Count(source, "\n"), strings.Count(result.Source, "\n"))
}
