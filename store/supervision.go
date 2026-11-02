package store

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The bounds of the settings of the supervisor, the ones of its table.
const (
	SupervisorMinInterval     = 300   // seconds between two analyses, at least
	SupervisorMaxInterval     = 86400 // and at most
	SupervisorMinTimeout      = 10    // seconds an answer of the model takes, at most, at least
	SupervisorMaxTimeout      = 600   // and at most
	SupervisorMaxInstructions = 20000 // characters of the instructions
	SupervisorMaxActions      = 10    // actions alone in an analysis, at most
)

// Autonomy is what the supervisor of the agents does alone, in its periodic
// analyses: block the entries of an agent, stop it once it holds nothing,
// cancel its pending entries, MaxActionsPerRun actions an analysis at most;
// the rest it proposes to the users. Closing positions is never one of them.
type Autonomy struct {
	BlockEntries     bool `json:"block_entries"`
	StopWhenFlat     bool `json:"stop_when_flat"`
	CancelPending    bool `json:"cancel_pending"`
	MaxActionsPerRun int  `json:"max_actions_per_run"`
}

// Value writes the autonomy as a JSON object.
func (a Autonomy) Value() (driver.Value, error) {
	data, err := json.Marshal(a)
	return string(data), err
}

// Scan reads the autonomy of its JSON object.
func (a *Autonomy) Scan(src interface{}) error {
	var data []byte
	switch v := src.(type) {
	case nil:
		*a = Autonomy{}
		return nil
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("store: autonomy of %T", src)
	}
	return json.Unmarshal(data, a)
}

// SupervisorSettings are the supervisor of the agents, a single record: its
// switch, the interval of its analyses, its language model as the ones of
// the LLM agents (see LLMSettings), the timeout of an answer, its
// instructions and its autonomy.
type SupervisorSettings struct {
	ID              int16     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Enabled         bool      `json:"enabled"`
	IntervalSeconds int       `json:"interval_seconds"`
	Provider        *string   `json:"provider,omitempty"`
	Model           *string   `json:"model,omitempty"`
	APIKey          []byte    `gorm:"column:api_key" json:"-"` // encrypted
	BaseURL         *string   `gorm:"column:base_url" json:"base_url,omitempty"`
	ReasoningEffort *string   `json:"reasoning_effort,omitempty"`
	MaxTokens       *int      `json:"max_tokens,omitempty"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	Instructions    string    `json:"instructions"`
	Autonomy        Autonomy  `gorm:"type:jsonb" json:"autonomy"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (SupervisorSettings) TableName() string { return "supervisor_settings" }

// supervisorKey is the place of the key of the language model of the
// supervisor.
func supervisorKey() string {
	return placeOf("supervisor_settings", "api_key", 1)
}

// Supervisor returns the settings of the supervisor, and whether they exist:
// false before the seeds are applied, the supervisor off then.
func (s *Store) Supervisor(ctx context.Context) (SupervisorSettings, bool, error) {
	var settings SupervisorSettings
	err := s.conn(ctx).First(&settings, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SupervisorSettings{}, false, nil
	}
	if err != nil {
		return SupervisorSettings{}, false, err
	}
	return settings, true, nil
}

// SupervisorKey returns the key of the language model of the supervisor,
// decrypted: "" for none.
func (s *Store) SupervisorKey(ctx context.Context) (string, error) {
	settings, found, err := s.Supervisor(ctx)
	if err != nil || !found {
		return "", err
	}
	return s.open(supervisorKey(), settings.APIKey)
}

// SupervisorUpdate is a change of the settings of the supervisor, of the
// fields set: an empty BaseURL or ReasoningEffort and a MaxTokens of 0 remove
// them, the defaults of the model applying; APIKey is a Secret.
type SupervisorUpdate struct {
	Enabled         *bool
	IntervalSeconds *int
	Provider        *string
	Model           *string
	BaseURL         *string
	ReasoningEffort *string
	MaxTokens       *int
	TimeoutSeconds  *int
	Instructions    *string
	Autonomy        *Autonomy
	APIKey          Secret
}

// SetSupervisor changes the settings of the supervisor, the row locked from
// its read to its write: ErrNotFound before the seeds are applied, ErrInvalid
// for a value out of its bounds, or the supervisor enabled without a model and
// its key.
func (s *Store) SetSupervisor(ctx context.Context, update SupervisorUpdate) (SupervisorSettings, error) {
	var settings SupervisorSettings
	err := s.Transaction(ctx, func(ctx context.Context) error {
		err := s.conn(ctx).Clauses(lockForUpdate()).First(&settings, 1).Error
		if err != nil {
			return notSeeded(err, "a configuração do XTrader AI")
		}
		if err := update.apply(&settings); err != nil {
			return err
		}
		if update.APIKey != nil {
			if err := s.sealInto(&settings.APIKey, supervisorKey(), update.APIKey); err != nil {
				return err
			}
		}
		if err := settings.validate(); err != nil {
			return err
		}
		settings.UpdatedAt = time.Now()
		return s.conn(ctx).Select("*").Save(&settings).Error
	})
	if err != nil {
		return SupervisorSettings{}, err
	}
	return settings, nil
}

// apply applies the fields of u set to settings, trimmed.
func (u SupervisorUpdate) apply(settings *SupervisorSettings) error {
	optional := func(value *string) *string {
		if value == nil {
			return nil
		}
		if trimmed := strings.TrimSpace(*value); trimmed != "" {
			return &trimmed
		}
		return nil
	}
	if u.Enabled != nil {
		settings.Enabled = *u.Enabled
	}
	if u.IntervalSeconds != nil {
		settings.IntervalSeconds = *u.IntervalSeconds
	}
	if u.Provider != nil {
		settings.Provider = optional(u.Provider)
	}
	if u.Model != nil {
		settings.Model = optional(u.Model)
	}
	if u.BaseURL != nil {
		settings.BaseURL = optional(u.BaseURL)
	}
	if u.ReasoningEffort != nil {
		settings.ReasoningEffort = optional(u.ReasoningEffort)
	}
	if u.MaxTokens != nil {
		settings.MaxTokens = nil
		if *u.MaxTokens != 0 {
			tokens := *u.MaxTokens
			settings.MaxTokens = &tokens
		}
	}
	if u.TimeoutSeconds != nil {
		settings.TimeoutSeconds = *u.TimeoutSeconds
	}
	if u.Instructions != nil {
		settings.Instructions = strings.TrimSpace(*u.Instructions)
	}
	if u.Autonomy != nil {
		settings.Autonomy = *u.Autonomy
	}
	return nil
}

// validate checks the settings against the bounds of their table, with a
// message for the dashboard.
func (s SupervisorSettings) validate() error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	switch {
	case s.IntervalSeconds < SupervisorMinInterval || s.IntervalSeconds > SupervisorMaxInterval:
		return invalid("o intervalo das análises deve ficar entre %d e %d minutos", SupervisorMinInterval/60,
			SupervisorMaxInterval/60)
	case s.TimeoutSeconds < SupervisorMinTimeout || s.TimeoutSeconds > SupervisorMaxTimeout:
		return invalid("o tempo máximo de resposta deve ficar entre %d e %d segundos", SupervisorMinTimeout,
			SupervisorMaxTimeout)
	case s.Provider != nil && *s.Provider != "openai" && *s.Provider != "anthropic":
		return invalid("o provedor %q não existe, use openai ou anthropic", *s.Provider)
	case s.ReasoningEffort != nil && !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh"},
		*s.ReasoningEffort):
		return invalid("o esforço de raciocínio %q não existe, use none, minimal, low, medium, high ou xhigh",
			*s.ReasoningEffort)
	case s.MaxTokens != nil && *s.MaxTokens < 1:
		return invalid("o máximo de tokens deve ser maior que 0")
	case utf8.RuneCountInString(s.Instructions) > SupervisorMaxInstructions:
		return invalid("as instruções podem ter até %d caracteres", SupervisorMaxInstructions)
	case s.Autonomy.MaxActionsPerRun < 0 || s.Autonomy.MaxActionsPerRun > SupervisorMaxActions:
		return invalid("as ações sem confirmação por análise devem ficar entre 0 e %d", SupervisorMaxActions)
	case s.Enabled && (s.Provider == nil || s.Model == nil || len(s.APIKey) == 0):
		return invalid("informe o provedor, o modelo e a chave da API para ligar o XTrader AI")
	}
	return nil
}

// The origins and the statuses of the analyses of the supervisor.
const (
	RunSchedule = "schedule"
	RunManual   = "manual"

	RunRunning = "running"
	RunOK      = "ok"
	RunFailed  = "failed"
	RunSkipped = "skipped"
)

// SupervisorRun is an analysis of the supervisor: Snapshot the data sent to
// the model, Findings what it found, a JSON array, Usage the tokens of its
// answers.
type SupervisorRun struct {
	ID          int64
	Origin      string
	Status      string
	RequestedBy string
	Model       string
	StartedAt   time.Time
	FinishedAt  *time.Time
	Snapshot    json.RawMessage
	Summary     string
	Findings    json.RawMessage
	Error       string
	Usage       json.RawMessage
}

// runRow is a row of supervisor_runs.
type runRow struct {
	ID          int64 `gorm:"primaryKey"`
	Origin      string
	Status      string
	RequestedBy string
	Model       string
	StartedAt   time.Time
	FinishedAt  *time.Time
	Snapshot    jsonb
	Summary     string
	Findings    jsonb
	Error       string
	Usage       jsonb
}

func (runRow) TableName() string { return "supervisor_runs" }

func (r runRow) run() SupervisorRun {
	return SupervisorRun{ID: r.ID, Origin: r.Origin, Status: r.Status, RequestedBy: r.RequestedBy, Model: r.Model,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Snapshot: json.RawMessage(r.Snapshot), Summary: r.Summary,
		Findings: json.RawMessage(r.Findings), Error: r.Error, Usage: json.RawMessage(r.Usage)}
}

// StartSupervisorRun records an analysis starting, running; skipped ones are
// recorded ended, see FinishSupervisorRun.
func (s *Store) StartSupervisorRun(ctx context.Context, origin, status, requestedBy, model string) (SupervisorRun,
	error) {
	row := runRow{Origin: origin, Status: status, RequestedBy: requestedBy, Model: model, StartedAt: time.Now(),
		Findings: jsonb("[]")}
	if status != RunRunning {
		now := row.StartedAt
		row.FinishedAt = &now
	}
	if err := s.conn(ctx).Create(&row).Error; err != nil {
		return SupervisorRun{}, notFound(err, "uma análise do XTrader AI")
	}
	return row.run(), nil
}

// FinishSupervisorRun records how the analysis run ended: its status, summary,
// findings, snapshot, usage and error.
func (s *Store) FinishSupervisorRun(ctx context.Context, run SupervisorRun) error {
	findings := jsonb(run.Findings)
	if len(findings) == 0 || string(findings) == "null" {
		findings = jsonb("[]")
	}
	now := time.Now()
	return s.conn(ctx).Model(&runRow{}).Where("id = ?", run.ID).Updates(map[string]any{"status": run.Status,
		"finished_at": now, "summary": run.Summary, "findings": findings, "snapshot": jsonb(run.Snapshot),
		"usage": jsonb(run.Usage), "error": run.Error, "model": run.Model}).Error
}

// FailRunningSupervisorRuns ends the analyses left running, failed for why:
// the ones of a controller stopped in their middle.
func (s *Store) FailRunningSupervisorRuns(ctx context.Context, why string) (int64, error) {
	result := s.conn(ctx).Model(&runRow{}).Where("status = ?", RunRunning).
		Updates(map[string]any{"status": RunFailed, "finished_at": time.Now(), "error": why})
	return result.RowsAffected, result.Error
}

// SupervisorRuns returns the last analyses, the newest first, without their
// snapshot.
func (s *Store) SupervisorRuns(ctx context.Context, limit int) ([]SupervisorRun, error) {
	var rows []runRow
	err := s.conn(ctx).Omit("snapshot").Order("started_at DESC, id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	runs := make([]SupervisorRun, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, row.run())
	}
	return runs, nil
}

// SupervisorRun returns the analysis of id, its snapshot included.
func (s *Store) SupervisorRun(ctx context.Context, id int64) (SupervisorRun, error) {
	var row runRow
	if err := s.conn(ctx).First(&row, id).Error; err != nil {
		return SupervisorRun{}, notFound(err, fmt.Sprintf("a análise %d", id))
	}
	return row.run(), nil
}

// PreviousSupervisorRun returns the last analysis ended ok before the one of
// id, false for none.
func (s *Store) PreviousSupervisorRun(ctx context.Context, id int64) (SupervisorRun, bool, error) {
	var row runRow
	err := s.conn(ctx).Omit("snapshot").Where("id < ? AND status = ?", id, RunOK).Order("id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SupervisorRun{}, false, nil
	}
	if err != nil {
		return SupervisorRun{}, false, err
	}
	return row.run(), true, nil
}

// The sources, the actions, the modes, the categories and the statuses of the
// actions of the supervision.
const (
	SourceSupervisor = "supervisor"
	SourceUser       = "user"

	ActionBlockEntries   = "block_entries"
	ActionAllowEntries   = "allow_entries"
	ActionStopAgent      = "stop_agent"
	ActionCancelPending  = "cancel_pending"
	ActionClosePositions = "close_positions"

	ModeWhenFlat = "when_flat"
	ModeNow      = "now"

	CategoryOperational = "operational"
	CategoryRisk        = "risk"
	CategoryPerformance = "performance"

	ActionProposed  = "proposed"
	ActionExecuting = "executing"
	ActionSucceeded = "succeeded"
	ActionPartial   = "partial"
	ActionFailed    = "failed"
	ActionUnknown   = "unknown"
	ActionRejected  = "rejected"
	ActionExpired   = "expired"
)

// SupervisorAction is an action of the supervision on the agents: Scope,
// Options, Evidence and Result are JSON, the shapes of the controller.
type SupervisorAction struct {
	ID          int64           `json:"id"`
	Source      string          `json:"source"`
	RunID       *int64          `json:"run_id,omitempty"`
	UserID      *int64          `json:"user_id,omitempty"`
	Action      string          `json:"action"`
	Mode        string          `json:"mode,omitempty"`
	Scope       json.RawMessage `json:"scope"`
	Options     json.RawMessage `json:"options"`
	Reason      string          `json:"reason"`
	Category    string          `json:"category,omitempty"`
	Evidence    json.RawMessage `json:"evidence"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	RequestedBy string          `json:"requested_by"`
	ConfirmedBy string          `json:"confirmed_by"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
	DecidedAt   *time.Time      `json:"decided_at,omitempty"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
}

// actionRow is a row of supervisor_actions.
type actionRow struct {
	ID          int64 `gorm:"primaryKey"`
	Source      string
	RunID       *int64
	UserID      *int64
	Action      string
	Mode        *string
	Scope       jsonb
	Options     jsonb
	Reason      string
	Category    *string
	Evidence    jsonb
	Status      string
	Result      jsonb
	RequestedBy string
	ConfirmedBy string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	DecidedAt   *time.Time
	FinishedAt  *time.Time
}

func (actionRow) TableName() string { return "supervisor_actions" }

func (r actionRow) action() SupervisorAction {
	a := SupervisorAction{ID: r.ID, Source: r.Source, RunID: r.RunID, UserID: r.UserID, Action: r.Action,
		Scope: json.RawMessage(r.Scope), Options: json.RawMessage(r.Options), Reason: r.Reason,
		Evidence: json.RawMessage(r.Evidence), Status: r.Status, Result: json.RawMessage(r.Result),
		RequestedBy: r.RequestedBy, ConfirmedBy: r.ConfirmedBy, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		DecidedAt: r.DecidedAt, FinishedAt: r.FinishedAt}
	if r.Mode != nil {
		a.Mode = *r.Mode
	}
	if r.Category != nil {
		a.Category = *r.Category
	}
	return a
}

// orJSON returns raw, else empty, eg: {} or [].
func orJSON(raw json.RawMessage, empty string) jsonb {
	if len(raw) == 0 || string(raw) == "null" {
		return jsonb(empty)
	}
	return jsonb(raw)
}

// optionalText returns a pointer to value, nil when empty.
func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// CreateSupervisorAction records the action a, its ID and its time set.
func (s *Store) CreateSupervisorAction(ctx context.Context, a SupervisorAction) (SupervisorAction, error) {
	row := actionRow{Source: a.Source, RunID: a.RunID, UserID: a.UserID, Action: a.Action, Mode: optionalText(a.Mode),
		Scope: orJSON(a.Scope, "{}"), Options: orJSON(a.Options, "{}"), Reason: strings.TrimSpace(a.Reason),
		Category: optionalText(a.Category), Evidence: orJSON(a.Evidence, "[]"), Status: a.Status,
		Result: jsonb(a.Result), RequestedBy: a.RequestedBy, ConfirmedBy: a.ConfirmedBy, CreatedAt: time.Now(),
		ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt}
	if err := s.conn(ctx).Create(&row).Error; err != nil {
		return SupervisorAction{}, notFound(err, "uma ação do XTrader AI")
	}
	return row.action(), nil
}

// SupervisorAction returns the action of id.
func (s *Store) SupervisorAction(ctx context.Context, id int64) (SupervisorAction, error) {
	var row actionRow
	if err := s.conn(ctx).First(&row, id).Error; err != nil {
		return SupervisorAction{}, notFound(err, fmt.Sprintf("a ação %d", id))
	}
	return row.action(), nil
}

// SupervisorActions returns the last actions, the newest first.
func (s *Store) SupervisorActions(ctx context.Context, limit int) ([]SupervisorAction, error) {
	var rows []actionRow
	if err := s.conn(ctx).Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	actions := make([]SupervisorAction, 0, len(rows))
	for _, row := range rows {
		actions = append(actions, row.action())
	}
	return actions, nil
}

// ErrDecided is the error of a proposal confirmed or rejected again, eg: two
// clicks: the first one decides it.
var ErrDecided = errors.New("store: the action was decided already")

// ErrExpired is the error of a proposal confirmed once it expired: the data it
// rested on is old.
var ErrExpired = errors.New("store: the proposal expired")

// ClaimSupervisorAction takes the proposal of id to execute, confirmed by by,
// at once and once: executing, the others (another click, another user)
// getting ErrDecided; a proposal expired is marked so, ErrExpired.
func (s *Store) ClaimSupervisorAction(ctx context.Context, id int64, by string) (SupervisorAction, error) {
	return s.decide(ctx, id, ActionExecuting, by)
}

// RejectSupervisorAction rejects the proposal of id, by by, see
// ClaimSupervisorAction.
func (s *Store) RejectSupervisorAction(ctx context.Context, id int64, by string) (SupervisorAction, error) {
	return s.decide(ctx, id, ActionRejected, by)
}

// decide moves the proposal of id to status, decided by by, see
// ClaimSupervisorAction.
func (s *Store) decide(ctx context.Context, id int64, status, by string) (SupervisorAction, error) {
	now := time.Now()
	var rows []actionRow
	result := s.conn(ctx).Model(&rows).Clauses(clause.Returning{}).
		Where("id = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", id, ActionProposed, now).
		Updates(map[string]any{"status": status, "confirmed_by": by, "decided_at": now})
	if result.Error != nil {
		return SupervisorAction{}, result.Error
	}
	if len(rows) == 1 {
		return rows[0].action(), nil
	}
	current, err := s.SupervisorAction(ctx, id)
	if err != nil {
		return SupervisorAction{}, err
	}
	if current.Status == ActionProposed { // expired meanwhile
		s.conn(ctx).Model(&actionRow{}).Where("id = ? AND status = ?", id, ActionProposed).
			Updates(map[string]any{"status": ActionExpired, "finished_at": now})
		return SupervisorAction{}, fmt.Errorf("%w: a ação %d", ErrExpired, id)
	}
	return current, fmt.Errorf("%w: a ação %d está %s", ErrDecided, id, current.Status)
}

// FinishSupervisorAction records how the action of id ended, status, and its
// result, checked.
func (s *Store) FinishSupervisorAction(ctx context.Context, id int64, status string, result json.RawMessage) error {
	return s.conn(ctx).Model(&actionRow{}).Where("id = ?", id).Updates(map[string]any{"status": status,
		"result": jsonb(result), "finished_at": time.Now()}).Error
}

// ExpireSupervisorActions marks the proposals past their expiry expired.
func (s *Store) ExpireSupervisorActions(ctx context.Context) (int64, error) {
	now := time.Now()
	result := s.conn(ctx).Model(&actionRow{}).Where("status = ? AND expires_at <= ?", ActionProposed, now).
		Updates(map[string]any{"status": ActionExpired, "finished_at": now})
	return result.RowsAffected, result.Error
}

// UnknownExecutingSupervisorActions ends the actions left executing, their
// outcome unknown: the ones of a controller stopped in their middle, to check
// on the accounts, never to execute again.
func (s *Store) UnknownExecutingSupervisorActions(ctx context.Context, why string) (int64, error) {
	data, _ := json.Marshal(map[string]string{"error": why})
	result := s.conn(ctx).Model(&actionRow{}).Where("status = ?", ActionExecuting).
		Updates(map[string]any{"status": ActionUnknown, "finished_at": time.Now(), "result": jsonb(data)})
	return result.RowsAffected, result.Error
}

// The roles and the kinds of the messages of the conversations with the
// supervisor.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"

	MessageQuestion = "question"
	MessageAnswer   = "answer"
	MessageProposal = "proposal"
	MessageResult   = "result"
	MessageError    = "error"
)

// SupervisorMessage is a message of the conversation of a user with the
// supervisor: DataAt is when the data the answer rests on was read.
type SupervisorMessage struct {
	ID        int64      `gorm:"primaryKey" json:"id"`
	UserID    int64      `json:"-"`
	Role      string     `json:"role"`
	Kind      string     `json:"kind"`
	Content   string     `json:"content"`
	ActionID  *int64     `json:"action_id,omitempty"`
	DataAt    *time.Time `json:"data_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func (SupervisorMessage) TableName() string { return "supervisor_messages" }

// AddSupervisorMessage records the message m of its user.
func (s *Store) AddSupervisorMessage(ctx context.Context, m SupervisorMessage) (SupervisorMessage, error) {
	m.ID, m.CreatedAt = 0, time.Now()
	if err := s.conn(ctx).Create(&m).Error; err != nil {
		return SupervisorMessage{}, notFound(err, "uma mensagem da conversa com o XTrader AI")
	}
	return m, nil
}

// SupervisorMessages returns the last limit messages of the conversation of
// the user of userID before the one of before (0 for the last ones), the
// oldest first.
func (s *Store) SupervisorMessages(ctx context.Context, userID, before int64, limit int) ([]SupervisorMessage,
	error) {
	query := s.conn(ctx).Where("user_id = ?", userID)
	if before > 0 {
		query = query.Where("id < ?", before)
	}
	var messages []SupervisorMessage
	if err := query.Order("id DESC").Limit(limit).Find(&messages).Error; err != nil {
		return nil, err
	}
	slices.Reverse(messages)
	return messages, nil
}

// The states of the stop of an agent by the supervisor.
const (
	StopNone        = "none"
	StopPendingFlat = "pending_flat"
	StopStopped     = "stopped"
)

// AgentSupervision is the supervision of an agent: its entries blocked, why,
// by whom and by which action, at a revision growing with every change (see
// agentapi.Entries), and its stop by the supervisor.
type AgentSupervision struct {
	AgentID         int64      `gorm:"primaryKey;autoIncrement:false" json:"-"`
	EntriesBlocked  bool       `json:"entries_blocked"`
	EntriesRevision int64      `json:"entries_revision"`
	EntriesReason   string     `json:"entries_reason"`
	EntriesBy       string     `json:"entries_by"`
	EntriesActionID *int64     `json:"entries_action_id,omitempty"`
	EntriesAt       *time.Time `json:"entries_at,omitempty"`
	StopState       string     `json:"stop_state"`
	StopReason      string     `json:"stop_reason"`
	StopCategory    *string    `json:"stop_category,omitempty"`
	StopBy          string     `json:"stop_by"`
	StopActionID    *int64     `json:"stop_action_id,omitempty"`
	StopAt          *time.Time `json:"stop_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (AgentSupervision) TableName() string { return "agent_supervision" }

// SupervisionOf returns the supervision of the agent a, the one of an agent
// never supervised when it has none.
func (a Agent) SupervisionOf() AgentSupervision {
	if a.Supervision == nil {
		return AgentSupervision{AgentID: a.ID, StopState: StopNone}
	}
	return *a.Supervision
}

// lockSupervision returns the supervision of the agent of id, created when
// missing, locked until the end of the transaction of ctx.
func (s *Store) lockSupervision(ctx context.Context, id int64) (AgentSupervision, error) {
	err := s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&AgentSupervision{AgentID: id, StopState: StopNone, UpdatedAt: time.Now()}).Error
	if err != nil {
		return AgentSupervision{}, notFound(err, fmt.Sprintf("a supervisão do agente %d", id))
	}
	var supervision AgentSupervision
	if err := s.conn(ctx).Clauses(lockForUpdate()).First(&supervision, "agent_id = ?", id).Error; err != nil {
		return AgentSupervision{}, err
	}
	return supervision, nil
}

// SetAgentEntries blocks the entries of the agent of id, for reason, by by, of
// the action of actionID, if any, or allows them again: a new revision either
// way, so that the agent applies it after any older one.
func (s *Store) SetAgentEntries(ctx context.Context, id int64, blocked bool, reason, by string,
	actionID *int64) (AgentSupervision, error) {
	var supervision AgentSupervision
	err := s.Transaction(ctx, func(ctx context.Context) error {
		var err error
		if supervision, err = s.lockSupervision(ctx, id); err != nil {
			return err
		}
		now := time.Now()
		supervision.EntriesBlocked, supervision.EntriesRevision = blocked, supervision.EntriesRevision+1
		supervision.EntriesReason, supervision.EntriesBy, supervision.EntriesActionID = "", by, actionID
		if blocked {
			supervision.EntriesReason = strings.TrimSpace(reason)
		}
		supervision.EntriesAt, supervision.UpdatedAt = &now, now
		return s.conn(ctx).Select("*").Save(&supervision).Error
	})
	return supervision, err
}

// MarkAgentStop records the stop of the agent of id by the supervisor: state
// pending_flat while it waits for the agent to hold nothing, stopped once
// disabled; none clears it.
func (s *Store) MarkAgentStop(ctx context.Context, id int64, state, reason, category, by string,
	actionID *int64) (AgentSupervision, error) {
	var supervision AgentSupervision
	err := s.Transaction(ctx, func(ctx context.Context) error {
		var err error
		if supervision, err = s.lockSupervision(ctx, id); err != nil {
			return err
		}
		now := time.Now()
		supervision.StopState, supervision.StopReason, supervision.StopCategory = state, reason, optionalText(category)
		supervision.StopBy, supervision.StopActionID, supervision.StopAt = by, actionID, &now
		if state == StopNone {
			supervision.StopReason, supervision.StopCategory, supervision.StopBy = "", nil, ""
			supervision.StopActionID, supervision.StopAt = nil, nil
		}
		supervision.UpdatedAt = now
		return s.conn(ctx).Select("*").Save(&supervision).Error
	})
	return supervision, err
}

// StopAgentFlat disables the agent of id, stopped by the supervisor once flat,
// only when it is still enabled and waits for it as the action of actionID
// asked: false when anything changed meanwhile, eg: a user who enabled it
// again, or another stop.
func (s *Store) StopAgentFlat(ctx context.Context, id, actionID int64) (bool, error) {
	stopped := false
	err := s.Transaction(ctx, func(ctx context.Context) error {
		supervision, err := s.lockSupervision(ctx, id)
		if err != nil {
			return err
		}
		if supervision.StopState != StopPendingFlat || supervision.StopActionID == nil ||
			*supervision.StopActionID != actionID {
			return nil
		}
		result := s.conn(ctx).Model(&Agent{}).Where("id = ? AND enabled", id).Update("enabled", false)
		if result.Error != nil {
			return result.Error
		}
		now := time.Now()
		supervision.StopState, supervision.UpdatedAt = StopStopped, now
		if result.RowsAffected == 0 {
			supervision.StopState = StopNone // disabled meanwhile by a user: not the supervisor's
		}
		stopped = result.RowsAffected == 1
		return s.conn(ctx).Select("*").Save(&supervision).Error
	})
	return stopped, err
}

// ClearAgentStop clears the stop of the agent of id by the supervisor, as a
// user enables it again, and allows its entries again when they were blocked
// by that stop (a block of its own stays): true when they were.
func (s *Store) ClearAgentStop(ctx context.Context, id int64, by string) (bool, error) {
	allowed := false
	err := s.Transaction(ctx, func(ctx context.Context) error {
		var supervision AgentSupervision
		err := s.conn(ctx).Clauses(lockForUpdate()).First(&supervision, "agent_id = ?", id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if supervision.StopState == StopNone {
			return nil
		}
		now := time.Now()
		if supervision.EntriesBlocked && supervision.EntriesActionID != nil && supervision.StopActionID != nil &&
			*supervision.EntriesActionID == *supervision.StopActionID {
			supervision.EntriesBlocked, supervision.EntriesReason = false, ""
			supervision.EntriesRevision++
			supervision.EntriesBy, supervision.EntriesActionID, supervision.EntriesAt = by, nil, &now
			allowed = true
		}
		supervision.StopState, supervision.StopReason, supervision.StopCategory = StopNone, "", nil
		supervision.StopBy, supervision.StopActionID, supervision.StopAt = "", nil, nil
		supervision.UpdatedAt = now
		return s.conn(ctx).Select("*").Save(&supervision).Error
	})
	return allowed, err
}
