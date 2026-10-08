package remote

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/ui"
)

func TestDecisions(t *testing.T) {
	f := newFakeController(t)
	f.set(func(f *fakeController) {
		f.decisions = []ui.Decision{ // newest first
			{ID: "c", Pair: "XAUUSDm", DecidedAt: 30},
			{ID: "b", Pair: "BTCUSDm", DecidedAt: 20},
			{ID: "a", Pair: "XAUUSDm", DecidedAt: 10, OrderIDs: []int64{1},
				Attachments: []ui.DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 19}}},
		}
		f.links = map[int64]string{1: "a"}
	})
	outbox := openOutbox(t, f)
	// a decision of a run before, not sent
	require.NoError(t, outbox.Add(agentapi.KindDecision, "d", agentapi.DecisionRecord{
		Decision: ui.Decision{ID: "d", Pair: "XAUUSDm", DecidedAt: 40, OrderIDs: []int64{5},
			Attachments: []ui.DecisionAttachment{{Index: 0, MediaType: "image/png", Size: 7}}},
		Attachments: []agentapi.Attachment{{MediaType: "image/png", Data: []byte("pending")}}}))
	decisions, err := NewDecisions(context.Background(), f.client(), outbox)
	require.NoError(t, err)

	list, err := decisions.Decisions("XAUUSDm", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"d", "c", "a"}, idsOf(list))
	assert.Equal(t, "a", decisions.OrderDecision(1))
	assert.Equal(t, "d", decisions.OrderDecision(5))
	_, data, err := decisions.Attachment("d", 0)
	require.NoError(t, err)
	assert.Equal(t, []byte("pending"), data, "held")
	mediaType, data, err := decisions.Attachment("a", 0)
	require.NoError(t, err)
	assert.Equal(t, "image/png", mediaType)
	assert.Equal(t, []byte("from the controller"), data, "read from the controller")

	// saved: held, and in the outbox
	kept := ui.Decision{ID: "e", Pair: "BTCUSDm", DecidedAt: 50, OrderIDs: []int64{6},
		Attachments: []ui.DecisionAttachment{{Index: 0, MediaType: "image/jpeg", Size: 4}}}
	require.NoError(t, decisions.Save(kept, []llm.Attachment{{MediaType: "image/jpeg", Data: []byte("JPEG")}}))
	found, ok, err := decisions.Decision("e")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, kept, found)
	assert.Equal(t, "e", decisions.OrderDecision(6))
	pending, err := outbox.Pending(agentapi.KindDecision)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	var record agentapi.DecisionRecord
	require.NoError(t, json.Unmarshal(pending[1].Payload, &record))
	assert.Equal(t, kept, record.Decision)
	assert.Equal(t, []agentapi.Attachment{{MediaType: "image/jpeg", Data: []byte("JPEG")}}, record.Attachments)

	// too few held: the older ones of the controller
	f.set(func(f *fakeController) {
		f.decisions = append(f.decisions, ui.Decision{ID: "0", Pair: "BTCUSDm", DecidedAt: 5})
	})
	list, err = decisions.Decisions("BTCUSDm", 3)
	require.NoError(t, err)
	assert.Equal(t, []string{"e", "b", "0"}, idsOf(list))
	found, ok, err = decisions.Decision("0")
	require.NoError(t, err)
	assert.True(t, ok, "read from the controller")
	assert.Equal(t, "0", found.ID)
	_, ok, err = decisions.Decision("none")
	require.NoError(t, err)
	assert.False(t, ok)

	// the controller out of reach: the ones held
	f.set(func(f *fakeController) { f.down = true })
	list, err = decisions.Decisions("BTCUSDm", 3)
	require.NoError(t, err)
	assert.Equal(t, []string{"e", "b"}, idsOf(list))
}

func idsOf(decisions []ui.Decision) []string {
	ids := make([]string, 0, len(decisions))
	for _, d := range decisions {
		ids = append(ids, d.ID)
	}
	return ids
}

func TestJournal(t *testing.T) {
	f := newFakeController(t)
	candle := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	f.set(func(f *fakeController) {
		f.memory = []agentapi.Remembered{
			{Pair: "XAUUSDm", Candle: candle, Action: "WAIT", Confidence: 0.4, Reason: "one"},
			{Pair: "XAUUSDm", Candle: candle.Add(time.Minute), At: candle.Add(90 * time.Second), Action: "HOLD",
				Confidence: 0.6, Reason: "two"},
			{Pair: "BTCUSDm", Candle: candle, Action: "WAIT", Confidence: 0.3, Reason: "btc"},
		}
	})
	outbox := openOutbox(t, f)
	journal := NewJournal(f.client(), outbox)

	prompt := &ui.DecisionPrompt{ID: 2, Name: "trend", Version: 3}
	input := llm.Input{Symbol: "XAUUSDm", Time: candle.Add(2 * time.Minute)}
	require.NoError(t, journal.Append(llm.JournalEntry{At: candle.Add(3 * time.Minute), ID: "x", Trigger: "candle",
		Model: "openai gpt-5", Prompt: prompt, LatencyMS: 900, Input: input,
		Decision: &llm.Decision{Action: llm.ActionExit, Confidence: 0.8, Reason: "three"}}))
	require.NoError(t, journal.Append(llm.JournalEntry{At: candle.Add(4 * time.Minute), ID: "y", Input: input,
		Error: "openai: HTTP 500"}))

	pending, err := outbox.Pending(agentapi.KindJournal)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	var record agentapi.JournalRecord
	require.NoError(t, json.Unmarshal(pending[0].Payload, &record))
	assert.Equal(t, "x", record.ID)
	assert.Equal(t, "XAUUSDm", record.Pair)
	assert.Equal(t, prompt, record.Prompt)
	assert.Contains(t, string(record.Input), `"symbol":"XAUUSDm"`)
	assert.Contains(t, string(record.Decision), `"action":"EXIT"`)
	var failed agentapi.JournalRecord
	require.NoError(t, json.Unmarshal(pending[1].Payload, &failed))
	assert.Empty(t, failed.Decision)
	assert.Equal(t, "openai: HTTP 500", failed.Error)

	// the memory: the controller's, then the ones not sent, perPair of each
	recent, err := journal.Recent(2)
	require.NoError(t, err)
	var reasons []string
	for _, r := range recent {
		reasons = append(reasons, r.Pair+":"+r.Reason)
	}
	assert.Equal(t, []string{"XAUUSDm:two", "BTCUSDm:btc", "XAUUSDm:three"}, reasons)
	assert.Equal(t, llm.ActionExit, recent[2].Action)
	assert.Equal(t, input.Time, recent[2].Candle.UTC())
	assert.Equal(t, candle.Add(90*time.Second), recent[0].At.UTC(), "when it was taken, of the controller")
	assert.Equal(t, candle.Add(3*time.Minute), recent[2].At.UTC(), "and of the outbox")
}

func TestPrompts(t *testing.T) {
	f := newFakeController(t)
	outbox := openOutbox(t, f)
	trend := &agentapi.Prompt{PromptRef: agentapi.PromptRef{ID: 2, Name: "trend", Version: 1}, Content: "# t\n\nTrend."}
	p := &Prompts{outbox: outbox, current: promptOf(trend)}
	ref := ui.DecisionPrompt{ID: 2, Name: "trend", Version: 1}

	_, changed := p.Current(ref)
	assert.False(t, changed, "known")
	got, changed := p.Current(ui.DecisionPrompt{})
	assert.True(t, changed)
	assert.Equal(t, llm.Prompt{Ref: ref, Content: "# t\n\nTrend."}, got)

	// a new version, read from the controller
	f.set(func(f *fakeController) {
		f.prompt = &agentapi.Prompt{PromptRef: agentapi.PromptRef{ID: 2, Name: "trend", Version: 2},
			Content: "# t\n\nTrend 2."}
	})
	require.NoError(t, p.update(context.Background(), f.client(), &agentapi.PromptRef{ID: 2, Name: "trend",
		Version: 2}))
	got, changed = p.Current(ref)
	assert.True(t, changed)
	assert.Equal(t, 2, got.Ref.Version)
	assert.Equal(t, "# t\n\nTrend 2.", got.Content)
	requests := len(f.requests)
	require.NoError(t, p.update(context.Background(), f.client(), &agentapi.PromptRef{ID: 2, Name: "trend",
		Version: 2}))
	assert.Len(t, f.requests, requests, "held: not read again")

	// the default one, without a request
	require.NoError(t, p.update(context.Background(), f.client(), nil))
	assert.Equal(t, llm.DefaultPrompt(), p.Prompt())
	assert.Len(t, f.requests, requests)

	// the controller out of reach: the prompt held kept
	f.set(func(f *fakeController) { f.down = true })
	require.Error(t, p.update(context.Background(), f.client(), &agentapi.PromptRef{ID: 3, Name: "x", Version: 1}))
	assert.Equal(t, llm.DefaultPrompt(), p.Prompt())

	p.ReportPrompt(ref, nil)
	p.ReportPrompt(ui.DecisionPrompt{ID: 3, Name: "x", Version: 1}, errors.New("unknown kind \"foo\""))
	pending, err := outbox.Pending(agentapi.KindPromptStatus)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	var applied, status agentapi.PromptStatus
	require.NoError(t, json.Unmarshal(pending[0].Payload, &applied))
	assert.True(t, applied.Applied)
	assert.Equal(t, int64(2), applied.PromptID)
	require.NoError(t, json.Unmarshal(pending[1].Payload, &status))
	assert.False(t, status.Applied)
	assert.Equal(t, "x", status.Name)
	assert.Equal(t, `unknown kind "foo"`, status.Error)
	assert.WithinDuration(t, time.Now(), status.At, time.Minute)
}
