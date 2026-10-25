package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

func frontendPlanningBatch(state *codeWikiRunState, repositoryID, repositoryName string, modules []frontendIndexModule, candidates []frontendIndexCandidate) *types.CodeWikiPlanningBatch {
	batch := &types.CodeWikiPlanningBatch{RepositoryID: repositoryID, RepositoryName: repositoryName, SnapshotID: state.scopes[repositoryID].SnapshotID, FileCount: len(candidates)}
	var identities []string
	for _, module := range modules {
		public := types.CodeWikiPlanningBatchModule{ID: module.ID, Directory: module.Directory}
		for _, file := range module.Files {
			public.Files = append(public.Files, path.Join(module.Directory, file.Path))
			identities = append(identities, file.candidateID)
		}
		batch.Modules = append(batch.Modules, public)
	}
	hash := sha256.Sum256([]byte(repositoryID + "\x00" + batch.SnapshotID + "\x00" + strings.Join(identities, "\x00")))
	batch.ID = hex.EncodeToString(hash[:12])
	return batch
}

// These limits bound navigation payloads, never document counts. Modules are
// structural buckets the model may split; directory names do not assign types.
const frontendModuleFiles = 32
const frontendModuleBytes = 8000
const frontendModuleBatchFiles = 256
const frontendModuleBatchBytes = 48000

type frontendModuleFile struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Symbols     []string `json:"symbols,omitempty"`
	SymbolCount int      `json:"symbol_count"`
	Boundary    string   `json:"boundary,omitempty"`
	candidateID string
}

type frontendIndexModule struct {
	ID          string               `json:"id"`
	Directory   string               `json:"directory"`
	Files       []frontendModuleFile `json:"files"`
	Imports     []string             `json:"imports_sample,omitempty"`
	ImportCount int                  `json:"import_count"`
	Entry       bool                 `json:"indexed_entry,omitempty"`
}

func frontendIndexAccounted(decision string) bool {
	return strings.HasPrefix(decision, "candidate:") || strings.HasPrefix(decision, "unsupported: 索引发现待查：")
}

func frontendIndexAccountedCount(candidates []frontendIndexCandidate, progress *frontendRepoPlan) int {
	count := 0
	for _, candidate := range candidates {
		if frontendIndexAccounted(progress.Decisions[candidate.Path]) {
			count++
		}
	}
	return count
}

func frontendIndexModules(candidates []frontendIndexCandidate, progress *frontendRepoPlan) ([]frontendIndexModule, error) {
	var modules []frontendIndexModule
	byDirectory := map[string]int{}
	owner := map[string]int{}
	for i, candidate := range candidates {
		if frontendIndexAccounted(progress.Decisions[candidate.Path]) {
			continue
		}
		directory := path.Dir(candidate.Path)
		file := frontendModuleFile{ID: "f" + strconv.Itoa(i), Path: path.Base(candidate.Path), SymbolCount: candidate.SymbolCount, Boundary: candidate.Boundary, candidateID: candidate.ID}
		seen := map[string]bool{}
		for _, symbol := range candidate.Symbols {
			name := symbol.Kind + ":" + truncateCodeWikiString(symbol.Name, 80)
			if !seen[name] && len(file.Symbols) < 3 {
				file.Symbols = append(file.Symbols, name)
				seen[name] = true
			}
		}
		encoded, _ := json.Marshal(file)
		if len(encoded)+len(directory) > frontendModuleBytes {
			return nil, fmt.Errorf("单个文件导航过大，不能静默省略路径：%s", candidate.Path)
		}
		idx, exists := byDirectory[directory]
		if !exists || candidate.Entry || modules[idx].Entry || len(modules[idx].Files) >= frontendModuleFiles {
			idx = len(modules)
			modules = append(modules, frontendIndexModule{Directory: directory, Entry: candidate.Entry})
			byDirectory[directory] = idx
		}
		trial := modules[idx]
		trial.Files = append(append([]frontendModuleFile(nil), trial.Files...), file)
		encoded, _ = json.Marshal(trial)
		if len(encoded) > frontendModuleBytes {
			idx = len(modules)
			modules = append(modules, frontendIndexModule{Directory: directory, Entry: candidate.Entry})
			byDirectory[directory] = idx
		}
		modules[idx].Files = append(modules[idx].Files, file)
		owner[candidate.Path] = idx
	}
	// Merge an exclusively used child directory into its actual importer, but
	// never absorb indexed entries or shared dependencies. All indexed edges
	// participate, including those beyond the small symbol/edge prompt samples.
	for changed := true; changed; {
		changed = false
		incoming := map[int]map[int]bool{}
		for _, candidate := range candidates {
			source, sourcePending := owner[candidate.Path]
			for _, dependency := range candidate.Dependencies {
				target, targetPending := owner[dependency]
				if !targetPending || (sourcePending && source == target) {
					continue
				}
				if incoming[target] == nil {
					incoming[target] = map[int]bool{}
				}
				if !sourcePending {
					source = -1 // Already classified consumers still make a dependency shared.
				}
				incoming[target][source] = true
			}
		}
		for target := range modules {
			if len(modules[target].Files) == 0 || modules[target].Entry || len(incoming[target]) != 1 {
				continue
			}
			for source := range incoming[target] {
				if source < 0 || len(modules[source].Files) == 0 || !strings.HasPrefix(modules[target].Directory, modules[source].Directory+"/") || len(modules[source].Files)+len(modules[target].Files) > frontendModuleFiles {
					continue
				}
				trial := modules[source]
				trial.Files = append([]frontendModuleFile(nil), trial.Files...)
				for _, file := range modules[target].Files {
					file.Path = strings.TrimPrefix(modules[target].Directory+"/"+file.Path, modules[source].Directory+"/")
					trial.Files = append(trial.Files, file)
				}
				encoded, _ := json.Marshal(trial)
				if len(encoded) > frontendModuleBytes {
					continue
				}
				modules[source] = trial
				modules[target].Files = nil
				for filePath, idx := range owner {
					if idx == target {
						owner[filePath] = source
					}
				}
				changed = true
			}
			if changed {
				break // Recompute ownership before considering the next merge.
			}
		}
	}
	var result []frontendIndexModule
	ids := map[int]string{}
	for i, module := range modules {
		if len(module.Files) > 0 {
			ids[i] = "m" + strconv.Itoa(len(ids))
		}
	}
	for i, module := range modules {
		if len(module.Files) == 0 {
			continue
		}
		module.ID = ids[i]
		targets := map[string]bool{}
		for _, candidate := range candidates {
			if idx, ok := owner[candidate.Path]; !ok || idx != i {
				continue
			}
			for _, dependency := range candidate.Dependencies {
				if target, ok := owner[dependency]; ok && target != i {
					targets[ids[target]] = true
				}
			}
		}
		for target := range targets {
			module.Imports = append(module.Imports, target)
		}
		sort.Strings(module.Imports)
		module.ImportCount = len(module.Imports)
		if len(module.Imports) > 16 {
			module.Imports = module.Imports[:16]
		}
		result = append(result, module)
	}
	return result, nil
}

func frontendModuleBatchEnd(modules []frontendIndexModule, start int) int {
	end, files, size := start, 0, 0
	for end < len(modules) {
		encoded, _ := json.Marshal(modules[end])
		if end > start && (files+len(modules[end].Files) > frontendModuleBatchFiles || size+len(encoded) > frontendModuleBatchBytes) {
			break
		}
		files += len(modules[end].Files)
		size += len(encoded)
		end++
	}
	return end
}

func frontendModuleCandidates(modules []frontendIndexModule, byID map[string]frontendIndexCandidate) []frontendIndexCandidate {
	var result []frontendIndexCandidate
	for _, module := range modules {
		for _, file := range module.Files {
			result = append(result, byID[file.candidateID])
		}
	}
	return result
}

func frontendModulePrompt(state *codeWikiRunState, repository, lang string, progress *frontendRepoPlan, modules []frontendIndexModule, batch []frontendIndexCandidate) string {
	relevantPaths := map[string]bool{}
	for _, candidate := range batch {
		relevantPaths[candidate.Path] = true
		for _, dependency := range candidate.Dependencies {
			relevantPaths[dependency] = true
		}
	}
	var related, other []map[string]string
	for _, page := range progress.Pages {
		identity := map[string]string{"category": page.Category, "domain": page.Domain, "title": page.Title}
		relevant := false
		for _, filePath := range page.SourcePaths {
			relevant = relevant || relevantPaths[filePath]
		}
		if relevant {
			related = append(related, identity)
		} else {
			other = append(other, identity)
		}
	}
	var known []map[string]string
	size := 0
	for _, identity := range append(related, other...) {
		encoded, _ := json.Marshal(identity)
		if size+len(encoded) <= 6000 {
			known = append(known, identity)
			size += len(encoded)
		}
	}
	knownJSON, _ := json.Marshal(map[string]any{"sample": known, "total": len(progress.Pages), "omitted": len(progress.Pages) - len(known)})
	data, _ := json.Marshal(modules)
	return frontendIndexPlanInstructions + `
Candidates are PROGRAM-GROUPED STRUCTURAL MODULES, not business facts or mandatory documents. Each module has directory + relative file paths, short file IDs, up to 3 sampled symbols per file with full symbol_count, and pending-module import IDs (sampled, import_count counts pending target modules). Dependencies on previously classified files are reflected in related existing topic identities. An indexed_entry is a route/handler index hint, not proof of a working page. Imports outside this batch are navigation only.
Use module IDs in candidate_ids to classify an entire coherent module. To split a mixed module, use its file IDs across groups/unassigned instead. A file may support multiple groups; EVERY file must be assigned or explicitly unassigned. Prefer concise module-level groups; do not echo file lists when one module ID suffices. You may merge modules. Do not create one topic per module by default; category and scope require the supplied structural signals, never directory names alone. Unassigned may also use a module ID with a concrete boundary reason. Return the same groups/unassigned JSON schema. No reasoning text.
` + fmt.Sprintf("\nLanguage: %s\nRepository: %s\nContent preferences: %s\nExtraction focus: %s\nExisting topic identities:\n%s\nModule batch:\n%s", lang, repository, state.cfg.ContentInstructions, state.cfg.ExtractionInstructions, knownJSON, data)
}

func frontendModulePromptFits(cfg codeWikiRunConfig, prompt string) bool {
	if len(prompt) > codeWikiMaxContextChars*2 {
		return false
	}
	policy := "You are a precise technical documentation generator. Follow the evidence protocol strictly."
	if cfg.SourcePolicyVersion > 0 {
		policy += "\n" + types.CodeWikiSourceFactPolicy
	}
	_, err := codeWikiContextBudget(cfg, []chat.Message{{Role: "system", Content: policy}, {Role: "user", Content: prompt}})
	return err == nil
}

// Expand only request-local short IDs. The existing atomic file coverage and
// source validation remain authoritative, including split modules and gaps.
func expandFrontendModuleReply(reply frontendIndexReply, modules []frontendIndexModule) (frontendIndexReply, error) {
	ids := map[string][]string{}
	for _, module := range modules {
		for _, file := range module.Files {
			ids[module.ID] = append(ids[module.ID], file.candidateID)
			ids[file.ID] = []string{file.candidateID}
		}
	}
	for i := range reply.Groups {
		var expanded []string
		for _, id := range reply.Groups[i].CandidateIDs {
			files, ok := ids[id]
			if !ok {
				return frontendIndexReply{}, fmt.Errorf("未知或不在本批次的模块/文件：%s", id)
			}
			expanded = append(expanded, files...)
		}
		reply.Groups[i].CandidateIDs = expanded
	}
	originalGaps := reply.Unassigned
	reply.Unassigned = nil
	for _, gap := range originalGaps {
		files, ok := ids[gap.ID]
		if !ok {
			return frontendIndexReply{}, fmt.Errorf("未知或不在本批次的未分配模块/文件：%s", gap.ID)
		}
		for _, id := range files {
			expanded := gap
			expanded.ID = id
			reply.Unassigned = append(reply.Unassigned, expanded)
		}
	}
	return reply, nil
}
