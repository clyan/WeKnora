package service

import (
	"encoding/json"
	"reflect"

	"github.com/Tencent/WeKnora/internal/types"
)

// Execution policy and checkpoints must not turn the second increment into a
// full rebuild. Source/configuration changes are compared separately.
func codeWikiGenerationSettings(cfg codeWikiRunConfig) codeWikiRunConfig {
	cfg.PublicationGuard, cfg.SourceChanges, cfg.SnapshotReferences = nil, nil, nil
	cfg.FrontendPlanning, cfg.FrontendPlanReady = nil, false
	cfg.TopicConcurrency, cfg.MaxModelCalls, cfg.TimeoutMinutes = 0, 0, 0
	cfg.FullSourceReanalysis = false
	return cfg
}

func compatibleCodeWikiIncrementalInputs(published *types.CodeWikiRun, modelID string, profileVersion int, before, after codeWikiRunConfig) bool {
	return published != nil && published.Status == types.CodeWikiRunStatusPublished &&
		published.ModelID == modelID && published.ProfileVersion == profileVersion &&
		before.TopicVersion == 1 && after.TopicVersion == 1 &&
		before.DirectoryVersion == types.CodeWikiDirectoryVersionFrontend &&
		reflect.DeepEqual(codeWikiGenerationSettings(before), codeWikiGenerationSettings(after))
}

// sameCodeWikiGenerationInputs is deliberately strict. Unknown, excluded,
// duplicate or incomplete inputs cannot justify a no-change conclusion.
// Branch names are tracking settings; snapshot identity pins the actual code
// and filters, so a different branch resolving to the same input is a no-op.
func sameCodeWikiGenerationInputs(published *types.CodeWikiRun, modelID string, profileVersion int,
	cfg codeWikiRunConfig, baseline, target []*types.CodeWikiRunRepository,
) bool {
	if published == nil || published.Status != types.CodeWikiRunStatusPublished ||
		published.ModelID != modelID || published.ProfileVersion != profileVersion ||
		len(baseline) == 0 || len(baseline) != len(target) {
		return false
	}
	var previous codeWikiRunConfig
	if json.Unmarshal(published.ConfigSnapshot, &previous) != nil {
		return false
	}
	// Per-run audit inputs do not change generation settings.
	if len(previous.SnapshotReferences) != len(cfg.SnapshotReferences) {
		return false
	}
	for id, ref := range cfg.SnapshotReferences {
		if previous.SnapshotReferences[id].TreeHash != ref.TreeHash {
			return false
		}
	}
	if !reflect.DeepEqual(codeWikiGenerationSettings(previous), codeWikiGenerationSettings(cfg)) {
		return false
	}
	type input struct {
		SnapshotID, ArtifactID, CommitSHA, Role, DisplayName string
	}
	collect := func(entries []*types.CodeWikiRunRepository) (map[string]input, bool) {
		out := make(map[string]input, len(entries))
		for _, entry := range entries {
			if entry == nil || entry.Excluded || entry.RepositoryID == "" || entry.SnapshotID == "" || entry.ArtifactID == "" || entry.CommitSHA == "" {
				return nil, false
			}
			if _, duplicate := out[entry.RepositoryID]; duplicate {
				return nil, false
			}
			out[entry.RepositoryID] = input{entry.SnapshotID, entry.ArtifactID, entry.CommitSHA, entry.Role, entry.DisplayName}
		}
		return out, true
	}
	before, beforeOK := collect(baseline)
	after, afterOK := collect(target)
	return beforeOK && afterOK && reflect.DeepEqual(before, after)
}
