package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/codebase"
	"github.com/Tencent/WeKnora/internal/codefacts"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func (s *codeWikiService) ListFactRecords(ctx context.Context, kbID, runID, recordType, after string, limit int) ([]*types.CodeWikiFactRecord, error) {
	kb, err := s.requireCodeWikiKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 500 || len(after) > 200 {
		return nil, apperrors.NewBadRequestError("事实清单分页范围无效")
	}
	switch recordType {
	case "source", "fact", "evidence", "relation", "document_link", "question", "human_explanation", "review":
	default:
		return nil, apperrors.NewBadRequestError("未知的事实记录类型")
	}
	if _, err := s.repo.GetRun(ctx, kbID, runID); err != nil {
		return nil, err
	}
	repo, ok := s.repo.(interfaces.CodeWikiFactRepository)
	if !ok {
		return nil, fmt.Errorf("源码事实仓储不可用")
	}
	return repo.ListFactRecords(ctx, types.CodeWikiFactScope{TenantID: kb.TenantID, KnowledgeBaseID: kbID, RunID: runID}, recordType, after, limit)
}

func factRecordKey(parts ...string) string {
	data, _ := json.Marshal(parts)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func factScope(state *codeWikiRunState) types.CodeWikiFactScope {
	return types.CodeWikiFactScope{TenantID: state.run.TenantID, KnowledgeBaseID: state.run.KnowledgeBaseID, RunID: state.run.ID}
}

func factRecord(scope types.CodeWikiFactScope, kind, key string, value any) (*types.CodeWikiFactRecord, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &types.CodeWikiFactRecord{TenantID: scope.TenantID, KnowledgeBaseID: scope.KnowledgeBaseID,
		RunID: scope.RunID, RecordType: kind, RecordKey: key, SchemaVersion: types.CodeWikiFactSchemaVersion, Payload: payload}, nil
}

func captureCodeWikiInventory(ctx context.Context, repo interfaces.CodeWikiFactRepository, state *codeWikiRunState, scope codebase.SnapshotScope, files []string) error {
	records := make([]*types.CodeWikiFactRecord, 0, len(files))
	for _, path := range files {
		role, reason := codeWikiSourceRole(path)
		record, err := factRecord(factScope(state), "source", factRecordKey(scope.RepositoryID, path, "eligibility"),
			types.CodeWikiSourceDecision{RepositoryID: scope.RepositoryID, SnapshotID: scope.SnapshotID, CommitSHA: scope.CommitSHA, Path: path, Role: role, Reason: reason})
		if err != nil {
			return err
		}
		records = append(records, record)
	}
	return repo.AppendFactRecords(ctx, factScope(state), records)
}

func captureCodeWikiSourceFacts(ctx context.Context, repo interfaces.CodeWikiFactRepository, state *codeWikiRunState, scope codebase.SnapshotScope, path, role, reason string, result *codefacts.Result) error {
	version := factScope(state)
	source, err := factRecord(version, "source", factRecordKey(scope.RepositoryID, path, "analysis"),
		types.CodeWikiSourceDecision{RepositoryID: scope.RepositoryID, SnapshotID: scope.SnapshotID, CommitSHA: scope.CommitSHA,
			Path: path, Role: role, Reason: reason, Parsed: result != nil})
	if err != nil {
		return err
	}
	records := []*types.CodeWikiFactRecord{source}
	if result != nil {
		for index, node := range result.Nodes {
			key := factRecordKey(scope.RepositoryID, path, fmt.Sprint(index), node.Kind)
			attributes, err := json.Marshal(node)
			if err != nil {
				return err
			}
			if len(attributes) > 64<<10 {
				question, err := factRecord(version, "question", key, types.CodeWikiFactQuestion{Question: fmt.Sprintf("%s:%d 的语法节点超过分析大小范围，需补充追踪；不计为已覆盖", path, node.StartLine)})
				if err != nil {
					return err
				}
				records = append(records, question)
				continue
			}
			evidence, err := factRecord(version, "evidence", key, types.CodeWikiFactEvidence{RepositoryID: scope.RepositoryID,
				SnapshotID: scope.SnapshotID, CommitSHA: scope.CommitSHA, Path: path, StartLine: node.StartLine, EndLine: node.EndLine, SourceRole: role})
			if err != nil {
				return err
			}
			fact, err := factRecord(version, "fact", key, types.CodeWikiFact{Kind: "syntax." + node.Kind, Subject: path,
				State: "syntax_candidate", Attributes: attributes, Evidence: types.StringArray{key}})
			if err != nil {
				return err
			}
			records = append(records, evidence, fact)
		}
	}
	for len(records) > 0 {
		count := len(records)
		if count > 500 {
			count = 500
		}
		if err := repo.AppendFactRecords(ctx, version, records[:count]); err != nil {
			return err
		}
		records = records[count:]
	}
	return nil
}
