package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/ui"
)

// DailyLimitMax bounds a daily limit, in its currency.
const DailyLimitMax = 1e9

// The kinds of a daily guard, see DailyGuard.
const (
	GuardLimit      = "limit"      // a limit of the day reached: positions closed, entries blocked
	GuardIncomplete = "incomplete" // the result of the day unknown: entries blocked
)

// The triggers of a daily guard of a limit.
const (
	TriggerLoss = "loss"
	TriggerGain = "gain"
)

// The states of a daily guard.
const (
	GuardTripped  = "tripped"
	GuardReleased = "released"
)

// The statuses of the close of the positions of a daily guard of a limit.
const (
	FlattenRunning = "running"
	FlattenDone    = "done"
	FlattenPartial = "partial"
	FlattenFailed  = "failed"
)

// The kinds of the events of the daily limits, see DailyLimitEvent.
const (
	EventLimitChanged = "limit_changed"
	EventTripped      = "tripped"
	EventReleased     = "released"
	EventIncomplete   = "incomplete"
	EventFlatten      = "flatten"
	EventDayRollover  = "day_rollover"
)

// Where a change of the daily limits came from, see DailyChange.
const (
	ViaSystem   = "system"
	ViaSettings = "settings"
	ViaChat     = "chat"
)

// DailyLimit is the daily limits of all the agents together, a single record,
// see ui.DailyLimitSettings: none while never set (no limit). Its amounts
// apply to the result of the agents in each currency of their accounts apart,
// none converted: Currency is unused, NoCurrency once set (the column, of
// migration 27, needs one while enabled).
type DailyLimit struct {
	ID        int16     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Enabled   bool      `json:"enabled"`
	Currency  string    `json:"currency"`
	MaxLoss   *float64  `json:"max_loss,omitempty"`
	MaxGain   *float64  `json:"max_gain,omitempty"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (DailyLimit) TableName() string { return "daily_limit_settings" }

// NoCurrency is the ISO 4217 code of no currency, kept in the unused currency
// of the daily limits of all the agents, see DailyLimit.
const NoCurrency = "XXX"

// Settings returns the limits as the dashboard takes them, without a currency:
// they apply in the one of each account.
func (l DailyLimit) Settings() ui.DailyLimitSettings {
	settings := ui.DailyLimitSettings{Enabled: l.Enabled, MaxLoss: l.MaxLoss, MaxGain: l.MaxGain,
		UpdatedBy: l.UpdatedBy}
	if !l.UpdatedAt.IsZero() {
		settings.UpdatedAt = l.UpdatedAt.Unix()
	}
	return settings
}

// AgentDailyLimit is the daily limits of an agent, in the currency of its
// account, see ui.DailyLimitSettings: a row once set, none without limits.
type AgentDailyLimit struct {
	AgentID   int64     `gorm:"primaryKey;autoIncrement:false" json:"agent_id"`
	Enabled   bool      `json:"enabled"`
	MaxLoss   *float64  `json:"max_loss,omitempty"`
	MaxGain   *float64  `json:"max_gain,omitempty"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (AgentDailyLimit) TableName() string { return "agent_daily_limits" }

// Settings returns the limits as the dashboard takes them.
func (l AgentDailyLimit) Settings() ui.DailyLimitSettings {
	settings := ui.DailyLimitSettings{Enabled: l.Enabled, MaxLoss: l.MaxLoss, MaxGain: l.MaxGain,
		UpdatedBy: l.UpdatedBy}
	if !l.UpdatedAt.IsZero() {
		settings.UpdatedAt = l.UpdatedAt.Unix()
	}
	return settings
}

// DailyLimitOf returns the daily limits of a, off without its row.
func (a Agent) DailyLimitOf() ui.DailyLimitSettings {
	if a.DailyLimit == nil {
		return ui.DailyLimitSettings{}
	}
	return a.DailyLimit.Settings()
}

// DailyChange is who changes the daily limits and why, for their record: the
// day of the change (eg: 2026-10-29, of the zone of the limits), through
// where (see ViaSettings), by whom, the user if one.
type DailyChange struct {
	Day    string
	Via    string
	By     string
	UserID *int64
	Reason string
}

// DailyLimitEvent is a record of the daily limits (daily_limit_events): a
// limit changed (Previous and Next settings), a guard tripped or released, the
// positions closed (ActionID); Metric the result of the day then; of an agent
// or of all the agents (AgentID nil). Its ID is the revision of the entries
// of the agents it concerns.
type DailyLimitEvent struct {
	ID          int64           `gorm:"primaryKey" json:"id"`
	At          time.Time       `gorm:"default:now()" json:"at"`
	Day         string          `gorm:"type:date" json:"day"`
	AgentID     *int64          `json:"agent_id,omitempty"`
	GuardID     *int64          `json:"guard_id,omitempty"`
	Kind        string          `json:"kind"`
	Via         string          `json:"via"`
	RequestedBy string          `json:"requested_by"`
	UserID      *int64          `json:"user_id,omitempty"`
	Reason      string          `json:"reason"`
	Previous    json.RawMessage `gorm:"type:jsonb" json:"previous,omitempty"`
	Next        json.RawMessage `gorm:"type:jsonb" json:"next,omitempty"`
	Metric      json.RawMessage `gorm:"type:jsonb" json:"metric,omitempty"`
	ActionID    *int64          `json:"action_id,omitempty"`
}

func (DailyLimitEvent) TableName() string { return "daily_limit_events" }

// DailyGuard is a daily guard (daily_guards): a limit of Day reached (Kind
// GuardLimit, Trigger loss or gain, Result and LimitAmount in Currency) or the
// result of the day unknown (GuardIncomplete), of an agent or of all the
// agents (AgentID nil); tripped, the entries of its agents are blocked until
// it is released; the close of its positions (FlattenStatus).
type DailyGuard struct {
	ID              int64      `gorm:"primaryKey" json:"id"`
	Day             time.Time  `gorm:"type:date" json:"day"`
	AgentID         *int64     `json:"agent_id,omitempty"`
	Kind            string     `json:"kind"`
	Trigger         *string    `json:"trigger,omitempty"`
	State           string     `json:"state"`
	Result          float64    `json:"result"`
	LimitAmount     float64    `json:"limit_amount"`
	Currency        string     `json:"currency"`
	Reason          string     `json:"reason"`
	Trips           int        `json:"trips"`
	TrippedAt       time.Time  `json:"tripped_at"`
	ReleasedAt      *time.Time `json:"released_at,omitempty"`
	ReleasedBy      string     `json:"released_by"`
	ReleaseReason   string     `json:"release_reason"`
	FlattenStatus   string     `json:"flatten_status"`
	FlattenActionID *int64     `json:"flatten_action_id,omitempty"`
	FlattenAttempts int        `json:"flatten_attempts"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (DailyGuard) TableName() string { return "daily_guards" }

// DayOf returns the day of the guard, eg: 2026-10-29.
func (g DailyGuard) DayOf() string { return g.Day.Format(time.DateOnly) }

// TriggerOf returns the trigger of the guard, empty for none.
func (g DailyGuard) TriggerOf() string {
	if g.Trigger == nil {
		return ""
	}
	return *g.Trigger
}

// Info returns the guard as the dashboard takes it.
func (g DailyGuard) Info() ui.DailyGuardInfo {
	return ui.DailyGuardInfo{ID: g.ID, Kind: g.Kind, Trigger: g.TriggerOf(), Result: g.Result, Limit: g.LimitAmount,
		Currency: g.Currency, Reason: g.Reason, Trips: g.Trips, TrippedAt: g.TrippedAt.Unix(),
		FlattenStatus: g.FlattenStatus, FlattenActionID: g.FlattenActionID}
}

// validateDailyLimit returns why settings are refused, as the database checks
// them: each limit set above 0 and up to DailyLimitMax; enabled, a limit at
// least; no currency, of all the agents (their amounts apply in the currency of
// each account apart) as of an agent (the one of its account).
func validateDailyLimit(settings ui.DailyLimitSettings, global bool) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: the daily limits: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	for name, value := range map[string]*float64{"the loss": settings.MaxLoss, "the gain": settings.MaxGain} {
		if value != nil && (math.IsNaN(*value) || *value <= 0 || *value > DailyLimitMax) {
			return invalid("%s: above 0 and up to %.0f", name, DailyLimitMax)
		}
	}
	if settings.Enabled && settings.MaxLoss == nil && settings.MaxGain == nil {
		return invalid("enabled, a loss or a gain at least")
	}
	switch {
	case global && settings.Currency != "":
		return invalid("the limits of all the agents apply in the currency of each account, none to set")
	case !global && settings.Currency != "":
		return invalid("the limits of an agent are in the currency of its account")
	}
	return nil
}

// jsonOf returns v as JSON, nil for a nil v.
func jsonOf(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// addDailyEvent records e, its ID set.
func (s *Store) addDailyEvent(ctx context.Context, e *DailyLimitEvent) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return s.conn(ctx).Create(e).Error
}

// GlobalDailyLimit returns the daily limits of all the agents together, false
// while never set.
func (s *Store) GlobalDailyLimit(ctx context.Context) (DailyLimit, bool, error) {
	var limit DailyLimit
	err := s.conn(ctx).Where("id = 1").Take(&limit).Error
	if err == gorm.ErrRecordNotFound {
		return DailyLimit{}, false, nil
	}
	return limit, err == nil, err
}

// SetGlobalDailyLimit sets the daily limits of all the agents together,
// validated (see validateDailyLimit), recorded with the previous ones (see
// DailyLimitEvent), and returns both.
func (s *Store) SetGlobalDailyLimit(ctx context.Context, settings ui.DailyLimitSettings,
	change DailyChange) (previous, next DailyLimit, err error) {
	settings.Currency = strings.TrimSpace(settings.Currency)
	if err = validateDailyLimit(settings, true); err != nil {
		return previous, next, err
	}
	err = s.Transaction(ctx, func(ctx context.Context) error {
		var rows []DailyLimit
		if err := s.conn(ctx).Clauses(lockForUpdate()).Where("id = 1").Find(&rows).Error; err != nil {
			return err
		}
		var before any
		if len(rows) > 0 {
			previous = rows[0]
			before = previous.Settings()
		}
		next = DailyLimit{ID: 1, Enabled: settings.Enabled, Currency: NoCurrency, MaxLoss: settings.MaxLoss,
			MaxGain: settings.MaxGain, UpdatedBy: change.By, UpdatedAt: time.Now()}
		if err := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
			Create(&next).Error; err != nil {
			return notFound(err, "the daily limits")
		}
		return s.addDailyEvent(ctx, &DailyLimitEvent{Day: change.Day, Kind: EventLimitChanged, Via: change.Via,
			RequestedBy: change.By, UserID: change.UserID, Reason: change.Reason, Previous: jsonOf(before),
			Next: jsonOf(next.Settings())})
	})
	return previous, next, err
}

// SetAgentDailyLimit sets the daily limits of the agent of id, validated (see
// validateDailyLimit), recorded with the previous ones, and returns both.
func (s *Store) SetAgentDailyLimit(ctx context.Context, id int64, settings ui.DailyLimitSettings,
	change DailyChange) (previous, next AgentDailyLimit, err error) {
	if err = validateDailyLimit(settings, false); err != nil {
		return previous, next, err
	}
	err = s.Transaction(ctx, func(ctx context.Context) error {
		var rows []AgentDailyLimit
		if err := s.conn(ctx).Clauses(lockForUpdate()).Where("agent_id = ?", id).Find(&rows).Error; err != nil {
			return err
		}
		var before any
		if len(rows) > 0 {
			previous = rows[0]
			before = previous.Settings()
		}
		next = AgentDailyLimit{AgentID: id, Enabled: settings.Enabled, MaxLoss: settings.MaxLoss,
			MaxGain: settings.MaxGain, UpdatedBy: change.By, UpdatedAt: time.Now()}
		if err := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}}, UpdateAll: true}).
			Create(&next).Error; err != nil {
			return notFound(err, fmt.Sprintf("the daily limits of the agent %d", id))
		}
		return s.addDailyEvent(ctx, &DailyLimitEvent{Day: change.Day, AgentID: &id, Kind: EventLimitChanged,
			Via: change.Via, RequestedBy: change.By, UserID: change.UserID, Reason: change.Reason,
			Previous: jsonOf(before), Next: jsonOf(next.Settings())})
	})
	return previous, next, err
}

// GuardTrip is a daily guard to trip, see TripDailyGuard: its day, scope
// (AgentID, nil for all the agents), kind and trigger; the result and the
// limit in Currency, why, and the result of the day (Metric) for the record.
type GuardTrip struct {
	Day      string
	AgentID  *int64
	Kind     string
	Trigger  string
	Result   float64
	Limit    float64
	Currency string
	Reason   string
	Metric   any
}

// TripDailyGuard trips the guard of trip: created, or tripped again when
// released that day (its trips counted), recorded (see DailyLimitEvent); a
// guard tripped already is left as it is. newly tells it tripped now.
func (s *Store) TripDailyGuard(ctx context.Context, trip GuardTrip) (guard DailyGuard, newly bool, err error) {
	var trigger *string
	if trip.Trigger != "" {
		trigger = &trip.Trigger
	}
	err = s.Transaction(ctx, func(ctx context.Context) error {
		var rows []DailyGuard
		err := s.conn(ctx).Raw(`INSERT INTO daily_guards (day, agent_id, kind, trigger, state, result, limit_amount,
				currency, reason, tripped_at, updated_at)
			VALUES (?::date, ?, ?, ?, 'tripped', ?, ?, ?, ?, now(), now())
			ON CONFLICT (day, COALESCE(agent_id, 0), kind) DO UPDATE SET trigger = EXCLUDED.trigger,
				state = 'tripped', result = EXCLUDED.result, limit_amount = EXCLUDED.limit_amount,
				currency = EXCLUDED.currency, reason = EXCLUDED.reason, trips = daily_guards.trips + 1,
				tripped_at = now(), released_at = NULL, released_by = '', release_reason = '', flatten_status = '',
				flatten_action_id = NULL, flatten_attempts = 0, updated_at = now()
			WHERE daily_guards.state = 'released'
			RETURNING *`, trip.Day, trip.AgentID, trip.Kind, trigger, trip.Result, trip.Limit, trip.Currency,
			trip.Reason).Scan(&rows).Error
		if err != nil {
			return notFound(err, "the daily guard")
		}
		if len(rows) == 0 { // tripped already
			q := s.conn(ctx).Where("day = ?::date AND kind = ? AND state = ?", trip.Day, trip.Kind, GuardTripped)
			if trip.AgentID == nil {
				q = q.Where("agent_id IS NULL")
			} else {
				q = q.Where("agent_id = ?", *trip.AgentID)
			}
			return q.Take(&guard).Error
		}
		guard, newly = rows[0], true
		kind := EventTripped
		if trip.Kind == GuardIncomplete {
			kind = EventIncomplete
		}
		return s.addDailyEvent(ctx, &DailyLimitEvent{Day: trip.Day, AgentID: trip.AgentID, GuardID: &guard.ID,
			Kind: kind, Via: ViaSystem, RequestedBy: "system", Reason: trip.Reason, Metric: jsonOf(trip.Metric)})
	})
	return guard, newly, err
}

// ReleaseDailyGuard releases the guard of id, tripped, for change (eg: a limit
// raised above the result), recorded; false when it wasn't tripped.
func (s *Store) ReleaseDailyGuard(ctx context.Context, id int64, change DailyChange,
	metric any) (DailyGuard, bool, error) {
	var released []DailyGuard
	err := s.Transaction(ctx, func(ctx context.Context) error {
		err := s.conn(ctx).Raw(`UPDATE daily_guards SET state = 'released', released_at = now(), released_by = ?,
				release_reason = ?, updated_at = now()
			WHERE id = ? AND state = 'tripped' RETURNING *`, change.By, change.Reason, id).Scan(&released).Error
		if err != nil || len(released) == 0 {
			return err
		}
		g := released[0]
		return s.addDailyEvent(ctx, &DailyLimitEvent{Day: change.Day, AgentID: g.AgentID, GuardID: &g.ID,
			Kind: EventReleased, Via: change.Via, RequestedBy: change.By, UserID: change.UserID, Reason: change.Reason,
			Metric: jsonOf(metric)})
	})
	if err != nil || len(released) == 0 {
		return DailyGuard{}, false, err
	}
	return released[0], true, nil
}

// RolloverDailyGuards releases the guards tripped before today (eg:
// 2026-10-29), at the end of their day, recorded, and returns them.
func (s *Store) RolloverDailyGuards(ctx context.Context, today string) ([]DailyGuard, error) {
	var released []DailyGuard
	err := s.Transaction(ctx, func(ctx context.Context) error {
		err := s.conn(ctx).Raw(`UPDATE daily_guards SET state = 'released', released_at = now(),
				released_by = 'system', release_reason = 'day_rollover', updated_at = now()
			WHERE state = 'tripped' AND day < ?::date RETURNING *`, today).Scan(&released).Error
		if err != nil {
			return err
		}
		for _, g := range released {
			err := s.addDailyEvent(ctx, &DailyLimitEvent{Day: today, AgentID: g.AgentID, GuardID: &g.ID,
				Kind: EventDayRollover, Via: ViaSystem, RequestedBy: "system",
				Reason: "a virada do dia libera a trava de " + g.DayOf()})
			if err != nil {
				return err
			}
		}
		return nil
	})
	return released, err
}

// SetGuardFlatten records how the positions of the guard of id are closed:
// status (see FlattenRunning), the action closing them and the attempts, and,
// once ended, an event of it.
func (s *Store) SetGuardFlatten(ctx context.Context, id int64, status string, actionID *int64,
	attempts int) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		var g DailyGuard
		if err := s.conn(ctx).Take(&g, id).Error; err != nil {
			return notFound(err, fmt.Sprintf("the daily guard %d", id))
		}
		err := s.conn(ctx).Model(&DailyGuard{}).Where("id = ?", id).Updates(map[string]any{"flatten_status": status,
			"flatten_action_id": actionID, "flatten_attempts": attempts, "updated_at": time.Now()}).Error
		if err != nil || status == FlattenRunning {
			return err
		}
		return s.addDailyEvent(ctx, &DailyLimitEvent{Day: g.DayOf(), AgentID: g.AgentID, GuardID: &g.ID,
			Kind: EventFlatten, Via: ViaSystem, RequestedBy: "system", ActionID: actionID,
			Reason: fmt.Sprintf("fechamento das posições: %s (tentativa %d)", status, attempts)})
	})
}

// TrippedDailyGuards returns the daily guards tripped, of any day, in their
// order.
func (s *Store) TrippedDailyGuards(ctx context.Context) ([]DailyGuard, error) {
	var guards []DailyGuard
	err := s.conn(ctx).Where("state = ?", GuardTripped).Order("id").Find(&guards).Error
	return guards, err
}

// DailyLimitEvents returns the last limit records of the daily limits, the
// newest first.
func (s *Store) DailyLimitEvents(ctx context.Context, limit int) ([]DailyLimitEvent, error) {
	var events []DailyLimitEvent
	err := s.conn(ctx).Order("id DESC").Limit(limit).Find(&events).Error
	return events, err
}

// DailyEntries returns whether the entries of the agent of id are blocked by a
// daily guard tripped, its own or the one of all the agents, of any day (only
// the end of its day releases one), why, and the revision of the daily limits
// it concerns (the last of their events), which only grows.
func (s *Store) DailyEntries(ctx context.Context, id int64) (blocked bool, reason string, revision int64,
	err error) {
	var guards []DailyGuard
	err = s.conn(ctx).Where("state = ? AND (agent_id IS NULL OR agent_id = ?)", GuardTripped, id).
		Order("agent_id NULLS FIRST, id").Find(&guards).Error
	if err != nil {
		return false, "", 0, err
	}
	err = s.conn(ctx).Raw("SELECT COALESCE(MAX(id), 0) FROM daily_limit_events WHERE agent_id IS NULL OR agent_id = ?",
		id).Scan(&revision).Error
	if err != nil {
		return false, "", 0, err
	}
	if len(guards) > 0 {
		blocked, reason = true, guards[0].Reason
	}
	return blocked, reason, revision, nil
}
