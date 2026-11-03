package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/agentapi"
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
// ui.TradingHoursSettings: none while never set (off). Its windows are in
// trading_hours_windows (Windows, read with it); its own start, end and
// actions are its first one, kept for the readers of a single window, and its
// only one while it has none there (the hours of before).
type TradingHours struct {
	ID            int16              `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Enabled       bool               `json:"enabled"`
	StartTime     string             `json:"start_time"`
	EndTime       string             `json:"end_time"`
	Flatten       bool               `json:"flatten"`
	CancelPending bool               `json:"cancel_pending"`
	UpdatedBy     string             `json:"updated_by"`
	UpdatedAt     time.Time          `json:"updated_at"`
	Windows       []ui.TradingWindow `gorm:"-" json:"windows,omitempty"`
}

func (TradingHours) TableName() string { return "trading_hours" }

// TradingHoursWindow is a window of the trading hours (trading_hours_windows),
// at Position in the order of the day, see ui.TradingWindow.
type TradingHoursWindow struct {
	ID            int64 `gorm:"primaryKey"`
	HoursID       int16
	Position      int16
	StartTime     string
	EndTime       string
	Flatten       bool
	CancelPending bool
}

func (TradingHoursWindow) TableName() string { return "trading_hours_windows" }

// TradingHoursWindowAgent is an agent allowed in a window of the trading
// hours (trading_hours_window_agents), by id.
type TradingHoursWindowAgent struct {
	WindowID int64 `gorm:"primaryKey;autoIncrement:false"`
	AgentID  int64 `gorm:"primaryKey;autoIncrement:false"`
}

func (TradingHoursWindowAgent) TableName() string { return "trading_hours_window_agents" }

// Settings returns the hours as the dashboard takes them: their windows, the
// first one also as its start, end and actions.
func (h TradingHours) Settings() ui.TradingHoursSettings {
	settings := ui.TradingHoursSettings{Enabled: h.Enabled, UpdatedBy: h.UpdatedBy,
		Windows: agentapi.HoursWindows(h.Windows, h.StartTime, h.EndTime, h.Flatten, h.CancelPending)}
	if settings.Windows == nil {
		settings.Windows = []ui.TradingWindow{}
	}
	if len(settings.Windows) > 0 {
		first := settings.Windows[0]
		settings.Start, settings.End = first.Start, first.End
		settings.Flatten, settings.CancelPending = first.Flatten, first.CancelPending
	}
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

// tradingWindows returns the windows of settings, validated, in the order of
// the day: its Windows, else its start and end as its only one (a client of a
// single window); each HH:MM within the day, none overlapping nor touching
// another (see agentapi.WindowsProblem); on, one at least.
func tradingWindows(settings ui.TradingHoursSettings) ([]ui.TradingWindow, error) {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: horário de operação: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	windows := agentapi.HoursWindows(settings.Windows, settings.Start, settings.End, settings.Flatten,
		settings.CancelPending)
	if problem := agentapi.WindowsProblem(windows); problem != "" {
		return nil, invalid("%s", problem)
	}
	if settings.Enabled && len(windows) == 0 {
		return nil, invalid("adicione ao menos uma janela para ligá-lo")
	}
	return agentapi.SortedWindows(windows), nil
}

// TradingHours returns the trading hours of all the agents, false while never
// set.
func (s *Store) TradingHours(ctx context.Context) (TradingHours, bool, error) {
	var hours TradingHours
	err := s.conn(ctx).Where("id = 1").Take(&hours).Error
	if err == gorm.ErrRecordNotFound {
		return TradingHours{}, false, nil
	}
	if err != nil {
		return TradingHours{}, false, err
	}
	var rows []TradingHoursWindow
	if err := s.conn(ctx).Where("hours_id = 1").Order("position").Find(&rows).Error; err != nil {
		return TradingHours{}, false, err
	}
	var allowed []struct {
		WindowID int64
		Name     string
	}
	if err := s.conn(ctx).Raw("SELECT wa.window_id, a.name FROM trading_hours_window_agents wa " +
		"JOIN agents a ON a.id = wa.agent_id WHERE a.deleted_at IS NULL ORDER BY a.name").
		Scan(&allowed).Error; err != nil {
		return TradingHours{}, false, err
	}
	for _, w := range rows {
		window := ui.TradingWindow{Start: w.StartTime, End: w.EndTime, Flatten: w.Flatten,
			CancelPending: w.CancelPending}
		for _, a := range allowed {
			if a.WindowID == w.ID {
				window.Agents = append(window.Agents, a.Name)
			}
		}
		hours.Windows = append(hours.Windows, window)
	}
	return hours, true, nil
}

// SetTradingHours sets the trading hours of all the agents, validated (see
// tradingWindows), by by (userID, when a user) for reason, recorded with the
// previous ones (see TradingHoursEvent), and returns both: their windows in
// trading_hours_windows, the first one also in their own columns.
func (s *Store) SetTradingHours(ctx context.Context, settings ui.TradingHoursSettings, by string, userID *int64,
	reason string) (previous, next TradingHours, err error) {
	windows, err := tradingWindows(settings)
	if err != nil {
		return previous, next, err
	}
	err = s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.conn(ctx).Clauses(lockForUpdate()).Where("id = 1").Find(&[]TradingHours{}).Error; err != nil {
			return err
		}
		var found bool
		var before any
		if previous, found, err = s.TradingHours(ctx); err != nil {
			return err
		} else if found {
			before = previous.recorded()
		}
		next = TradingHours{ID: 1, Enabled: settings.Enabled, UpdatedBy: by, UpdatedAt: time.Now(), Windows: windows}
		if len(windows) > 0 {
			next.StartTime, next.EndTime = windows[0].Start, windows[0].End
			next.Flatten, next.CancelPending = windows[0].Flatten, windows[0].CancelPending
		}
		if err := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
			Create(&next).Error; err != nil {
			return notFound(err, "o horário de operação")
		}
		if err := s.conn(ctx).Where("hours_id = 1").Delete(&TradingHoursWindow{}).Error; err != nil {
			return err
		}
		ids, err := s.windowAgentIDs(ctx, windows)
		if err != nil {
			return err
		}
		for i, w := range windows {
			row := TradingHoursWindow{HoursID: 1, Position: int16(i), StartTime: w.Start, EndTime: w.End,
				Flatten: w.Flatten, CancelPending: w.CancelPending}
			if err := s.conn(ctx).Create(&row).Error; err != nil {
				return err
			}
			for _, name := range w.Agents {
				if err := s.conn(ctx).Create(&TradingHoursWindowAgent{WindowID: row.ID, AgentID: ids[name]}).
					Error; err != nil {
					return err
				}
			}
		}
		return s.RecordTradingHours(ctx, &TradingHoursEvent{Kind: HoursChanged, RequestedBy: by, UserID: userID,
			Reason: reason, Previous: jsonOf(before), Next: jsonOf(next.recorded())})
	})
	return previous, next, err
}

// windowAgentIDs returns the IDs of the agents of windows, by name: refused
// (ErrInvalid) for a name of no agent.
func (s *Store) windowAgentIDs(ctx context.Context, windows []ui.TradingWindow) (map[string]int64, error) {
	var names []string
	for _, w := range windows {
		names = append(names, w.Agents...)
	}
	ids := make(map[string]int64)
	if len(names) == 0 {
		return ids, nil
	}
	var agents []Agent
	if err := s.conn(ctx).Where("name IN ?", names).Find(&agents).Error; err != nil {
		return nil, err
	}
	for _, a := range agents {
		ids[a.Name] = a.ID
	}
	for _, w := range windows {
		for _, name := range w.Agents {
			if _, ok := ids[name]; !ok {
				return nil, fmt.Errorf("%w: horário de operação: o agente %s da janela %s não existe", ErrInvalid,
					name, agentapi.WindowLabel(w))
			}
		}
	}
	return ids, nil
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

// TradingHoursEntries returns whether the entries of the agent of id are
// blocked by the trading hours (outside their windows, or in a window for other
// agents, as recorded), why, and the revision of the trading hours (the last of
// their events), which only grows.
func (s *Store) TradingHoursEntries(ctx context.Context, id int64) (blocked bool, reason string, revision int64,
	err error) {
	state, found, err := s.TradingHoursState(ctx)
	if err != nil {
		return false, "", 0, err
	}
	err = s.conn(ctx).Raw("SELECT COALESCE(MAX(id), 0) FROM trading_hours_events").Scan(&revision).Error
	if err != nil {
		return false, "", 0, err
	}
	switch {
	case found && state.Kind == HoursClosed:
		blocked, reason = true, state.Reason
	case found && state.Kind == HoursOpened:
		var w ui.TradingWindow
		if json.Unmarshal(state.Next, &w) != nil || len(w.Agents) == 0 {
			break
		}
		var name string
		if err := s.conn(ctx).Raw("SELECT name FROM agents WHERE id = ?", id).Scan(&name).Error; err != nil {
			return false, "", 0, err
		}
		if !agentapi.WindowAllows(w, name) {
			blocked = true
			reason = fmt.Sprintf("Janela %s só para %s", agentapi.WindowLabel(w), strings.Join(w.Agents, ", "))
		}
	}
	return blocked, reason, revision, nil
}
