package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type codeWikiBlockingChat struct {
	codeWikiUnusedChat
	started     chan struct{}
	late        <-chan struct{}
	beforeReply func() error
}

func (m *codeWikiBlockingChat) Chat(ctx context.Context, _ []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
	close(m.started)
	if m.beforeReply != nil {
		if err := m.beforeReply(); err != nil {
			return nil, err
		}
		return &types.ChatResponse{Content: "output after stop must not be accepted"}, nil
	}
	if m.late != nil {
		// Simulate a provider that returns successfully even after cancellation.
		<-m.late
		return &types.ChatResponse{Content: "late output must not be accepted"}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCodeWikiControlsInterruptInFlightModelsWithoutWaitingForCompletion(t *testing.T) {
	for _, phase := range []string{"planning", "topic"} {
		for _, control := range []string{"pause", "cancel"} {
			t.Run(phase+"/"+control, func(t *testing.T) {
				s, db, run, _ := topicServiceFixture(t)
				s.modelService = topicFlowModelService{}
				topicRepo := s.repo.(interfaces.CodeWikiTopicRepository)
				storedTopic, err := topicRepo.GetTopic(context.Background(), 7, "kb", run.ID, "0")
				require.NoError(t, err)
				topic, err := topicRepo.ClaimTopic(context.Background(), 7, "kb", run.ID, "0", run.ExecutionEpoch, storedTopic.Revision, "worker")
				require.NoError(t, err)
				require.NotNil(t, topic)
				originalCheckpoint := string(topic.Checkpoint)
				model := &codeWikiBlockingChat{started: make(chan struct{})}
				state := &codeWikiRunState{run: run, cfg: codeWikiRunConfig{MaxModelCalls: 100}, chat: model, interruptCheck: func() error { return nil }}
				ctx, stop := s.watchRunControl(context.Background(), run, phase == "topic")
				defer stop()
				result := make(chan error, 1)
				go func() {
					if phase == "topic" {
						_, err := s.callTopicModel(ctx, state, topic, "analyze implementation")
						result <- err
					} else {
						_, err := s.callModel(ctx, state, "plan directory")
						result <- err
					}
				}()
				select {
				case <-model.started:
				case <-time.After(3 * time.Second):
					t.Fatal("model request did not start")
				}
				if control == "cancel" {
					require.NoError(t, s.CancelRun(context.Background(), "kb", run.ID))
				} else {
					require.NoError(t, s.PauseRun(context.Background(), "kb", run.ID))
				}
				want := errCodeWikiRunPaused
				if control == "cancel" {
					want = errCodeWikiRunCanceled
				}
				select {
				case err := <-result:
					require.ErrorIs(t, err, want)
				case <-time.After(3 * time.Second):
					t.Fatal("control waited for model completion instead of interrupting the request")
				}
				var slots int64
				require.NoError(t, db.Model(&types.CodeWikiModelSlot{}).Where("run_id = ?", run.ID).Count(&slots).Error)
				require.Zero(t, slots, "interrupted model request must release its reservation")
				topic.Status, topic.Action = "paused", "执行已中断，已保存进度"
				require.NoError(t, topicRepo.SaveTopic(context.Background(), topic, run.ExecutionEpoch, topic.Revision))
				current, err := topicRepo.RefreshTopicRun(context.Background(), 7, "kb", run.ID, run.ExecutionEpoch)
				require.NoError(t, err)
				wantStatus := types.CodeWikiRunStatusPaused
				if control == "cancel" {
					wantStatus = types.CodeWikiRunStatusCanceled
				}
				require.Equal(t, wantStatus, current.Status)
				stored, err := topicRepo.GetTopic(context.Background(), 7, "kb", run.ID, "0")
				require.NoError(t, err)
				require.Equal(t, originalCheckpoint, string(stored.Checkpoint))
			})
		}
	}
}

func TestCodeWikiControlsRejectLateSuccessfulModelOutput(t *testing.T) {
	for _, phase := range []string{"planning", "topic"} {
		t.Run(phase, func(t *testing.T) {
			s, _, run, _ := topicServiceFixture(t)
			s.modelService = topicFlowModelService{}
			topicRepo := s.repo.(interfaces.CodeWikiTopicRepository)
			storedTopic, err := topicRepo.GetTopic(context.Background(), 7, "kb", run.ID, "0")
			require.NoError(t, err)
			topic, err := topicRepo.ClaimTopic(context.Background(), 7, "kb", run.ID, "0", run.ExecutionEpoch, storedTopic.Revision, "worker")
			require.NoError(t, err)
			require.NotNil(t, topic)
			late := make(chan struct{})
			model := &codeWikiBlockingChat{started: make(chan struct{}), late: late}
			state := &codeWikiRunState{run: run, cfg: codeWikiRunConfig{MaxModelCalls: 100}, chat: model, interruptCheck: func() error { return nil }}
			ctx, stop := s.watchRunControl(context.Background(), run, phase == "topic")
			defer stop()
			type outcome struct {
				content string
				err     error
			}
			result := make(chan outcome, 1)
			go func() {
				var content string
				var err error
				if phase == "topic" {
					content, err = s.callTopicModel(ctx, state, topic, "analyze")
				} else {
					content, err = s.callModel(ctx, state, "plan")
				}
				result <- outcome{content, err}
			}()
			select {
			case <-model.started:
			case <-time.After(3 * time.Second):
				t.Fatal("model request did not start")
			}
			require.NoError(t, s.repo.RequestRunControl(context.Background(), "kb", run.ID, "pause"))
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("worker in another process did not observe persisted control")
			}
			close(late)
			select {
			case got := <-result:
				require.Empty(t, got.content)
				require.ErrorIs(t, got.err, errCodeWikiRunPaused)
			case <-time.After(3 * time.Second):
				t.Fatal("late request did not settle")
			}
		})
	}
}

func TestCodeWikiPlannerControlWatchAllowsItsOwnTopicEpochTransition(t *testing.T) {
	s, db, run, _ := topicServiceFixture(t)
	ctx, stop := s.watchRunControl(context.Background(), run, false)
	defer stop()
	require.NoError(t, db.Model(&types.CodeWikiRun{}).Where("id = ?", run.ID).Update("execution_epoch", run.ExecutionEpoch+1).Error)
	require.NoError(t, db.Model(&types.CodeWikiRun{}).Where("id = ?", run.ID).Update("execution_deadline", time.Now().Add(-time.Minute)).Error)
	// Planner updates its own in-memory snapshot on dispatch; the watcher must
	// neither race on that field nor mistake the normal transition for a stop.
	run.ExecutionEpoch++
	select {
	case <-ctx.Done():
		t.Fatalf("planner canceled its own dispatch: %v", context.Cause(ctx))
	case <-time.After(2 * codeWikiControlPollInterval):
	}
	require.NoError(t, s.repo.RequestRunControl(context.Background(), "kb", run.ID, "cancel"))
	select {
	case <-ctx.Done():
		require.ErrorIs(t, context.Cause(ctx), errCodeWikiRunCanceled)
	case <-time.After(3 * time.Second):
		t.Fatal("cancel was not observed after dispatch transition")
	}
}

func TestCodeWikiControlsStopQueuedTopicsWithoutWaitingForDispatcher(t *testing.T) {
	for _, control := range []string{"pause", "cancel"} {
		t.Run(control, func(t *testing.T) {
			s, _, run, _ := topicServiceFixture(t)
			ctx := context.Background()
			want := types.CodeWikiRunStatusPaused
			if control == "pause" {
				require.NoError(t, s.PauseRun(ctx, "kb", run.ID))
			} else {
				require.NoError(t, s.CancelRun(ctx, "kb", run.ID))
				want = types.CodeWikiRunStatusCanceled
			}
			stored, err := s.repo.GetRun(ctx, "kb", run.ID)
			require.NoError(t, err)
			require.Equal(t, want, stored.Status)
			rows, err := s.repo.(interfaces.CodeWikiTopicRepository).ListTopicStates(ctx, 7, "kb", run.ID)
			require.NoError(t, err)
			for _, row := range rows {
				require.Equal(t, "paused", row.Status)
			}
		})
	}
}

func TestCodeWikiControlsRejectReplyBeforeNextControlPoll(t *testing.T) {
	for _, phase := range []string{"planning", "topic"} {
		for _, control := range []string{"pause", "cancel"} {
			t.Run(phase+"/"+control, func(t *testing.T) {
				s, _, run, _ := topicServiceFixture(t)
				s.modelService = topicFlowModelService{}
				topicRepo := s.repo.(interfaces.CodeWikiTopicRepository)
				stored, err := topicRepo.GetTopic(context.Background(), 7, "kb", run.ID, "0")
				require.NoError(t, err)
				topic, err := topicRepo.ClaimTopic(context.Background(), 7, "kb", run.ID, "0", run.ExecutionEpoch, stored.Revision, "worker")
				require.NoError(t, err)
				require.NotNil(t, topic)
				model := &codeWikiBlockingChat{started: make(chan struct{}), beforeReply: func() error {
					return s.repo.RequestRunControl(context.Background(), "kb", run.ID, control)
				}}
				state := &codeWikiRunState{run: run, cfg: codeWikiRunConfig{MaxModelCalls: 100}, chat: model,
					interruptCheck: func() error { return s.readRunControl(context.Background(), run, phase == "topic") }}
				// No polling watcher: the reply check itself must reject a stop
				// that arrived during this exact request.
				var content string
				if phase == "topic" {
					content, err = s.callTopicModel(context.Background(), state, topic, "analyze")
				} else {
					content, err = s.callModel(context.Background(), state, "plan")
				}
				require.Empty(t, content)
				want := errCodeWikiRunPaused
				if control == "cancel" {
					want = errCodeWikiRunCanceled
				}
				require.ErrorIs(t, err, want)
			})
		}
	}
}
