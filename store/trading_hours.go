package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/ui"
)

// The kinds of the events of the trading hours, see TradingHoursEvent.
const (
	HoursChanged   = "changed"    // the hours changed
	HoursClosed    = "closed"     // the window closed: the entries of the agents blocked
	HoursOpened    = "opened"     // the window opened: the entries free
	HoursReleased  = "released"   // the hours off: the entries free
	HoursEndAction = "end_action" // the positions closed or the pending orders canceled at the end
)

// TradingHours is the trading hours of all the agents, a single record, see
// ui.TradingHoursSettings: none while never set (off).
type TradingHours struct {
	ID            int16     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Enabled       bool      `json:"enabled"`
	StartTime     string    `json:"start_time"`
	EndTime       string    `json:"end_time"`
	Flatten       bool      `json:"flatten"`
	CancelPending bool      `json:"cancel_pending"`
	UpdatedBy     string    `json:"updated_by"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (TradingHours) TableName() string { return "trading_hours" }

// Settings returns the hours as the dashboard takes them.
func (h TradingHours) Settings() ui.TradingHoursSettings {
	settings := ui.TradingHoursSettings{Enabled: h.Enabled, Start: h.StartTime, End: h.EndTime, Flatten: h.Flatten,
		CancelPending: h.CancelPending, UpdatedBy: h.UpdatedBy}
	if !h.UpdatedAt.IsZero() {
		settings.UpdatedAt = h.UpdatedAt.Unix()
	}
	return settings
}

// recorded returns the hours as their events record them: without who
// changed them last, the event telling it.
func (h TradingHours) recorded() ui.TradingHoursSettings {
	settings := h.Settings()
	settings.UpdatedBy, settings.UpdatedAt = "", 0
	return settings
}

// TradingHoursEvent is an event of the trading hours (trading_hours_events):
// the hours changed (Previous and Next), the window closed, opened or released
// (the hours off), or the actions at its end (ActionID and their Status). Its
// ID is the revision of the entries of the agents.
type TradingHoursEvent struct {
	ID          int64           `gorm:"primaryKey" json:"id"`
	At          time.Time       `json:"at"`
	Kind        string          `json:"kind"`
	RequestedBy string          `json:"requested_by"`
	UserID      *int64          `json:"user_id,omitempty"`
	Reason      string          `json:"reason"`
	Previous    json.RawMessage `gorm:"type:jsonb" json:"previous,omitempty"`
	Next        json.RawMessage `gorm:"type:jsonb" json:"next,omitempty"`
	ActionID    *int64          `json:"action_id,omitempty"`
	Status      string          `json:"status"`
}

func (TradingHoursEvent) TableName() string { return "trading_hours_events" }

// hoursClock is a time of the trading hours, HH:MM.
var hoursClock = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// validateTradingHours returns why settings are refused, as the database
// checks them: the times HH:MM when set; on, both set and apart.
func validateTradingHours(settings ui.TradingHoursSettings) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: the trading hours: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	for name, value := range map[string]string{"the start": settings.Start, "the end": settings.End} {
		if value != "" && !hoursClock.MatchString(value) {
			return invalid("%s: a time HH:MM, from 00:00 to 23:59", name)
		}
	}
	switch {
	case settings.Enabled && (settings.Start == "" || settings.End == ""):
		return invalid("on, the start and the end")
	case settings.Enabled && settings.Start == settings.End:
		return invalid("the start and the end apart")
	}
	return nil
}

// TradingHours returns the trading hours of all the agents, false while never
// set.
func (s *Store) TradingHours(ctx context.Context) (TradingHours, bool, error) {
	var hours TradingHours
	err := s.conn(ctx).Where("id = 1").Take(&hours).Error
	if err == gorm.ErrRecordNotFound {
		return TradingHours{}, false, nil
	}
	return hours, err == nil, err
}

// SetTradingHours sets the trading hours of all the agents, validated (see
// validateTradingHours), by by (userID, when a user) for reason, recorded with
// the previous ones (see TradingHoursEvent), and returns both.
func (s *Store) SetTradingHours(ctx context.Context, settings ui.TradingHoursSettings, by string, userID *int64,
	reason string) (previous, next TradingHours, err error) {
	if err = validateTradingHours(settings); err != nil {
		return previous, next, err
	}
	err = s.Transaction(ctx, func(ctx context.Context) error {
		var rows []TradingHours
		if err := s.conn(ctx).Clauses(lockForUpdate()).Where("id = 1").Find(&rows).Error; err != nil {
			return err
		}
		var before any
		if len(rows) > 0 {
			previous = rows[0]
			before = previous.recorded()
		}
		next = TradingHours{ID: 1, Enabled: settings.Enabled, StartTime: settings.Start, EndTime: settings.End,
			Flatten: settings.Flatten, CancelPending: settings.CancelPending, UpdatedBy: by, UpdatedAt: time.Now()}
		if err := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
			Create(&next).Error; err != nil {
			return notFound(err, "the trading hours")
		}
		return s.RecordTradingHours(ctx, &TradingHoursEvent{Kind: HoursChanged, RequestedBy: by, UserID: userID,
			Reason: reason, Previous: jsonOf(before), Next: jsonOf(next.recorded())})
	})
	return previous, next, err
}

// RecordTradingHours records e, its ID (the revision of the entries) set.
func (s *Store) RecordTradingHours(ctx context.Context, e *TradingHoursEvent) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return s.conn(ctx).Create(e).Error
}

// TradingHoursState returns the last event of the trading hours closing,
// opening or releasing their window: how they stand, false while none.
func (s *Store) TradingHoursState(ctx context.Context) (TradingHoursEvent, bool, error) {
	var events []TradingHoursEvent
	err := s.conn(ctx).Where("kind IN ?", []string{HoursClosed, HoursOpened, HoursReleased}).Order("id DESC").
		Limit(1).Find(&events).Error
	if err != nil || len(events) == 0 {
		return TradingHoursEvent{}, false, err
	}
	return events[0], true, nil
}

// LastTradingHoursEnd returns the last actions at the end of the window,
// false while none.
func (s *Store) LastTradingHoursEnd(ctx context.Context) (TradingHoursEvent, bool, error) {
	var events []TradingHoursEvent
	err := s.conn(ctx).Where("kind = ?", HoursEndAction).Order("id DESC").Limit(1).Find(&events).Error
	if err != nil || len(events) == 0 {
		return TradingHoursEvent{}, false, err
	}
	return events[0], true, nil
}

// SetTradingHoursEnd records how the actions at the end of the window of the
// event of id ended: status and the action.
func (s *Store) SetTradingHoursEnd(ctx context.Context, id int64, status string, actionID *int64) error {
	return s.conn(ctx).Model(&TradingHoursEvent{}).Where("id = ? AND kind = ?", id, HoursEndAction).
		Updates(map[string]any{"status": status, "action_id": actionID}).Error
}

// TradingHoursEntries returns whether the entries of the agents are blocked by
// the trading hours (their window closed, as recorded), why, and the revision
// of the trading hours (the last of their events), which only grows.
func (s *Store) TradingHoursEntries(ctx context.Context) (blocked bool, reason string, revision int64, err error) {
	state, found, err := s.TradingHoursState(ctx)
	if err != nil {
		return false, "", 0, err
	}
	err = s.conn(ctx).Raw("SELECT COALESCE(MAX(id), 0) FROM trading_hours_events").Scan(&revision).Error
	if err != nil {
		return false, "", 0, err
	}
	if found && state.Kind == HoursClosed {
		blocked, reason = true, state.Reason
	}
	return blocked, reason, revision, nil
}
