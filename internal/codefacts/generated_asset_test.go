package codefacts

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedGlyphRequiresSyntaxAndKeepsMixedBusinessModules(t *testing.T) {
	glyph := `window._iconfont_svg_string_1 = '<svg><symbol>` + strings.Repeat("glyph", 1100) + `</symbol></svg>';`
	for _, test := range []struct {
		name, source, kind string
	}{
		{"generated", glyph, "asset"},
		{"comment-marker", "/*" + glyph + "*/\nrun();", "implementation"},
		{"mixed-module", glyph + "\nexport const send = () => fetch('/orders');", "implementation"},
		{"mixed-global", glyph + "\nwindow.send = () => fetch('/orders');", "implementation"},
		{"mixed-local", glyph + "\nconst send = () => customSend();", "implementation"},
		{"ordinary-public-script", "window.send = () => fetch('/orders');", "implementation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Analyze(context.Background(), "public/lib/custom.js", test.source)
			require.NoError(t, err)
			require.Empty(t, result.Diagnostics)
			require.Equal(t, test.kind, result.Kind)
		})
	}
}
