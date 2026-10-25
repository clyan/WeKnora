package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeWikiModesHaveHistoricalProjectDefault(t *testing.T) {
	var absent *CodeWikiConfig
	require.Equal(t, CodeWikiModeProject, absent.EffectiveMode())
	for _, mode := range []string{"", CodeWikiModeProject, CodeWikiModeDomain} {
		cfg := CodeWikiConfig{Mode: mode}
		cfg.Normalize()
		if mode == CodeWikiModeDomain {
			require.Empty(t, cfg.TemplateID, "domain mode must not inherit the project template")
		}
		require.True(t, IsValidCodeWikiMode(cfg.Mode))
		raw, err := cfg.Value()
		require.NoError(t, err)
		var decoded CodeWikiConfig
		require.NoError(t, decoded.Scan(raw))
		require.Equal(t, cfg.Mode, decoded.Mode)
		blob, err := json.Marshal(cfg)
		require.NoError(t, err)
		require.Contains(t, string(blob), `"mode"`)
	}
	require.False(t, IsValidCodeWikiMode("frontend"))
}
