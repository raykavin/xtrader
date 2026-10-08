package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The accounting periods of the agents (see store.Period): the cards of the
// dashboard and the /profit of Telegram count the trades closed since the
// start of the current one; the reset of the results of an agent starts a new
// one, the earlier ones kept. The orders are never touched.

// currentPeriods returns the current period of the agents with one, by name:
// none without the settings, eg: in the tests of the dashboard alone, or when
// they can't be read, logged.
func (d *Dashboard) currentPeriods(ctx context.Context) map[string]store.Period {
	if d.settings == nil {
		return nil
	}
	periods, err := d.settings.store.CurrentPeriods(ctx)
	if err != nil {
		log.Errorf("ui: the periods of the agents: %v", err)
		return nil
	}
	return periods
}

// currentPeriod returns the start of the current period of the agent of name,
// false before its first reset: all its trades count.
func (d *Dashboard) currentPeriod(ctx context.Context, name string) (time.Time, bool) {
	period, ok := d.currentPeriods(ctx)[name]
	return period.StartedAt, ok
}

// uiPeriod returns p as the API shows it, next the period after it, nil for
// the current one.
func uiPeriod(p store.Period, next *store.Period) ui.AgentPeriod {
	period := ui.AgentPeriod{ID: p.ID, StartedAt: p.StartedAt.Unix(), Deposit: p.Deposit, Currency: p.Currency,
		CreatedBy: p.CreatedBy}
	if next != nil {
		ended := next.StartedAt.Unix()
		period.EndedAt = &ended
	}
	return period
}

// handlePeriods lists the periods of an agent, the current one first.
func (s *settings) handlePeriods(w http.ResponseWriter, r *http.Request) {
	periods, err := s.store.Periods(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	list := make([]ui.AgentPeriod, 0, len(periods))
	for i, p := range periods {
		var next *store.Period
		if i > 0 {
			next = &periods[i-1]
		}
		list = append(list, uiPeriod(p, next))
	}
	writeJSON(w, ui.AgentPeriodsResponse{Periods: list})
}

// handleStartPeriod resets the results of every pair of an agent: a period
// starting now, with the deposit of the request as its balance. Nothing of the
// agent, its orders or its positions changes: the dashboard counts the trades
// closed from now on, as soon as it lists the agents again.
func (s *settings) handleStartPeriod(w http.ResponseWriter, r *http.Request) {
	var request ui.AgentPeriodRequest
	if !decode(w, r, &request) {
		return
	}
	name := chi.URLParam(r, "name")
	user := userOf(r.Context())
	period, err := s.store.StartPeriod(r.Context(), name, request.Deposit, request.Currency, user)
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the results of the agent %s reset by %s: a period from %s, a deposit of %.2f %s", name, user,
		period.StartedAt.UTC().Format(time.DateTime), period.Deposit, period.Currency)
	writeCreated(w, uiPeriod(period, nil))
}
