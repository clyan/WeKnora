package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// Every fact operation requires explicit tenant, KB and run scope, including
// workers. Publication status is resolved from the authoritative run row.
type CodeWikiFactRepository interface {
	AppendFactRecords(context.Context, types.CodeWikiFactScope, []*types.CodeWikiFactRecord) error
	ListFactRecords(context.Context, types.CodeWikiFactScope, string, string, int) ([]*types.CodeWikiFactRecord, error)
}
