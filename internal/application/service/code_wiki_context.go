package service

import (
	"github.com/Tencent/WeKnora/internal/agent/token"
	"github.com/Tencent/WeKnora/internal/models/chat"
)

func codeWikiContextBudget(cfg codeWikiRunConfig, messages []chat.Message) (int, error) {
	window := cfg.ContextWindow
	if window <= 0 {
		window = 32768
	}
	output := cfg.MaxOutputTokens
	if output <= 0 || output > 4096 {
		output = 4096
	}
	estimator, err := token.NewEstimator()
	if err != nil {
		return 0, err
	}
	// The tokenizer is approximate for other model families. Reserve 25%
	// estimation error, output capacity and tool/protocol headroom explicitly.
	if estimator.EstimateMessages(messages)*5/4+output+2048 > window {
		return 0, errTopicContext
	}
	return output, nil
}
