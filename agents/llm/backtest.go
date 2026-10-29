package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrBudgetExhausted is the error of a call of a backtest past its budget, see
// Recorder.
var ErrBudgetExhausted = errors.New("the budget of the backtest is exhausted")

// recordedReason bounds the justification of a call kept, in runes.
const recordedReason = 600

// Recorder is the provider of a backtest: it asks provider, records each call
// (the simulated time, a hash of the context, the decision, the error, the
// tokens and the estimated cost), and stops the run once MaxCalls calls were
// made or their cost reached MaxCost dollars (estimated with PriceInput and
// PriceOutput, dollars per million tokens; 0 for unknown): the call past it
// refused (ErrBudgetExhausted) and stop called. Its usage comes from the
// provider (see WithUsageHook and Usage).
type Recorder struct {
	provider Provider
	now      func() time.Time
	stop     func()

	MaxCalls                int
	MaxCost                 float64
	PriceInput, PriceOutput float64

	mu        sync.Mutex
	calls     []RecordedCall
	usage     Usage
	exhausted bool
	pending   *Usage // the usage of the call in flight, see Usage
}

// RecordedCall is a call of the model in a backtest, see Recorder.
type RecordedCall struct {
	At         int64   `json:"at"`
	Pair       string  `json:"pair"`
	InputHash  string  `json:"input_sha256"`
	Action     string  `json:"action,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Error      string  `json:"error,omitempty"`
	Input      int     `json:"input_tokens"`
	Output     int     `json:"output_tokens"`
	Cost       float64 `json:"cost"`
}

// NewRecorder returns the provider of a backtest asking provider, its calls
// timed by now, stop called once its budget is exhausted, see Recorder.
func NewRecorder(provider Provider, now func() time.Time, stop func()) *Recorder {
	return &Recorder{provider: provider, now: now, stop: stop}
}

// Usage records the usage of the answer of the call in flight: the hook of
// the provider, see WithUsageHook.
func (r *Recorder) Usage(usage Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != nil {
		r.pending.add(usage)
	}
}

// cost returns the estimated cost of usage, in dollars.
func (r *Recorder) cost(usage Usage) float64 {
	return (float64(usage.Input)*r.PriceInput + float64(usage.Output)*r.PriceOutput) / 1e6
}

// begin starts a call: false past the budget, then stop called once.
func (r *Recorder) begin() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	over := (r.MaxCalls > 0 && len(r.calls) >= r.MaxCalls) || (r.MaxCost > 0 && r.cost(r.usage) >= r.MaxCost)
	if over {
		if !r.exhausted && r.stop != nil {
			r.exhausted = true
			go r.stop()
		}
		r.exhausted = true
		return false
	}
	r.pending = &Usage{}
	return true
}

// end records the call on input that answered decision or err.
func (r *Recorder) end(input Input, decision Decision, err error) {
	data, _ := json.Marshal(input)
	hash := sha256.Sum256(data)
	call := RecordedCall{At: r.now().Unix(), Pair: input.Symbol, InputHash: hex.EncodeToString(hash[:])}
	if err != nil {
		call.Error = err.Error()
	}
	if decision.Action != "" {
		call.Action, call.Confidence = string(decision.Action), float64(decision.Confidence)
		reason := []rune(decision.Reason)
		if len(reason) > recordedReason {
			reason = append(reason[:recordedReason], '…')
		}
		call.Reason = string(reason)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending != nil {
		call.Input, call.Output = r.pending.Input, r.pending.Output
		call.Cost = r.cost(*r.pending)
		r.usage.add(*r.pending)
		r.pending = nil
	}
	r.calls = append(r.calls, call)
}

func (r *Recorder) Analyze(ctx context.Context, input Input) (Decision, error) {
	if !r.begin() {
		return Decision{}, ErrBudgetExhausted
	}
	decision, err := r.provider.Analyze(ctx, input)
	r.end(input, decision, err)
	return decision, err
}

func (r *Recorder) AnalyzeWith(ctx context.Context, system string, input Input) (Decision, error) {
	with, ok := r.provider.(SystemPromptProvider)
	if !ok {
		return r.Analyze(ctx, input)
	}
	if !r.begin() {
		return Decision{}, ErrBudgetExhausted
	}
	decision, err := with.AnalyzeWith(ctx, system, input)
	r.end(input, decision, err)
	return decision, err
}

func (r *Recorder) String() string { return fmt.Sprint(r.provider) }

// RecorderReport is what a Recorder adds to the report of a backtest: its
// calls, their tokens and their estimated cost (nil when unknown: no price),
// the ceilings, whether they were reached, and the model.
type RecorderReport struct {
	Calls        int            `json:"calls"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	Cost         *float64       `json:"cost,omitempty"`
	MaxCalls     int            `json:"max_calls"`
	MaxCost      float64        `json:"max_cost"`
	Exhausted    bool           `json:"exhausted"`
	Model        string         `json:"model"`
	Context      []string       `json:"context"`
	Recorded     []RecordedCall `json:"recorded"`
}

// Report returns what the recorder adds to the report, see RecorderReport.
func (r *Recorder) Report(model string, context []string) RecorderReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	report := RecorderReport{Calls: len(r.calls), InputTokens: r.usage.Input, OutputTokens: r.usage.Output,
		MaxCalls: r.MaxCalls, MaxCost: r.MaxCost, Exhausted: r.exhausted, Model: model, Context: context,
		Recorded: append([]RecordedCall(nil), r.calls...)}
	if r.PriceInput > 0 || r.PriceOutput > 0 {
		cost := r.cost(r.usage)
		report.Cost = &cost
	}
	return report
}
