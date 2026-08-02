package llmloop

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

const (
	// wrapUpTimeReserve: when less than this remains on the ctx deadline,
	// stop investigating and force a verdict. Sized to fit wrapUpMaxRounds
	// typical LLM rounds — smaller and the wrap-up itself gets cut off.
	wrapUpTimeReserve = 90 * time.Second
	// wrapUpRoundReserve: force the verdict when this many tool rounds
	// remain (counting the current one), so round-budget exhaustion ends in
	// an explicit task_done instead of a silent partial.
	wrapUpRoundReserve = 2
	// wrapUpMaxRounds caps the rounds granted once a deadline wrap-up is
	// issued: what's left of the budget goes to concluding, not digging.
	wrapUpMaxRounds = 2
	// maxBulletinsPerLoop caps model-initiated post_bulletin publishes per unit
	// loop — the board must stay a few cards, not a firehose (docs/unit_review.md
	// capacity defenses), and an unbounded tool invites filler posts the same
	// way unbounded findings invite padding.
	maxBulletinsPerLoop = 3
	// maxBulletinTextRunes truncates one bulletin's text so a single verbose
	// note cannot eat the byte-capped board digest.
	maxBulletinTextRunes = 300
)

const defaultWrapUpPrompt = "BUDGET NEARLY EXHAUSTED — stop investigating now. Report any confident results with the available result tool, then call task_done. State explicitly which parts you completed. Do not call investigation tools."

// Outcome is how a unit's main loop terminated — the terminal state a debrief
// records. RunPerFile computes it anyway (completed / truncation reason /
// deadline / LLM error); returning it saves callers re-deriving the ending from
// warnings, where two units of one file would collide on the path key.
type Outcome struct {
	State  string // OutcomeCompleted | OutcomeTruncated | OutcomeTimeout | OutcomeLLMError
	Reason string // truncation reason or error text; "" when completed
	// Review Team board activity for this unit's loop (0 when the board is off).
	BoardPulled         int // peer bulletins injected into this loop
	BoardInjectedTokens int // their rough token cost
	BoardPosted         int // facts this loop published for peers
}

const (
	OutcomeCompleted = "completed" // explicit task_done
	OutcomeTruncated = "truncated" // ended without task_done (rounds / empty rounds / compression)
	OutcomeTimeout   = "timeout"   // ctx deadline exceeded
	OutcomeLLMError  = "llm_error" // a completion call failed terminally
)

// Deps bundles the generic execution dependencies for one Harness loop.
type Deps struct {
	LLMClient    llm.LLMClient
	Model        string
	Template     template.Template
	Tools        *tool.Registry
	MainToolDefs []llm.ToolDef
	Session      *session.SessionHistory
	// WrapUpPrompt is injected by the domain when its result tool needs
	// specific instructions. The default only assumes task_done.
	WrapUpPrompt string
	// ToolCallHook lets a domain extension intercept a parsed tool call. The
	// Harness remains responsible for loop control, while the hook owns domain
	// semantics such as turning code_comment arguments into findings.
	ToolCallHook ToolCallHook
	// FileDedupEnabled gates read_files result deduplication (the file_dedup
	// feature gate): a later read covering an earlier one stubs the earlier
	// copy in place, so the model pays for file content once.
	FileDedupEnabled bool
	// FileEvictEnabled gates eviction of File messages under token pressure
	// (the file_evict feature gate): before paying for an LLM compression
	// pass, shed the re-derivable slice of the context — file content the
	// model can always read again.
	FileEvictEnabled bool
	// Board is the Review Team's shared case board (docs/unit_review.md); nil =
	// no team (today's behavior, prompt byte-identical). When set, each turn
	// pulls peers' relevant bulletins and auto-publishes this loop's facts.
	Board board.Board
	// PostBulletinEnabled gates the model-initiated post_bulletin tool (the
	// post_bulletin feature gate). Requires Board: NewRunner strips the tool's
	// def from MainToolDefs when either is missing, so the model never sees a
	// tool it cannot use.
	PostBulletinEnabled bool
}

// Runner is a per-session (across files) executor of the LLM tool-use
// loop. Token counters, warnings, and the optional background compression
// job are aggregated across every RunPerFile call.
type Runner struct {
	deps                  Deps
	totalInputTokens      int64 // atomically updated
	totalOutputTokens     int64
	totalCacheReadTokens  int64
	totalCacheWriteTokens int64
	warningsMu            sync.Mutex
	warnings              []Warning
	toolCallsMu           sync.Mutex
	toolCalls             map[string]int64
	modelsMu              sync.Mutex
	models                map[string]int // routing alias -> #responses it served this run (dedup by key)
	compressionMu         sync.Mutex
	pendingJob            *compressionJob
}

// NewRunner returns a Runner bound to the given dependencies.
func NewRunner(deps Deps) *Runner {
	if deps.WrapUpPrompt == "" {
		deps.WrapUpPrompt = defaultWrapUpPrompt
	}
	// post_bulletin only exists as a callable tool when there is a board to post
	// to AND the gate is on; otherwise drop its def so the tool list matches
	// reality (a def without a handler would burn rounds on NotAvailable).
	if deps.Board == nil || !deps.PostBulletinEnabled {
		deps.MainToolDefs = slices.DeleteFunc(slices.Clone(deps.MainToolDefs), func(d llm.ToolDef) bool {
			return d.Function.Name == tool.PostBulletin.Name()
		})
	}
	return &Runner{deps: deps}
}

// TotalInputTokens returns the accumulated input/prompt tokens from all LLM calls.
func (r *Runner) TotalInputTokens() int64 { return atomic.LoadInt64(&r.totalInputTokens) }

// TotalOutputTokens returns the accumulated completion tokens from all LLM calls.
func (r *Runner) TotalOutputTokens() int64 { return atomic.LoadInt64(&r.totalOutputTokens) }

// TotalCacheReadTokens returns the accumulated cache read tokens.
func (r *Runner) TotalCacheReadTokens() int64 { return atomic.LoadInt64(&r.totalCacheReadTokens) }

// TotalCacheWriteTokens returns the accumulated cache write tokens.
func (r *Runner) TotalCacheWriteTokens() int64 { return atomic.LoadInt64(&r.totalCacheWriteTokens) }

// TotalTokensUsed returns input + output.
func (r *Runner) TotalTokensUsed() int64 {
	return r.TotalInputTokens() + r.TotalOutputTokens()
}

// Warnings returns a copy of the accumulated warnings.
func (r *Runner) Warnings() []Warning {
	r.warningsMu.Lock()
	defer r.warningsMu.Unlock()
	out := make([]Warning, len(r.warnings))
	copy(out, r.warnings)
	return out
}

// RecordWarning adds a non-fatal warning.
func (r *Runner) RecordWarning(warningType, file, message string) {
	r.warningsMu.Lock()
	r.warnings = append(r.warnings, Warning{
		File:    file,
		Message: message,
		Type:    warningType,
	})
	r.warningsMu.Unlock()
}

// ToolCalls returns a snapshot of the per-tool call counts.
func (r *Runner) ToolCalls() map[string]int64 {
	r.toolCallsMu.Lock()
	defer r.toolCallsMu.Unlock()
	out := make(map[string]int64, len(r.toolCalls))
	for k, v := range r.toolCalls {
		out[k] = v
	}
	return out
}

func (r *Runner) recordToolCall(name string) {
	r.toolCallsMu.Lock()
	if r.toolCalls == nil {
		r.toolCalls = make(map[string]int64)
	}
	r.toolCalls[name]++
	r.toolCallsMu.Unlock()
}

// recordModel counts one response served by a routing alias. Empty alias
// (single-model / non-routing config) is ignored — there's no alias to report.
func (r *Runner) recordModel(alias string) {
	if alias == "" {
		return
	}
	r.modelsMu.Lock()
	if r.models == nil {
		r.models = make(map[string]int)
	}
	r.models[alias]++
	r.modelsMu.Unlock()
}

// ModelsUsed returns the run's model identity: each routing alias that served a
// response mapped to how many it served (deduped by key). Run-level — present
// even when no finding was produced. Empty for a single-model (non-routing) run.
func (r *Runner) ModelsUsed() map[string]int {
	r.modelsMu.Lock()
	defer r.modelsMu.Unlock()
	out := make(map[string]int, len(r.models))
	for alias, n := range r.models {
		out[alias] = n
	}
	return out
}

// RecordUsage adds the prompt/completion/cache tokens reported by an LLM
// response to the runner's aggregate counters. Used by callers (plan phase
// in agent / future scan phases) that perform their own LLM calls outside
// RunPerFile.
func (r *Runner) RecordUsage(u *llm.UsageInfo) {
	if u == nil {
		return
	}
	atomic.AddInt64(&r.totalInputTokens, u.PromptTokens)
	atomic.AddInt64(&r.totalOutputTokens, u.CompletionTokens)
	atomic.AddInt64(&r.totalCacheReadTokens, u.CacheReadTokens)
	atomic.AddInt64(&r.totalCacheWriteTokens, u.CacheWriteTokens)
}

// RunPerFile drives the main LLM conversation loop for a single file.
// It sends messages with the configured tool definitions, executes any
// tool calls returned by the model, and collects review comments until
// task_done is called or limits are reached. Token usage and warnings
// are aggregated on the Runner across all files.
//
// The conversation's currency is the review-domain []msg.Msg; the wire form
// (llm.Message) exists only at the msg.Lower call sites — the API request,
// the session record, and the compression prompt (see docs/harness.md).
//
// Truncation discipline: a chain that ends WITHOUT task_done is a partial
// verdict, and silence would read as "clean" downstream (measured on real
// trajectories: the timed-out chains were exactly the big-PR blind spots).
// Two guards enforce an explicit ending: a wrap-up turn is injected when the
// time or round budget is nearly exhausted (forcing a verdict while there is
// still budget to say it), and any chain that still ends without task_done
// records a unit_incomplete warning instead of returning silently.
func (r *Runner) RunPerFile(ctx context.Context, messages []msg.Msg, sc session.Scope) (Outcome, error) {
	newPath := sc.Path() // representative path for logging
	toolReqCount := r.deps.Template.MaxToolRequestTimes
	const maxConsecutiveEmptyRounds = 3
	consecutiveEmptyRounds := 0
	wrapUpIssued := false
	completed := false
	truncateReason := ""
	turn := 0
	var boardPulled, boardTokens, boardPosted int
	bulletinBudget := maxBulletinsPerLoop
	// finish stamps the loop's board activity onto every Outcome return.
	finish := func(o Outcome) Outcome {
		o.BoardPulled, o.BoardInjectedTokens, o.BoardPosted = boardPulled, boardTokens, boardPosted
		return o
	}

	for toolReqCount > 0 {
		turn++
		select {
		case <-ctx.Done():
			r.RecordWarning("unit_incomplete", newPath,
				"review ended without task_done (deadline exceeded); verdict is partial — do not read as clean")
			return finish(Outcome{State: OutcomeTimeout, Reason: "deadline exceeded"}), ctx.Err()
		default:
		}

		if !wrapUpIssued {
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wrapUpTimeReserve {
				wrapUpIssued = true
				// Cap the remaining rounds: the point of wrap-up is to spend
				// what's left of the deadline on a verdict, not on more digging.
				if toolReqCount > wrapUpMaxRounds {
					toolReqCount = wrapUpMaxRounds
				}
				messages = append(messages, msg.Text("user", r.deps.WrapUpPrompt))
				fmt.Fprintf(console.Out(), "[ccr] Budget nearly exhausted for %s — forcing wrap-up (deadline)\n", newPath)
			} else if toolReqCount == wrapUpRoundReserve {
				wrapUpIssued = true
				messages = append(messages, msg.Text("user", r.deps.WrapUpPrompt))
				fmt.Fprintf(console.Out(), "[ccr] Budget nearly exhausted for %s — forcing wrap-up (rounds)\n", newPath)
			}
		}

		toolReqCount--

		// Review Team: pull peers' relevant bulletins at the turn boundary
		// (docs/unit_review.md). Injection is directed + incremental + capped by
		// the board, so an empty digest costs nothing; a non-empty one enters as
		// an evictable Board message.
		if r.deps.Board != nil {
			if digest, n := r.deps.Board.Pull(sc.ID); n > 0 {
				messages = append(messages, msg.NewBoard(digest))
				boardPulled += n
				boardTokens += llm.CountTokens(digest)
			}
		}

		// Lower once per round: the session record and the API call see the same
		// wire form the model does.
		wire := msg.Lower(messages)
		fs := r.deps.Session.GetOrCreateScope(sc)
		rec := fs.AppendTaskRecord(session.MainTask, wire)
		startTime := time.Now()

		resp, err := r.deps.LLMClient.CompletionsWithCtx(ctx, llm.ChatRequest{
			Model:     r.deps.Model,
			Messages:  wire,
			Tools:     r.deps.MainToolDefs,
			MaxTokens: r.deps.Template.MaxTokens,
		})
		duration := time.Since(startTime)
		if err != nil {
			rec.SetError(err, duration)
			telemetry.RecordLLMRequest(ctx, r.deps.Model, duration, 0, "error")
			// A deadline hit mid-call surfaces as an LLM error; report it as the
			// timeout it is so debriefs don't misclassify slow units as API failures.
			if ctx.Err() != nil {
				return finish(Outcome{State: OutcomeTimeout, Reason: "deadline exceeded"}), fmt.Errorf("LLM completion error: %w", err)
			}
			return finish(Outcome{State: OutcomeLLMError, Reason: err.Error()}), fmt.Errorf("LLM completion error: %w", err)
		}
		rec.SetResponse(resp, duration)
		r.recordModel(resp.Alias) // run-level model identity; counts every response, not just ones with findings
		totalTokens := int64(0)
		if resp.Usage != nil {
			totalTokens = resp.Usage.TotalTokens
			atomic.AddInt64(&r.totalInputTokens, resp.Usage.PromptTokens)
			atomic.AddInt64(&r.totalOutputTokens, resp.Usage.CompletionTokens)
			atomic.AddInt64(&r.totalCacheReadTokens, resp.Usage.CacheReadTokens)
			atomic.AddInt64(&r.totalCacheWriteTokens, resp.Usage.CacheWriteTokens)
		}
		telemetry.RecordLLMRequest(ctx, r.deps.Model, duration, totalTokens, "ok")

		content := resp.Content()
		calls := resp.ToolCalls()

		if len(calls) == 0 {
			fmt.Fprintf(console.Out(), "[ccr] No tool calls parsed for %s, retrying...\n", newPath)
			messages = append(messages, msg.Text("user", "You did not successfully call any tools. Please try again or use task_done if finished."))
			if content != "" {
				messages = append(messages[:len(messages)-1], msg.Text("assistant", content), messages[len(messages)-1])
			}
			continue
		}

		var results []tool.ToolCallResult
		taskCompleted := false
		hasValidResult := false

		for _, call := range calls {
			// post_bulletin is loop-owned, not a Registry provider: publishing
			// needs the scope identity, current turn, and per-loop budget that
			// only this loop holds. Without a board/gate it falls through to
			// executeToolCall's NotAvailable path like any unregistered tool.
			if call.Function.Name == tool.PostBulletin.Name() && r.deps.Board != nil && r.deps.PostBulletinEnabled {
				r.recordToolCall(tool.PostBulletin.Name())
				res, posted := r.handlePostBulletin(sc.ID, turn, call, &bulletinBudget)
				if posted {
					boardPosted++
				}
				results = append(results, tool.ToolCallResult{
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Result:     res,
				})
				hasValidResult = true
				continue
			}
			cp := r.executeToolCall(ctx, sc, call, rec, resp.Alias)
			if cp.Completed {
				results = append(results, tool.ToolCallResult{
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Result:     "Task completed successfully.",
				})
				taskCompleted = true
			} else if cp.Data != "" {
				results = append(results, tool.ToolCallResult{
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Result:     cp.Data,
				})
				hasValidResult = true
			} else {
				results = append(results, tool.ToolCallResult{
					ToolCallID: call.ID,
					Name:       call.Function.Name,
					Result:     "Error: Tool execution returned no result.",
				})
			}
		}

		// Review Team: auto-publish this turn's facts for peers (docs/unit_review.md
		// D2 — the engine extracts facts from tool calls for free; the model
		// never spends a round on it). v0 publishes read + flag facts, keyed by
		// path so peers watching the same file receive them.
		if r.deps.Board != nil {
			facts := extractFacts(sc, turn, calls)
			if hook, ok := r.deps.ToolCallHook.(ToolFactHook); ok {
				facts = append(facts, hook.Facts(sc, turn, calls)...)
			}
			for _, b := range facts {
				r.deps.Board.Publish(b)
				boardPosted++
			}
		}

		if taskCompleted {
			completed = true
			break
		}
		if !hasValidResult {
			consecutiveEmptyRounds++
			if consecutiveEmptyRounds >= maxConsecutiveEmptyRounds {
				fmt.Fprintf(console.Out(), "[ccr] Too many empty retries for %s, stopping.\n", newPath)
				truncateReason = "consecutive empty tool rounds"
				break
			}
			fmt.Fprintf(console.Out(), "[ccr] No valid tool results for %s, retrying...\n", newPath)
		} else {
			consecutiveEmptyRounds = 0
		}

		succeed := r.addNextMessage(ctx, content, calls, results, &messages, sc)
		if !succeed {
			fmt.Fprintf(console.Out(), "[ccr] Context compression exceeded threshold for %s, stopping.\n", newPath)
			truncateReason = "context compression limit"
			break
		}
	}

	if !completed {
		if truncateReason == "" {
			truncateReason = "tool-round budget exhausted"
			fmt.Fprintf(console.Out(), "[ccr] Max tool requests reached for %s.\n", newPath)
		}
		r.RecordWarning("unit_incomplete", newPath,
			"review ended without task_done ("+truncateReason+"); verdict is partial — do not read as clean")
		return finish(Outcome{State: OutcomeTruncated, Reason: truncateReason}), nil
	}
	return finish(Outcome{State: OutcomeCompleted}), nil
}

// executeToolCall dispatches a single tool call from the LLM response and
// records the result in session history. Domain-specific tools may be
// intercepted by ToolCallHook after their arguments are decoded.
func (r *Runner) executeToolCall(ctx context.Context, sc session.Scope, call llm.ToolCall, rec *session.TaskRecord, alias string) tool.TaskCheckpoint {
	t := tool.OfName(call.Function.Name)
	if !t.IsKnown() {
		return tool.Of(tool.NotAvailableMsg)
	}

	if t == tool.TaskDone {
		return tool.Complete()
	}

	p := lookupTool(r.deps.Tools, t)
	if p == nil {
		return tool.Of(tool.NotAvailableMsg)
	}

	r.recordToolCall(t.Name())

	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return tool.Of(fmt.Sprintf("Error parsing tool arguments for %s: %v", t.Name(), err))
	}

	if r.deps.ToolCallHook != nil {
		if result, handled := r.deps.ToolCallHook.Handle(ctx, HookCall{
			Scope: sc, Tool: t, Call: call, Args: args, Record: rec, Alias: alias,
		}); handled {
			return result
		}
	}

	// Synchronous path for all other tools
	startTime := time.Now()
	telemetry.PrintToolCallStarted(t.Name(), args)
	result, err := p.Execute(ctx, args)
	dur := time.Since(startTime)
	ok := err == nil
	telemetry.RecordToolCall(ctx, t.Name(), dur, ok)

	if err != nil {
		telemetry.PrintToolCallError(t.Name(), err)
		return tool.Of(fmt.Sprintf("Error executing tool %s: %v", t.Name(), err))
	}
	telemetry.PrintToolCallFinished(t.Name(), dur)
	if rec != nil {
		rec.AddToolResult(t.Name(), call.Function.Arguments, result)
	}
	return tool.Of(result)
}

// addNextMessage extends the conversation with the assistant message and
// tool responses, applying three-zone compression at the soft (60%) and
// warning (80%) MaxTokens thresholds. Returns false when even after
// synchronous compression the conversation is still over the warning
// threshold — caller should stop the loop in that case.
func (r *Runner) addNextMessage(ctx context.Context, assistantContent string, toolCalls []llm.ToolCall, results []tool.ToolCallResult, messages *[]msg.Msg, sc session.Scope) bool {
	maxAllowed := r.deps.Template.MaxTokens
	softLimit := int(float64(maxAllowed) * tokenSoftThreshold)
	warnLimit := int(float64(maxAllowed) * tokenWarningThreshold)

	r.tryApplyPendingCompression(messages)

	tokenCount := countMsgTokens(*messages)

	// Over the warning threshold: shed re-derivable file content first (free,
	// deterministic), and only summarize if that wasn't enough.
	if tokenCount > warnLimit && r.deps.FileEvictEnabled {
		if n := evictReclaimable(*messages, warnLimit); n > 0 {
			telemetry.Event(ctx, "context.file_evict",
				telemetry.AnyToAttr("file.path", sc.Path()),
				telemetry.AnyToAttr("evicted", n))
		}
		tokenCount = countMsgTokens(*messages)
	}
	if tokenCount > warnLimit {
		r.cancelPendingCompression()
		*messages, _ = r.runCompression(ctx, *messages, sc)
		tokenCount = countMsgTokens(*messages)
	}

	if tokenCount > softLimit && r.pendingJob == nil {
		r.triggerAsyncCompression(ctx, *messages, sc)
	}

	if len(toolCalls) > 0 {
		*messages = append(*messages, msg.Raw{M: llm.NewToolCallMessage(assistantContent, toolCalls)})
	} else if assistantContent != "" {
		*messages = append(*messages, msg.Text("assistant", assistantContent))
	}

	for _, rs := range results {
		m := msg.FromLLM(msg.LLMToolResult{
			Tool: rs.Name, ToolCallID: rs.ToolCallID, Content: rs.Result,
		})
		*messages = append(*messages, m)
	}
	if r.deps.FileDedupEnabled {
		if n := msg.DedupFiles(*messages); n > 0 {
			telemetry.Event(ctx, "context.file_dedup",
				telemetry.AnyToAttr("file.path", sc.Path()),
				telemetry.AnyToAttr("stubbed", n))
		}
	}

	finalCount := countMsgTokens(*messages)
	if finalCount > warnLimit && r.deps.FileEvictEnabled {
		evictReclaimable(*messages, warnLimit)
		finalCount = countMsgTokens(*messages)
	}
	if finalCount > warnLimit {
		r.cancelPendingCompression()
		*messages, _ = r.runCompression(ctx, *messages, sc)
	}

	return countMsgTokens(*messages) < warnLimit
}

// extractFacts turns a turn's tool calls into board bulletins — the auto
// publish layer described in docs/unit_review.md. Core harvests generic file reads;
// domain tools contribute their own facts through ToolFactHook.
func extractFacts(sc session.Scope, turn int, calls []llm.ToolCall) []board.Bulletin {
	var out []board.Bulletin
	for _, c := range calls {
		var args map[string]any
		_ = json.Unmarshal([]byte(c.Function.Arguments), &args)
		if c.Function.Name == tool.FileRead.Name() {
			requests, err := tool.ParseFileReadRequests(args)
			if err != nil {
				continue
			}
			paths := make([]string, 0, len(requests))
			for _, request := range requests {
				if request.FilePath != "" {
					paths = append(paths, request.FilePath)
				}
			}
			if len(paths) == 0 {
				continue
			}
			out = append(out, board.Bulletin{From: sc.ID, Turn: turn, Level: board.LevelConfirmed,
				Paths: paths, Text: fmt.Sprintf("read %d file range(s): %s", len(requests), strings.Join(paths, ", "))})
		}
	}
	return out
}

// handlePostBulletin executes the model-initiated publish side of the Review
// Team (see docs/unit_review.md): an observation-level suspicion routed to peers
// via the caller-supplied keys. Loop-owned rather than a Registry provider
// because publishing needs the scope identity, turn, and per-loop budget that
// only the running loop holds. Returns the tool result for the model and
// whether a bulletin was actually published.
func (r *Runner) handlePostBulletin(scopeID string, turn int, call llm.ToolCall, budget *int) (string, bool) {
	var args struct {
		Text    string   `json:"text"`
		Paths   []string `json:"paths"`
		Symbols []string `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return fmt.Sprintf("Error parsing post_bulletin arguments: %v", err), false
	}
	args.Text = strings.TrimSpace(args.Text)
	if args.Text == "" {
		return "Error: post_bulletin requires a non-empty text.", false
	}
	if len(args.Paths) == 0 && len(args.Symbols) == 0 {
		return "Error: post_bulletin requires at least one routing key (paths or symbols) — without one the note cannot reach the peers reviewing that code.", false
	}
	if *budget <= 0 {
		return "Bulletin budget for this unit is exhausted — note NOT posted. Continue the current task and use its result tool for final output.", false
	}
	*budget--
	// Keep the card a card: the board digest is byte-capped, and one verbose
	// note must not crowd out peers' (docs/unit_review.md: summary + pointer).
	if runes := []rune(args.Text); len(runes) > maxBulletinTextRunes {
		args.Text = string(runes[:maxBulletinTextRunes]) + "…"
	}
	r.deps.Board.Publish(board.Bulletin{From: scopeID, Turn: turn, Level: board.LevelObservation,
		Paths: args.Paths, Symbols: args.Symbols, Text: args.Text})
	return "Posted to the team board as an unverified observation; peers reviewing the referenced code will see it. They cannot reply — continue your own review now.", true
}

// lookupTool returns the provider for a given tool from the registry, or
// nil when not registered.
func lookupTool(reg *tool.Registry, t tool.Tool) tool.Provider {
	p, ok := reg.Get(t.Name())
	if !ok {
		return nil
	}
	return p
}
