package codebase

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Declarative tool-mapping evaluation for the custom MCP provider.
//
// Security model (implementation plan §4.3): input bindings accept only
// static literal strings and whole-value references to controlled context
// variables ({{repo}}, {{commit}}, {{path}}, {{query}}, {{symbol}},
// {{start_line}}, {{end_line}}, {{limit}}, {{offset}}, {{handle}});
// output bindings are dotted field paths into the tool's JSON response.
// No JS, shell, templates or expressions are ever evaluated.

// contextVariableKeys lists every variable a mapping may reference.
var contextVariableKeys = map[string]struct{}{
	"repo": {}, "commit": {}, "path": {}, "query": {}, "symbol": {},
	"start_line": {}, "end_line": {}, "limit": {}, "offset": {}, "handle": {},
}

// ValidateToolMappings checks every mapping entry for unknown capabilities,
// duplicate capabilities, illegal input values and empty tool names.
func ValidateToolMappings(mappings []types.CodeWikiToolMapping) error {
	if len(mappings) > types.MaxCodeWikiToolMappings {
		return fmt.Errorf("codebase: too many tool mappings (max %d)", types.MaxCodeWikiToolMappings)
	}
	seen := make(map[string]struct{}, len(mappings))
	for i := range mappings {
		m := &mappings[i]
		if _, known := capabilityKeySet[m.Capability]; !known {
			return fmt.Errorf("codebase: mapping[%d]: unknown capability %q", i, m.Capability)
		}
		if strings.TrimSpace(m.Tool) == "" {
			return fmt.Errorf("codebase: mapping[%d] (%s): tool name is required", i, m.Capability)
		}
		if _, dup := seen[m.Capability]; dup {
			return fmt.Errorf("codebase: mapping[%d]: duplicate capability %q", i, m.Capability)
		}
		seen[m.Capability] = struct{}{}
		for key, value := range m.Input {
			if err := validateInputValue(key, value); err != nil {
				return fmt.Errorf("codebase: mapping[%d] (%s): %w", i, m.Capability, err)
			}
		}
		if m.Output.ResultsPath == "" && m.Output.TextField == "" {
			return fmt.Errorf("codebase: mapping[%d] (%s): output requires results_path or text", i, m.Capability)
		}
	}
	return nil
}

func validateInputValue(key, value string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("empty input key")
	}
	value = strings.TrimSpace(value)
	// Any occurrence of "{{" makes the value a variable reference; anything
	// that is not exactly one whole-value {{name}} is rejected. This also
	// catches embedded references like "prefix {{query}} suffix" — the
	// safest reading of the plan's "静态值、受控上下文变量" restriction.
	if !strings.Contains(value, "{{") {
		return nil // static literal
	}
	if !strings.HasPrefix(value, "{{") || !strings.HasSuffix(value, "}}") || strings.Count(value, "{{") != 1 {
		return fmt.Errorf("input %q: variables must be a whole-value {{name}} reference", key)
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "{{"), "}}"))
	if _, ok := contextVariableKeys[name]; !ok {
		return fmt.Errorf("input %q: unknown context variable {{%s}}", key, name)
	}
	return nil
}

// capabilityKeySet enumerates the capabilities a tool mapping may target.
var capabilityKeySet = map[string]struct{}{
	types.CodeWikiCapSnapshotScope:   {},
	types.CodeWikiCapCodeSearch:      {},
	types.CodeWikiCapSourceRead:      {},
	types.CodeWikiCapSnapshotPrepare: {},
	types.CodeWikiCapDirectory:       {},
	types.CodeWikiCapSymbols:         {},
	types.CodeWikiCapCallGraph:       {},
	types.CodeWikiCapCrossRepo:       {},
	types.CodeWikiCapCoverage:        {},
}

// mappingContext carries the controlled variables available to one call.
type mappingContext struct {
	Repo      string
	Commit    string
	Path      string
	Query     string
	Symbol    string
	StartLine int
	EndLine   int
	Limit     int
	Offset    int
	Handle    string
}

// renderInput builds the MCP tool arguments: literals pass through,
// {{var}} references resolve from the context. Unknown references were
// already rejected at validation time; here they resolve to empty strings
// defensively rather than erroring mid-call.
func renderInput(input map[string]string, mc mappingContext) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(input))
	for key, value := range input {
		out[key] = renderValue(value, mc)
	}
	return out
}

func renderValue(value string, mc mappingContext) interface{} {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return value
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "{{"), "}}"))
	switch name {
	case "repo":
		return mc.Repo
	case "commit":
		return mc.Commit
	case "path":
		return mc.Path
	case "query":
		return mc.Query
	case "symbol":
		return mc.Symbol
	case "start_line":
		return mc.StartLine
	case "end_line":
		return mc.EndLine
	case "limit":
		return mc.Limit
	case "offset":
		return mc.Offset
	case "handle":
		return mc.Handle
	default:
		return ""
	}
}

// lookupPath navigates a decoded JSON structure by dotted path. Array
// indices are addressed numerically ("items.0.path"). A missing path
// yields (nil, false).
func lookupPath(node interface{}, path string) (interface{}, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, false
	}
	current := node
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		switch typed := current.(type) {
		case map[string]interface{}:
			next, ok := typed[segment]
			if !ok {
				return nil, false
			}
			current = next
		case []interface{}:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// coerceInt extracts an int from a decoded JSON scalar.
func coerceInt(node interface{}) int {
	switch v := node.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// coerceString extracts a string from a decoded JSON scalar.
func coerceString(node interface{}) string {
	switch v := node.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}
