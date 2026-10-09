package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
	"github.com/qiankunli/case-code-review/internal/harness/compactor"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

type contextCompactorFunc func(context.Context, agentgo.TransformContext, float64) ([]agentgo.AgentMessage, error)

func (f contextCompactorFunc) Compact(ctx context.Context, input agentgo.TransformContext, expect float64) ([]agentgo.AgentMessage, error) {
	return f(ctx, input, expect)
}

func TestContextSyncKeepsActualRequestCoverage(t *testing.T) {
	file := msg.NewFile("pkg/a.go", 1, 3, 3, "File: pkg/a.go (Total lines: 3)\n1|package a\n2|\n3|func A() {}\n")
	baseline := []agentgo.AgentMessage{agentgo.UserMsg("review"), file}
	manager := newContextManager(ExecutionSpec{FileDedupEnabled: true}, nil)
	manager.Sync(baseline)
	// A recovery view can omit raw baseline evidence. Only content sent to the
	// model may suppress read_files, including after its response is accepted.
	if _, err := manager.Transform(t.Context(), agentgo.TransformContext{Messages: baseline[:1]}); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot(); got.TranscriptMessages != 2 || got.ActiveMessages != 1 {
		t.Fatalf("transform replaced baseline: %+v", got)
	}
	manager.Sync(append(baseline, agentgo.Message{Role: agentgo.RoleAssistant}))
	if _, covered := manager.coveredFileRead(tool.FileReadRequest{FilePath: "pkg/a.go"}); covered {
		t.Fatal("raw baseline suppressed a legitimate reread")
	}
	if _, err := manager.Transform(t.Context(), agentgo.TransformContext{Messages: baseline}); err != nil {
		t.Fatal(err)
	}
	manager.Sync(append(baseline, agentgo.Message{Role: agentgo.RoleAssistant}))
	if _, covered := manager.coveredFileRead(tool.FileReadRequest{FilePath: "pkg/a.go"}); !covered {
		t.Fatal("accepting a response lost actual request coverage")
	}
}

func TestContextCompactionUsesBaselineBeforeMaterialProjection(t *testing.T) {
	body := strings.Repeat("document evidence ", 100)
	messages := []agentgo.AgentMessage{newMaterialTestMessage("first", "", "", body), newMaterialTestMessage("second", "", "", body)}
	projected := ProjectContext(messages, msg.Artifacts(messages), true)
	baselineTokens, viewTokens := agentcontext.EstimateTotal(messages), agentcontext.EstimateTotal(projected)
	if viewTokens >= baselineTokens {
		t.Fatal("fixture did not reduce duplicate material")
	}
	window := ((baselineTokens + viewTokens) / 2) * 5 / 4
	manager := newContextManager(ExecutionSpec{ContextWindow: window, FileDedupEnabled: true}, nil)
	calls := 0
	manager.engine = agentcontext.NewEngine(agentcontext.EngineConfig{ContextWindow: window, ReserveTokens: max(window/5, 1), Compactor: contextCompactorFunc(func(_ context.Context, input agentgo.TransformContext, _ float64) ([]agentgo.AgentMessage, error) {
		calls++
		if len(input.Messages) != 2 || !strings.Contains(input.Messages[1].TextContent(), body) {
			t.Error("request-local reference entered compaction")
		}
		return input.Messages, nil
	})})
	if _, err := manager.Compact(t.Context(), agentgo.TransformContext{Messages: messages}, agentgo.CompactReasonThreshold); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("projection incorrectly suppressed baseline compaction: calls=%d", calls)
	}
}

func TestZoneCompactionReceivesStagedArtifactsAndPublishesAcceptedEvent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "failed"}[fail], func(t *testing.T) {
			failure := errors.New("archive failed")
			manager := newContextManager(ExecutionSpec{ContextWindow: 400}, nil)
			calls, models, commits, compactEvents := 0, 0, 0, 0
			stage := contextCompactorFunc(func(_ context.Context, input agentgo.TransformContext, _ float64) ([]agentgo.AgentMessage, error) {
				calls++
				if _, ok := input.Artifacts.GetArtifact("test:retained"); !ok {
					t.Error("zone stage lost artifacts")
				}
				if err := input.Artifacts.AddArtifact(testMaterialArtifact("archive"), false); err != nil {
					return nil, err
				}
				if fail {
					return nil, failure
				}
				reduced, _ := input.Messages[0].Compact(0)
				return []agentgo.AgentMessage{reduced}, nil
			})
			manager.engine = agentcontext.NewEngine(agentcontext.EngineConfig{ContextWindow: 400, ReserveTokens: 80,
				Compactor: &compactor.ZoneCompactor{KeepRecentTokens: 1, LimitTokens: 320, Stages: []compactor.Stage{{Name: "archive", Compactor: stage}}},
			})
			var final *agentgo.AgentState
			for event := range agentgo.AgentLoop(t.Context(), []agentgo.AgentMessage{msg.FixedText("user", "review"), msg.NewFile("old.go", 1, 200, 200, strings.Repeat("1|source line\n", 200))}, agentgo.AgentContext{}, agentgo.LoopConfig{
				ContextManager: manager, InitialState: agentgo.AgentState{Artifacts: []agentgo.Artifact{testMaterialArtifact("retained")}},
				CommitContext: func(result agentgo.ContextCommitResult) error {
					commits++
					if len(result.Artifacts) != 2 || result.Compaction == nil || result.Compaction.Committed {
						t.Errorf("invalid candidate: %+v", result)
					}
					return nil
				},
				ModelMiddlewares: []agentgo.ModelMiddleware{func(_ context.Context, execution agentgo.ModelExecution, _ agentgo.ModelExecuteFunc) (agentgo.ModelResult, error) {
					models++
					if _, ok := execution.Artifacts.GetArtifact("test:archive"); !ok {
						t.Error("accepted material absent from model call")
					}
					return agentgo.ModelResult{Message: agentgo.Message{Role: agentgo.RoleAssistant, StopReason: agentgo.StopReasonStop}}, nil
				}},
			}) {
				if event.Type == agentgo.EventContextCompacted {
					compactEvents++
					if !event.Compaction.Committed {
						t.Error("event reports unaccepted compaction")
					}
				}
				if event.Type == agentgo.EventAgentEnd {
					final = event.State
					if fail {
						if !errors.Is(event.Err, failure) {
							t.Errorf("error=%v", event.Err)
						}
					} else if event.Err != nil {
						t.Error(event.Err)
					}
				}
			}
			if fail {
				if calls != 1 || models != 0 || commits != 0 || compactEvents != 0 || final == nil || len(final.Artifacts) != 1 {
					t.Fatalf("failed preparation leaked: calls=%d models=%d commits=%d events=%d final=%+v", calls, models, commits, compactEvents, final)
				}
				return
			}
			if calls != 1 || models != 1 || commits != 1 || compactEvents != 1 || final == nil || len(final.Artifacts) != 2 {
				t.Fatalf("calls=%d models=%d commits=%d events=%d final=%+v", calls, models, commits, compactEvents, final)
			}

		})
	}
}
