package service

import (
	"encoding/json"
	"reflect"

	"github.com/Tencent/WeKnora/internal/types"
)

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
	previous.PublicationGuard, cfg.PublicationGuard = nil, nil
	previous.SourceChanges, cfg.SourceChanges = nil, nil
	previous.SnapshotReferences, cfg.SnapshotReferences = nil, nil
	// Execution checkpoints do not change generation inputs.
	previous.FrontendPlanning, cfg.FrontendPlanning = nil, nil
	previous.FrontendPlanReady, cfg.FrontendPlanReady = false, false
	previous.TopicConcurrency, cfg.TopicConcurrency = 0, 0
	if !reflect.DeepEqual(previous, cfg) {
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
