package store

import (
	"context"
	"fmt"
	"time"
)

// MarginGuard is a guard of the margin of a daily loss limit
// (daily_margin_guards): the loss left to the limit below its least for new
// entries (see ui.DailyLimitSettings.MinMargin), of all the agents (AgentID
// nil) or of an agent, a day; tripped, the entries of its agents are blocked,
// nothing closed, until it is released; the margin, its least and the result
// of the day then, in Currency; why. Its trips and releases are recorded as
// events of the daily limits (guard_id NULL, the guard in their metric).
type MarginGuard struct {
	ID            int64      `gorm:"primaryKey" json:"id"`
	Day           time.Time  `gorm:"type:date" json:"day"`
	AgentID       *int64     `json:"agent_id,omitempty"`
	State         string     `json:"state"`
	Margin        float64    `json:"margin"`
	Least         float64    `json:"least"`
	Result        float64    `json:"result"`
	Currency      string     `json:"currency"`
	Reason        string     `json:"reason"`
	Trips         int        `json:"trips"`
	TrippedAt     time.Time  `json:"tripped_at"`
	ReleasedAt    *time.Time `json:"released_at,omitempty"`
	ReleaseReason string     `json:"release_reason"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (MarginGuard) TableName() string { return "daily_margin_guards" }

// Guard returns the guard of the margin as a daily guard of kind GuardMargin,
// for the code weighing the guards of the scopes together (its ID of the
// guards of the margin, never of daily_guards).
func (g MarginGuard) Guard() DailyGuard {
	return DailyGuard{ID: g.ID, Day: g.Day, AgentID: g.AgentID, Kind: GuardMargin, State: g.State, Result: g.Result,
		LimitAmount: g.Least, Currency: g.Currency, Reason: g.Reason, Trips: g.Trips, TrippedAt: g.TrippedAt,
		UpdatedAt: g.UpdatedAt}
}

// MarginTrip is a guard of the margin to trip, see TripMarginGuard: its day,
// scope (AgentID, nil for all the agents), the margin, its least and the
// result in Currency, why, and the result of the day (Metric) for the record.
type MarginTrip struct {
	Day      string
	AgentID  *int64
	Margin   float64
	Least    float64
	Result   float64
	Currency string
	Reason   string
	Metric   any
}

// marginEvent returns the metric of an event of a guard of the margin.
func marginEvent(id int64, metric any) map[string]any {
	return map[string]any{"guard": GuardMargin, "margin_guard_id": id, "metric": metric}
}

// TripMarginGuard trips the guard of the margin of trip, once a day by scope
// (again after a release, its trips counted), recorded as an event tripped;
// newly false when it was tripped already.
func (s *Store) TripMarginGuard(ctx context.Context, trip MarginTrip) (guard MarginGuard, newly bool, err error) {
	err = s.Transaction(ctx, func(ctx context.Context) error {
		var rows []MarginGuard
		err := s.conn(ctx).Raw(`INSERT INTO daily_margin_guards (day, agent_id, state, margin, least, result, currency,
				reason, tripped_at, updated_at)
			VALUES (?::date, ?, 'tripped', ?, ?, ?, ?, ?, now(), now())
			ON CONFLICT (day, COALESCE(agent_id, 0)) DO UPDATE SET state = 'tripped', margin = EXCLUDED.margin,
				least = EXCLUDED.least, result = EXCLUDED.result, currency = EXCLUDED.currency,
				reason = EXCLUDED.reason, trips = daily_margin_guards.trips + 1, tripped_at = now(),
				released_at = NULL, release_reason = '', updated_at = now()
			WHERE daily_margin_guards.state = 'released'
			RETURNING *`, trip.Day, trip.AgentID, trip.Margin, trip.Least, trip.Result, trip.Currency,
			trip.Reason).Scan(&rows).Error
		if err != nil {
			return notFound(err, "o bloqueio da margem do limite diário")
		}
		if len(rows) == 0 { // tripped already
			q := s.conn(ctx).Where("day = ?::date AND state = ?", trip.Day, GuardTripped)
			if trip.AgentID == nil {
				q = q.Where("agent_id IS NULL")
			} else {
				q = q.Where("agent_id = ?", *trip.AgentID)
			}
			return q.Take(&guard).Error
		}
		guard, newly = rows[0], true
		return s.addDailyEvent(ctx, &DailyLimitEvent{Day: trip.Day, AgentID: trip.AgentID, Kind: EventTripped,
			Via: ViaSystem, RequestedBy: "system", Reason: trip.Reason, Metric: jsonOf(marginEvent(guard.ID,
				trip.Metric))})
	})
	return guard, newly, err
}

// ReleaseMarginGuard releases the guard of the margin of id, tripped, for
// change, recorded as an event released; false when it wasn't tripped.
func (s *Store) ReleaseMarginGuard(ctx context.Context, id int64, change DailyChange, metric any) (bool, error) {
	var released []MarginGuard
	err := s.Transaction(ctx, func(ctx context.Context) error {
		err := s.conn(ctx).Raw(`UPDATE daily_margin_guards SET state = 'released', released_at = now(),
				release_reason = ?, updated_at = now()
			WHERE id = ? AND state = 'tripped' RETURNING *`, change.Reason, id).Scan(&released).Error
		if err != nil || len(released) == 0 {
			return err
		}
		g := released[0]
		return s.addDailyEvent(ctx, &DailyLimitEvent{Day: change.Day, AgentID: g.AgentID, Kind: EventReleased,
			Via: change.Via, RequestedBy: change.By, UserID: change.UserID, Reason: change.Reason,
			Metric: jsonOf(marginEvent(g.ID, metric))})
	})
	return len(released) > 0, err
}

// TrippedMarginGuards returns the guards of the margin tripped.
func (s *Store) TrippedMarginGuards(ctx context.Context) ([]MarginGuard, error) {
	var guards []MarginGuard
	err := s.conn(ctx).Where("state = ?", GuardTripped).Order("agent_id NULLS FIRST, id").Find(&guards).Error
	return guards, err
}

// rolloverMarginGuards releases the guards of the margin tripped before today,
// at the end of their day, within the transaction of ctx, recorded; returned
// as daily guards of kind GuardMargin.
func (s *Store) rolloverMarginGuards(ctx context.Context, today string) ([]DailyGuard, error) {
	var released []MarginGuard
	err := s.conn(ctx).Raw(`UPDATE daily_margin_guards SET state = 'released', released_at = now(),
			release_reason = 'day_rollover', updated_at = now()
		WHERE state = 'tripped' AND day < ?::date RETURNING *`, today).Scan(&released).Error
	if err != nil {
		return nil, err
	}
	guards := make([]DailyGuard, 0, len(released))
	for _, g := range released {
		day := g.Day.Format(time.DateOnly)
		err := s.addDailyEvent(ctx, &DailyLimitEvent{Day: today, AgentID: g.AgentID, Kind: EventDayRollover,
			Via: ViaSystem, RequestedBy: "system", Reason: fmt.Sprintf("a virada do dia libera a trava da margem "+
				"de %s", day), Metric: jsonOf(marginEvent(g.ID, nil))})
		if err != nil {
			return nil, err
		}
		guards = append(guards, g.Guard())
	}
	return guards, nil
}
