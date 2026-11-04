// Package agentapi is the internal API of the controller for the agents it
// launches: the settings each agent runs with, what it applies while it runs
// (the channels of its control panel, the prompt of an LLM agent), and the
// records it keeps through the controller, which alone reaches the database:
// its orders and the positions its order controller discarded, and the
// decisions and journal of an LLM agent.
//
// The controller serves it on the loopback (agents.api of config.yml), under
// Prefix, and gives each start of an agent its URL and a token of its own in
// the environment (see EnvURL): every request carries the token, and only
// reaches the settings and the records of that agent.
package agentapi

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/raykavin/xtrader/ui"
)

const (
	// EnvURL is the variable of the URL of the internal API, eg:
	// http://127.0.0.1:8090.
	EnvURL = "XTRADER_CONTROLLER_URL"
	// EnvAgent is the variable of the name of the agent.
	EnvAgent = "XTRADER_AGENT"
	// EnvToken is the variable of the token of the start of the agent, sent
	// as Authorization: Bearer <token>.
	EnvToken = "XTRADER_AGENT_TOKEN"
	// EnvAPIKey is the variable of the key of the start of the agent the
	// controller sends to its API, which wants it (see ui.WithAPIKey): apart
	// from the token, which the API of the agent never gets.
	EnvAPIKey = "XTRADER_AGENT_API_KEY"

	// Prefix is the path of the routes of the internal API.
	Prefix = "/internal/v1"

	// MaxBody is the size of a request to the internal API at most: a batch
	// of records, see OutboxRequest.
	MaxBody = 64 << 20
)

// Config are the settings of an agent, the answer of GET /config: its Params
// are its other flags by name, only the ones set. Reanalysis allows the
// reanalysis of an LLM agent, apart from its channels. The entries of the
// agent are the ones of its runtime as it starts, see Entries. The secrets are
// the ones of the agent, never of another.
type Config struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`
	Mode            string            `json:"mode"`
	APIAddress      string            `json:"api_address"`
	TradePanel      bool              `json:"trade_panel"`
	TelegramControl bool              `json:"telegram_control"`
	Reanalysis      bool              `json:"reanalysis"`
	Entries                           // the entries blocked by the supervisor or the dashboard, if any
	Protection                        // the protection of the positions, see Protection
	Params          map[string]string `json:"params"`
	Account         *Account          `json:"account,omitempty"`
	LLM             *LLM              `json:"llm,omitempty"`
	Prompt          *Prompt           `json:"prompt,omitempty"` // of an LLM agent, nil for its default one
	News            *News             `json:"news,omitempty"`   // see News
}

// News is the news feature of all the agents, set on the dashboard (the tab
// Notícias): on or off; the MCP server of the economic calendar (URL) and its
// token, shared by the agents; and the window of the news block, in minutes
// (BlockBefore before a release of at least BlockImportance to BlockAfter
// after it, both 0 for none): inside it, an agent opens no position, its
// pending entries canceled and its positions closed as it starts. Every agent
// gets it at its start and in its runtime, applied while it runs: off, it
// reads no calendar and blocks nothing.
type News struct {
	Enabled         bool   `json:"enabled"`
	URL             string `json:"url,omitempty"`
	Token           string `json:"token,omitempty"`
	BlockBefore     int    `json:"block_before,omitempty"`
	BlockAfter      int    `json:"block_after,omitempty"`
	BlockImportance string `json:"block_importance,omitempty"`
}

// Account is the trading account of an agent: the bridge of MT5, the token of
// the bridge, and what tells its orders apart there, the magic number and the
// comment, their defaults when nil.
type Account struct {
	BridgeURL   string  `json:"bridge_url"`
	BridgeToken string  `json:"bridge_token,omitempty"`
	Magic       *int64  `json:"magic,omitempty"`
	Comment     *string `json:"comment,omitempty"`
	RealAllowed bool    `json:"real_allowed"`
}

// LLM is the language model of an LLM agent: its Params are its other flags,
// -llm-*, -calendar-* and -news-*, by name.
type LLM struct {
	Provider        string            `json:"provider"`
	Model           string            `json:"model"`
	APIKey          string            `json:"api_key,omitempty"`
	BaseURL         string            `json:"base_url,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Params          map[string]string `json:"params"`
}

// PromptRef names a version of a prompt of the database.
type PromptRef struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Prompt is a prompt of the database with its content, the answer of GET
// /prompt: the strategy of an LLM agent, its header lines included.
type Prompt struct {
	PromptRef
	Content string `json:"content"`
}

// Runtime is what an agent applies while it runs, the answer of GET
// /runtime: the channels of its control panel, its entries (see Entries), and
// of an LLM agent the switch of its reanalysis, its prompt, nil for its
// default one, and the news (see News). Version changes with any of them: GET
// /runtime?wait=25s with
// If-None-Match: <version> waits for a change.
type Runtime struct {
	Version         int64      `json:"version"`
	TradePanel      bool       `json:"trade_panel"`
	TelegramControl bool       `json:"telegram_control"`
	Reanalysis      bool       `json:"reanalysis"`
	Entries                    // the entries blocked by the supervisor or the dashboard, if any
	Protection                 // the protection of the positions, see Protection
	Prompt          *PromptRef `json:"prompt,omitempty"`
	News            *News      `json:"news,omitempty"` // see News
}

// Protection is the protection of the open positions of an agent (breakeven,
// trailing stop), applied live, without a restart: its settings, nil when
// off, and the arms of the XTrader AI on its open positions, see
// ui.ProtectionArm.
type Protection struct {
	ProtectionSettings *ui.ProtectionSettings `json:"protection_settings,omitempty"`
	ProtectionArms     []ui.ProtectionArm     `json:"protection_arms,omitempty"`
}

// Entries tells whether the entries of an agent are blocked, by the
// supervisor of the controller or from the dashboard, and why: the agent then
// opens no position, nor adds to one, and only reduces the ones it holds (see
// order.Controller.BlockEntries). Revision grows with every block and every
// allowance, so that an agent never applies an older one after a newer, eg: a
// runtime read before: 0 for an agent never blocked. EntriesPairs, while
// set, are the only assets the agent may open positions on (the window of the
// trading hours open allowing them only), PairsReason why: its other entries
// refused by its strategy (see PairGate), at the same revision. Exposure
// tells the exposure limit of the agents on: each entry claimed of the
// controller first (see ExposureClaim).
type Entries struct {
	EntriesBlocked  bool     `json:"entries_blocked,omitempty"`
	EntriesReason   string   `json:"entries_reason,omitempty"`
	EntriesRevision int64    `json:"entries_revision,omitempty"`
	EntriesPairs    []string `json:"entries_pairs,omitempty"`
	PairsReason     string   `json:"pairs_reason,omitempty"`
	Exposure        bool     `json:"exposure,omitempty"`
}

// ExposurePath is the path of the claims of an entry, POST, under Prefix.
const ExposurePath = "/exposure/claim"

// ExposureClaim is the payload of POST ExposurePath: an entry the agent is
// about to send, on Pair, on Side (BUY or SELL), claimed against the exposure
// limit of the agents (see ui.ExposureSettings).
type ExposureClaim struct {
	Pair string `json:"pair"`
	Side string `json:"side"`
}

// ExposureAnswer answers an ExposureClaim: Granted, the entry may go (its
// place held a while for the agent); else why not.
type ExposureAnswer struct {
	Granted bool   `json:"granted"`
	Reason  string `json:"reason,omitempty"`
}

// OrderIDsRequest is the payload of POST /order-ids: how many order IDs the
// agent leases.
type OrderIDsRequest struct {
	Count int64 `json:"count"`
}

// OrderIDs is a range of order IDs leased to an agent, First to Last, never
// leased again: the IDs of its next orders.
type OrderIDs struct {
	First int64 `json:"first"`
	Last  int64 `json:"last"`
}

// DecisionLink is an order and the decision that placed it, of GET
// /decision-links.
type DecisionLink struct {
	OrderID    int64  `json:"order_id"`
	DecisionID string `json:"decision_id"`
}

// Remembered is a decision of the journal of an LLM agent for the memory of
// its prompt, of GET /journal/memory: the candle it was taken on, when (the
// time of its entry; zero from a controller of a version before it), and its
// answer.
type Remembered struct {
	Pair       string    `json:"pair"`
	Candle     time.Time `json:"candle"`
	At         time.Time `json:"at"`
	Action     string    `json:"action"`
	Confidence float64   `json:"confidence"`
	Reason     string    `json:"reason"`
}

// The kinds of the records of the outbox of an agent, see Record.
const (
	KindOrder        = "order"         // a model.Order
	KindDecision     = "decision"      // a DecisionRecord
	KindJournal      = "journal"       // a JournalRecord
	KindPromptStatus = "prompt_status" // a PromptStatus
	KindDiscard      = "discard"       // a model.Discard
)

// Record is a record an agent keeps through the controller, of its outbox.
// Seq orders the records of the outbox while it holds them, nothing more: it
// starts again from 1 once the outbox was sent whole and the agent starts
// again, and the controller keeps none of it, answering only the last one
// written (see OutboxResponse). Key names what the record writes for the
// logs, eg: the ID of an order. The controller writes a record by the keys of
// its payload, never by Seq or Key: the ID of an order, of a decision or of
// an entry of the journal, the prompt status of the agent, the pair and the
// fill after which a position was discarded (kept once). So a record sent
// again writes the same, and a later record of the same order, decision or
// prompt status replaces it, an order never going back (see
// model.Order.Supersedes). At is when the agent added it, zero for a record
// of a version before it.
type Record struct {
	Seq     uint64          `json:"seq"`
	Kind    string          `json:"kind"`
	Key     string          `json:"key"`
	At      time.Time       `json:"at,omitzero"`
	Payload json.RawMessage `json:"payload"`
}

// OutboxRequest is the payload of POST /outbox: records in the order of their
// Seq, written in a single transaction. What the dashboard tells of the
// outbox of the agent: Pending are the records of the outbox not sent yet,
// the ones of the request included, and NextAt when the first of them after
// the ones of the request was added, zero for none; SetApart are the records
// the controller refused for good, kept apart in it, and Held the ones it
// failed again and again alone while it wrote the next ones of their kind,
// held apart in it, sent again now and then. A record held sent again is a
// request alone, counted among the ones not sent.
//
// POST /outbox/check takes one too and answers as POST /outbox would, nothing
// written: the agent checks with it that the controller writes the records
// after one it fails to write again and again, before holding that one apart.
type OutboxRequest struct {
	Records  []Record  `json:"records"`
	Pending  int       `json:"pending,omitempty"`
	NextAt   time.Time `json:"next_at,omitzero"`
	SetApart int       `json:"set_apart,omitempty"`
	Held     int       `json:"held,omitempty"`
}

// OutboxResponse answers POST /outbox: the records written, up to Acked.
type OutboxResponse struct {
	Acked uint64 `json:"acked"`
}

// OutboxError answers POST /outbox with 422: the record at Index is refused,
// for good, none written.
type OutboxError struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

// DecisionRecord is a decision of an LLM agent to keep, with its attachments.
type DecisionRecord struct {
	Decision    ui.Decision  `json:"decision"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is a file of a decision, eg: an image the model answered with.
type Attachment struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

// JournalRecord is a call of an LLM agent to its model, with its whole
// context: Input and Decision as the agent marshals them, its ID the one of
// the decision kept of it.
type JournalRecord struct {
	ID          string             `json:"id"`
	At          time.Time          `json:"at"`
	Pair        string             `json:"pair"`
	Trigger     string             `json:"trigger,omitempty"`
	RequestedBy string             `json:"requested_by,omitempty"`
	Model       string             `json:"model"`
	Prompt      *ui.DecisionPrompt `json:"prompt,omitempty"`
	LatencyMS   int64              `json:"latency_ms"`
	Input       json.RawMessage    `json:"input"`
	Decision    json.RawMessage    `json:"decision,omitempty"`
	Error       string             `json:"error,omitempty"`
}

// PromptStatus is the last prompt an LLM agent was asked to switch to, and how
// it went: applied, or refused with Error, the previous one kept.
type PromptStatus struct {
	PromptID int64     `json:"prompt_id,omitempty"`
	Name     string    `json:"name"`
	Version  int       `json:"version"`
	Applied  bool      `json:"applied"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

// Args returns the flags of the settings of the agent, the ones set only, as
// its command line gave them: its params, then its columns, which the
// controller keeps apart. Its secrets are never among them, see Config.
func (cfg Config) Args() []string {
	var args []string
	add := func(name, value string) {
		args = append(args, "-"+name+"="+value)
	}
	params := func(values map[string]string) {
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add(name, values[name])
		}
	}

	params(cfg.Params)
	if l := cfg.LLM; l != nil {
		params(l.Params)
	}
	add("mode", cfg.Mode)
	if cfg.APIAddress != "" {
		add("api", cfg.APIAddress)
	}
	if cfg.TradePanel {
		add("trade-panel", "true")
	}
	if cfg.TelegramControl {
		add("telegram-control", "true")
	}
	if cfg.Reanalysis && cfg.Kind == "llm" { // a flag of the LLM agent only
		add("reanalysis", "true")
	}
	if acc := cfg.Account; acc != nil {
		if acc.BridgeURL != "" {
			add("mt5-bridge", acc.BridgeURL)
		}
		if acc.Magic != nil {
			add("mt5-magic", strconv.FormatInt(*acc.Magic, 10))
		}
		if acc.Comment != nil {
			add("mt5-comment", *acc.Comment)
		}
		if acc.RealAllowed {
			add("mt5-live", "true")
		}
	}
	if l := cfg.LLM; l != nil {
		add("llm-provider", l.Provider)
		add("llm-model", l.Model)
		if l.BaseURL != "" {
			add("llm-base-url", l.BaseURL)
		}
		if l.ReasoningEffort != "" {
			add("llm-reasoning", l.ReasoningEffort)
		}
		if l.MaxTokens > 0 {
			add("llm-max-tokens", strconv.Itoa(l.MaxTokens))
		}
	}
	return args
}
