package interfaces

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"time"
)

// Optional capability lets historical repositories remain readable while new
// generation fails closed if durable topic execution is unavailable.
type CodeWikiTopicRepository interface {
	ApplyTopicPlan(context.Context, *types.CodeWikiTopic, int, int, *types.CodeWikiTopicPlanUpdate) error
	EnsureTopics(context.Context, *types.CodeWikiRun, []*types.CodeWikiTopic) error
	ListTopics(context.Context, uint64, string, string) ([]*types.CodeWikiTopic, error)
	ListTopicStates(context.Context, uint64, string, string) ([]*types.CodeWikiTopic, error)
	GetTopic(context.Context, uint64, string, string, string) (*types.CodeWikiTopic, error)
	BeginTopicPass(context.Context, *types.CodeWikiRun, int, time.Duration, int) error
	ClaimTopic(context.Context, uint64, string, string, string, int, int, string) (*types.CodeWikiTopic, error)
	SaveTopic(context.Context, *types.CodeWikiTopic, int, int) error
	HeartbeatTopic(context.Context, *types.CodeWikiTopic, int) error
	StageTopic(context.Context, *types.CodeWikiTopic, int, int, *types.CodeWikiRunPage) error
	RefreshTopicRun(context.Context, uint64, string, string, int) (*types.CodeWikiRun, error)
	ReserveTopicModelCall(context.Context, *types.CodeWikiRun, string, int) (bool, error)
	FinishTopicModelCall(context.Context, *types.CodeWikiRun, string, int64) error
	RefundTopicModelCall(context.Context, *types.CodeWikiRun, string) error
	RetryTopic(context.Context, uint64, string, string, string) error
	AddTopicReview(context.Context, uint64, string, string, string, int, types.CodeWikiTopicReview) error
}

type CodeWikiTopicService interface {
	GetRunTopicPage(context.Context, string, string, string, string, int) (*types.CodeWikiTopicPage, error)
	ListRunTopics(context.Context, string, string) ([]*types.CodeWikiTopic, error)
	RetryRunTopic(context.Context, string, string, string) error
	GetRunTopic(context.Context, string, string, string, string, int) (*types.CodeWikiTopicDetail, error)
	ReviewRunTopic(context.Context, string, string, string, *types.CodeWikiTopicReviewRequest) error
	GetRunTopicSource(context.Context, string, string, string, string, int) (*types.CodeWikiTopicSource, error)
}

type CodeWikiProgressRepository interface {
	ReadRunProgress(context.Context, uint64, string, string) (*types.CodeWikiProgressSnapshot, error)
}

type CodeWikiProgressService interface {
	GetRunProgress(context.Context, string, string) (*types.CodeWikiProgressSnapshot, error)
}
