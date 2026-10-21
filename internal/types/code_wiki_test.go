package types

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsValidKnowledgeBaseType(t *testing.T) {
	require.True(t, IsValidKnowledgeBaseType(KnowledgeBaseTypeDocument))
	require.True(t, IsValidKnowledgeBaseType(KnowledgeBaseTypeFAQ))
	require.True(t, IsValidKnowledgeBaseType(KnowledgeBaseTypeWiki))
	require.True(t, IsValidKnowledgeBaseType(KnowledgeBaseTypeCodeWiki))
	require.False(t, IsValidKnowledgeBaseType("code-wiki"))
	require.False(t, IsValidKnowledgeBaseType(""))
}

func TestCodeWikiEnsureDefaultsForcesWikiAndSkipsEmbedding(t *testing.T) {
	kb := &KnowledgeBase{Type: KnowledgeBaseTypeCodeWiki}
	kb.EnsureDefaults()

	// Wiki reading is the product: always on.
	require.True(t, kb.IsWikiEnabled(), "code wiki KBs must always expose wiki reading")
	// Fresh code wiki KBs default to wiki-only: source never enters the
	// document chunk pipeline and generation must not require embeddings.
	require.False(t, kb.IndexingStrategy.VectorEnabled)
	require.False(t, kb.IndexingStrategy.KeywordEnabled)
	require.False(t, kb.NeedsEmbeddingModel())
	// Config is materialized with defaults available.
	require.NotNil(t, kb.CodeWikiConfig)
	require.Equal(t, DefaultCodeWikiMaxPagesPerRun, kb.CodeWikiConfig.PagesCapOrDefault())
}

func TestCodeWikiEnsureDefaultsRespectsExplicitVector(t *testing.T) {
	kb := &KnowledgeBase{
		Type:             KnowledgeBaseTypeCodeWiki,
		IndexingStrategy: IndexingStrategy{VectorEnabled: true, WikiEnabled: true},
	}
	kb.EnsureDefaults()
	require.True(t, kb.IsVectorEnabled(), "an explicitly enabled vector pipeline stays on")
	require.True(t, kb.IsWikiEnabled())
}

func TestEnsureDefaultsDropsCodeWikiConfigOnOtherTypes(t *testing.T) {
	kb := &KnowledgeBase{
		Type:            KnowledgeBaseTypeDocument,
		CodeWikiConfig:  &CodeWikiConfig{Languages: []string{"zh"}},
	}
	kb.EnsureDefaults()
	require.Nil(t, kb.CodeWikiConfig)
}

func TestCodeWikiCapabilitiesIncludeCodeWiki(t *testing.T) {
	kb := &KnowledgeBase{Type: KnowledgeBaseTypeCodeWiki}
	kb.EnsureDefaults()
	caps := kb.Capabilities()
	require.True(t, caps.CodeWiki)
	require.True(t, caps.Wiki)
	require.False(t, caps.Vector)
}

func TestCodeWikiConfigNormalizeClamps(t *testing.T) {
	cfg := &CodeWikiConfig{
		MaxPagesPerRun:      MaxCodeWikiPagesPerRunHardCap * 2,
		MaxModelCallsPerRun: MaxCodeWikiModelCallsHardCap * 2,
		RunTimeoutMinutes:   MaxCodeWikiRunTimeoutMinutesCap * 2,
	}
	cfg.Normalize()
	require.Equal(t, MaxCodeWikiPagesPerRunHardCap, cfg.MaxPagesPerRun)
	require.Equal(t, MaxCodeWikiModelCallsHardCap, cfg.MaxModelCallsPerRun)
	require.Equal(t, MaxCodeWikiRunTimeoutMinutesCap, cfg.RunTimeoutMinutes)
	require.Equal(t, time.Duration(MaxCodeWikiRunTimeoutMinutesCap)*time.Minute, cfg.TimeoutOrDefault())
}

func TestCodeWikiCapabilityReportHelpers(t *testing.T) {
	report := &CodeWikiCapabilityReport{
		Capabilities: []CodeWikiCapabilityStatus{
			{Capability: CodeWikiCapCodeSearch, Available: true, Source: "provider"},
			{Capability: CodeWikiCapCallGraph, Available: false, Detail: "not mapped"},
		},
	}
	require.True(t, report.HasCapability(CodeWikiCapCodeSearch))
	require.False(t, report.HasCapability(CodeWikiCapCallGraph))
	require.False(t, report.HasCapability(CodeWikiCapCrossRepo)) // absent = unavailable
	c := report.Capability(CodeWikiCapCallGraph)
	require.NotNil(t, c)
	require.Equal(t, "not mapped", c.Detail)
}

func TestCodeWikiToolMappingRoundTrip(t *testing.T) {
	profile := &CodeWikiAnalysisProfile{}
	blob, err := json.Marshal([]CodeWikiToolMapping{{
		Capability: CodeWikiCapCodeSearch,
		Tool:       "search",
		Input:      map[string]string{"q": "{{query}}", "project": "{{handle}}"},
		Output:     CodeWikiMappingOutput{ResultsPath: "hits.items", PathField: "file"},
	}})
	require.NoError(t, err)
	profile.ToolMappings = JSON(blob)

	mappings, err := profile.ParsedToolMappings()
	require.NoError(t, err)
	require.Len(t, mappings, 1)
	require.Equal(t, CodeWikiCapCodeSearch, mappings[0].Capability)
	require.Equal(t, "hits.items", mappings[0].Output.ResultsPath)
}

func TestIsTerminalCodeWikiRunStatus(t *testing.T) {
	for _, status := range []string{
		CodeWikiRunStatusPublished, CodeWikiRunStatusSuperseded,
		CodeWikiRunStatusFailed, CodeWikiRunStatusCanceled,
	} {
		require.True(t, IsTerminalCodeWikiRunStatus(status))
	}
	for _, status := range []string{
		CodeWikiRunStatusPending, CodeWikiRunStatusGenerating, CodeWikiRunStatusStaged,
	} {
		require.False(t, IsTerminalCodeWikiRunStatus(status))
	}
}
