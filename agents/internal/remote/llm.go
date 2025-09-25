package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// The decisions of an LLM agent held in memory: the last ones read of each
// pair at the start, and at most the last ones of all.
const (
	decisionsLoaded = 100
	decisionsHeld   = 2000
)

// Decisions is the store of the decisions of an LLM agent launched by the
// controller (llm.DecisionStore): each saved through the outbox, the last ones
// held in memory with the decisions that placed the orders, which the chart
// reads holding its lock, the older ones read from the controller.
type Decisions struct {
	memory *llm.MemoryDecisions
	client *Client
	outbox *Outbox
}

var _ llm.DecisionStore = (*Decisions)(nil)

// NewDecisions returns the decisions of the agent: the last ones of the
// controller, and the ones of the outbox not sent yet, newer.
func NewDecisions(ctx context.Context, client *Client, outbox *Outbox) (*Decisions, error) {
	recent, err := client.DecisionsPerPair(ctx, decisionsLoaded)
	if err != nil {
		return nil, fmt.Errorf("remote: the decisions: %w", err)
	}
	placed, err := client.DecisionLinks(ctx)
	if err != nil {
		return nil, fmt.Errorf("remote: the orders of the decisions: %w", err)
	}
	memory := llm.NewMemoryDecisions(decisionsHeld)
	memory.Load(recent, placed)

	pending, err := outbox.Pending(agentapi.KindDecision)
	if err != nil {
		return nil, err
	}
	for _, record := range pending {
		var kept agentapi.DecisionRecord
		if err := json.Unmarshal(record.Payload, &kept); err != nil {
			return nil, fmt.Errorf("remote: the decision %s of the outbox: %w", record.Key, err)
		}
		if err := memory.Save(kept.Decision, attachmentsOf(kept.Attachments)); err != nil {
			return nil, err
		}
	}
	return &Decisions{memory: memory, client: client, outbox: outbox}, nil
}

// attachmentsOf returns the attachments of a record in the form of the Trader.
func attachmentsOf(list []agentapi.Attachment) []llm.Attachment {
	attachments := make([]llm.Attachment, 0, len(list))
	for _, a := range list {
		attachments = append(attachments, llm.Attachment{MediaType: a.MediaType, Data: a.Data})
	}
	return attachments
}

// Save implements llm.DecisionStore.
func (s *Decisions) Save(d ui.Decision, attachments []llm.Attachment) error {
	if err := s.memory.Save(d, attachments); err != nil {
		return err
	}
	record := agentapi.DecisionRecord{Decision: d}
	for _, a := range attachments {
		record.Attachments = append(record.Attachments, agentapi.Attachment{MediaType: a.MediaType, Data: a.Data})
	}
	return s.outbox.Add(agentapi.KindDecision, d.ID, record)
}

// Decision implements llm.DecisionStore.
func (s *Decisions) Decision(id string) (ui.Decision, bool, error) {
	if d, ok, _ := s.memory.Decision(id); ok {
		return d, true, nil
	}
	return s.client.Decision(context.Background(), id)
}

// Decisions implements llm.DecisionStore: the ones held, with the older ones
// of the controller when they are too few, or else alone while the controller
// is out of reach.
func (s *Decisions) Decisions(pair string, limit int) ([]ui.Decision, error) {
	held, _ := s.memory.Decisions(pair, limit)
	if len(held) >= limit {
		return held, nil
	}
	stored, err := s.client.Decisions(context.Background(), pair, limit)
	if err != nil {
		log.Warnf("remote: the decisions of the controller: %v: the ones held alone", err)
		return held, nil
	}
	seen := make(map[string]bool, len(held))
	for _, d := range held {
		seen[d.ID] = true
	}
	for _, d := range stored {
		if !seen[d.ID] {
			held = append(held, d)
		}
	}
	sort.SliceStable(held, func(i, j int) bool {
		if a, b := held[i].DecidedAt, held[j].DecidedAt; a != b {
			return a > b
		}
		return held[i].ID > held[j].ID
	})
	return held[:min(len(held), limit)], nil
}

// OrderDecision implements llm.DecisionStore, from memory.
func (s *Decisions) OrderDecision(orderID int64) string {
	return s.memory.OrderDecision(orderID)
}

// Attachment implements llm.DecisionStore.
func (s *Decisions) Attachment(id string, index int) (string, []byte, error) {
	if mediaType, data, err := s.memory.Attachment(id, index); err == nil {
		return mediaType, data, nil
	}
	return s.client.Attachment(context.Background(), id, index)
}

// Journal is the journal of an LLM agent launched by the controller
// (llm.Journal): each call to the model written through the outbox, the memory
// of its prompt read back from the controller.
type Journal struct {
	client *Client
	outbox *Outbox
}

var _ llm.Journal = (*Journal)(nil)

// NewJournal returns the journal of the agent.
func NewJournal(client *Client, outbox *Outbox) *Journal {
	return &Journal{client: client, outbox: outbox}
}

// Append implements llm.Journal.
func (j *Journal) Append(entry llm.JournalEntry) error {
	input, err := json.Marshal(entry.Input)
	if err != nil {
		return fmt.Errorf("remote: the journal: %w", err)
	}
	record := agentapi.JournalRecord{ID: entry.ID, At: entry.At, Pair: entry.Input.Symbol, Trigger: entry.Trigger,
		RequestedBy: entry.Requested, Model: entry.Model, Prompt: entry.Prompt, LatencyMS: entry.LatencyMS,
		Input: input, Error: entry.Error}
	if entry.Decision != nil {
		if record.Decision, err = json.Marshal(entry.Decision); err != nil {
			return fmt.Errorf("remote: the journal: %w", err)
		}
	}
	return j.outbox.Add(agentapi.KindJournal, entry.ID, record)
}

// Recent implements llm.Journal: the decisions of the controller, then the
// ones of the outbox not sent yet.
func (j *Journal) Recent(perPair int) ([]llm.Remembered, error) {
	stored, err := j.client.JournalMemory(context.Background(), perPair)
	if err != nil {
		return nil, err
	}
	var list []llm.Remembered
	for _, r := range stored {
		list = append(list, llm.Remembered{Pair: r.Pair, Candle: r.Candle, Action: llm.Action(r.Action),
			Confidence: r.Confidence, Reason: r.Reason})
	}
	pending, err := j.outbox.Pending(agentapi.KindJournal)
	if err != nil {
		return nil, err
	}
	for _, record := range pending {
		var entry struct {
			Pair  string `json:"pair"`
			Error string `json:"error"`
			Input struct {
				Time time.Time `json:"time"`
			} `json:"input"`
			Decision *struct {
				Action     llm.Action `json:"action"`
				Confidence float64    `json:"confidence"`
				Reason     string     `json:"reason"`
			} `json:"decision"`
		}
		if json.Unmarshal(record.Payload, &entry) != nil || entry.Decision == nil || entry.Error != "" {
			continue
		}
		list = append(list, llm.Remembered{Pair: entry.Pair, Candle: entry.Input.Time,
			Action: entry.Decision.Action, Confidence: entry.Decision.Confidence, Reason: entry.Decision.Reason})
	}

	// the last perPair of each pair, oldest first
	counts := make(map[string]int)
	kept := make([]llm.Remembered, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		if counts[list[i].Pair] < perPair {
			counts[list[i].Pair]++
			kept = append(kept, list[i])
		}
	}
	for i, k := 0, len(kept)-1; i < k; i, k = i+1, k-1 {
		kept[i], kept[k] = kept[k], kept[i]
	}
	return kept, nil
}

// Prompts is the source of the prompts of an LLM agent launched by the
// controller (llm.PromptSource): the prompt selected for it, kept as the
// runtime of the agent changes (see Agent.Run), so the Trader reads it without
// a request. What comes of each prompt is told to the controller through the
// outbox (llm.PromptReporter).
type Prompts struct {
	outbox *Outbox

	mu      sync.Mutex
	current llm.Prompt
}

var (
	_ llm.PromptSource   = (*Prompts)(nil)
	_ llm.PromptReporter = (*Prompts)(nil)
)

// promptOf returns the prompt of the Trader of a prompt of the controller, its
// default one for none.
func promptOf(p *agentapi.Prompt) llm.Prompt {
	if p == nil {
		return llm.DefaultPrompt()
	}
	return llm.Prompt{Ref: refOf(&p.PromptRef), Content: p.Content}
}

// refOf returns the name of the prompt of ref in the records of the Trader,
// the one of its default prompt for none.
func refOf(ref *agentapi.PromptRef) ui.DecisionPrompt {
	if ref == nil {
		return llm.DefaultPrompt().Ref
	}
	return ui.DecisionPrompt{ID: ref.ID, Name: ref.Name, Version: ref.Version}
}

// Current implements llm.PromptSource.
func (p *Prompts) Current(known ui.DecisionPrompt) (llm.Prompt, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current.Ref == known {
		return llm.Prompt{}, false
	}
	return p.current, true
}

// Prompt returns the prompt selected for the agent.
func (p *Prompts) Prompt() llm.Prompt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

// ReportPrompt implements llm.PromptReporter.
func (p *Prompts) ReportPrompt(ref ui.DecisionPrompt, err error) {
	status := agentapi.PromptStatus{PromptID: ref.ID, Name: ref.Name, Version: ref.Version, Applied: err == nil,
		At: time.Now().UTC()}
	if err != nil {
		status.Error = err.Error()
	}
	if err := p.outbox.Add(agentapi.KindPromptStatus, "prompt", status); err != nil {
		log.Warnf("remote: the status of the prompt: %v", err)
	}
}

// update keeps the prompt of ref, read from the controller unless held.
func (p *Prompts) update(ctx context.Context, client *Client, ref *agentapi.PromptRef) error {
	want := refOf(ref)
	p.mu.Lock()
	held := p.current.Ref
	p.mu.Unlock()
	if held == want {
		return nil
	}
	next := llm.DefaultPrompt()
	if ref != nil {
		prompt, found, err := client.Prompt(ctx)
		if err != nil {
			return fmt.Errorf("remote: the prompt %s: %w", want.Name, err)
		}
		if found {
			next = promptOf(&prompt) // maybe newer than ref: the next runtime tells it
		}
	}
	p.mu.Lock()
	p.current = next
	p.mu.Unlock()
	return nil
}
