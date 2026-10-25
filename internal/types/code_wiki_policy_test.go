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

func TestCodeWikiTopicConcurrencyDefaultsAndRoundTrips(t *testing.T) {
	var absent *CodeWikiConfig
	require.Equal(t, 3, absent.TopicConcurrencyOrDefault())
	for _, value := range []int{0, -1, 1, 3, 8, 99} {
		cfg := CodeWikiConfig{TopicConcurrency: value}
		cfg.Normalize()
		require.GreaterOrEqual(t, cfg.TopicConcurrency, 1)
		require.LessOrEqual(t, cfg.TopicConcurrency, 8)
		blob, err := cfg.Value()
		require.NoError(t, err)
		var decoded CodeWikiConfig
		require.NoError(t, decoded.Scan(blob))
		require.Equal(t, cfg.TopicConcurrency, decoded.TopicConcurrency)
	}
}
