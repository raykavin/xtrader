// Package agentapi is the internal API of the controller for the agents it
// launches: the settings each agent runs with, what it applies while it runs
// (the channels of its control panel, the prompt of an LLM agent), and the
// records it keeps through the controller, which alone reaches the database:
// its orders, and the decisions and journal of an LLM agent.
//
// The controller serves it on the loopback (agents.api of config.yml), under
// Prefix, and gives each start of an agent its URL and a token of its own in
// the environment (see EnvURL): every request carries the token, and only
// reaches the settings and the records of that agent.
package agentapi

import (
	"encoding/json"
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
// are its other flags by name, only the ones set. The secrets are the ones of
// the agent, never of another.
type Config struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`
	Mode            string            `json:"mode"`
	APIAddress      string            `json:"api_address"`
	TradePanel      bool              `json:"trade_panel"`
	TelegramControl bool              `json:"telegram_control"`
	Params          map[string]string `json:"params"`
	Account         *Account          `json:"account,omitempty"`
	LLM             *LLM              `json:"llm,omitempty"`
	Prompt          *Prompt           `json:"prompt,omitempty"` // of an LLM agent, nil for its default one
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
	CalendarURL     string            `json:"calendar_url,omitempty"`
	CalendarToken   string            `json:"calendar_token,omitempty"`
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
// /runtime: the channels of its control panel, and the prompt of an LLM agent,
// nil for its default one. Version changes with any of them: GET
// /runtime?wait=25s with If-None-Match: <version> waits for a change.
type Runtime struct {
	Version         int64      `json:"version"`
	TradePanel      bool       `json:"trade_panel"`
	TelegramControl bool       `json:"telegram_control"`
	Prompt          *PromptRef `json:"prompt,omitempty"`
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
)

// Record is a record an agent keeps through the controller, of its outbox.
// Seq orders the records of the outbox while it holds them, nothing more: it
// starts again from 1 once the outbox was sent whole and the agent starts
// again, and the controller keeps none of it, answering only the last one
// written (see OutboxResponse). Key names what the record writes for the
// logs, eg: the ID of an order. The controller writes a record by the keys of
// its payload, never by Seq or Key: the ID of an order, of a decision or of
// an entry of the journal, the prompt status of the agent. So a record sent
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
