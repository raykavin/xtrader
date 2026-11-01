package runner

import (
	"context"
	"slices"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/internal/entryfilter"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/agents/strategies"
	"github.com/raykavin/xtrader/strategy"
)

// The sources a strategy may read besides its candles, given by the runner
// when it asks for them: the candles of the symbols it doesn't trade (see
// entryfilter.CandleSource, eg: the reference of -intermarket-symbol) and the
// releases of the economic calendar (see strategies.ReleasesUser).

// candleUser is a strategy reading the candles of other symbols, eg: the entry
// filter of -intermarket-symbol.
type candleUser interface {
	SetCandleSource(source entryfilter.CandleSource)
}

// giveCandles gives s the source of the candles of the other symbols, when it
// reads them.
func giveCandles(s strategy.Strategy, source entryfilter.CandleSource) {
	if user, ok := s.(candleUser); ok {
		user.SetCandleSource(source)
	}
}

// giveReleases gives s the source of the releases of the calendar, when it
// trades around them.
func giveReleases(s strategy.Strategy, source strategies.Releases) {
	if user, ok := s.(strategies.ReleasesUser); ok {
		user.SetReleases(source)
	}
}

// usesReleases reports whether s trades around the releases of the calendar.
func usesReleases(s strategy.Strategy) bool {
	_, ok := s.(strategies.ReleasesUser)
	return ok
}

// newsReleases are the releases of the calendar of the news of the dashboard
// (the switch the news guard reads, no second client of the MCP server), the
// currencies of a pair as the guard has them (llm.CalendarCurrencies, eg: USD
// for XAUUSDm): their times, codes, names and importance, never a value
// released. The news off, or the calendar unreadable, fails the read.
type newsReleases struct {
	news *llm.NewsSwitch
}

func (n newsReleases) Releases(ctx context.Context, pair string, from, to time.Time) ([]strategies.Release,
	error) {
	currencies := llm.CalendarCurrencies(pair)
	if len(currencies) == 0 {
		return nil, nil
	}
	events, err := n.news.Events(ctx, currencies, from, to)
	if err != nil {
		return nil, err
	}
	releases := make([]strategies.Release, 0, len(events))
	for _, e := range events {
		releases = append(releases, strategies.Release{Time: e.Time.UTC(), Currency: e.Currency, Code: e.Code,
			Name: e.Name, Importance: strategies.ReleaseImportance(e.Importance), Timed: e.Timed()})
	}
	return releases, nil
}

// backtestReleases are the releases of a backtest, the snapshot of the
// calendar the controller read for its period (see
// agentapi.BacktestConfig.Releases), on the clock of the run (now, the
// simulated one): a release whose time the calendar knows only once released
// (Tentative) is told untimed at the start of its day (UTC, the trade server
// of Exness) until then, as the calendar told it live; read as the calendar
// reads its releases, the timed ones at their time between from and to, the
// others any time of their day.
type backtestReleases struct {
	byPair map[string][]agentapi.BacktestRelease
	now    func() time.Time
}

func (b backtestReleases) Releases(_ context.Context, pair string, from, to time.Time) ([]strategies.Release,
	error) {
	now := b.now()
	var releases []strategies.Release
	for _, r := range b.byPair[pair] {
		at, timed := time.Unix(r.Time, 0).UTC(), r.Timed
		if r.Tentative && now.Before(at) {
			at, timed = at.Truncate(24*time.Hour), false
		}
		end := at
		if !timed {
			end = at.Add(24 * time.Hour)
		}
		if end.Before(from) || at.After(to) {
			continue
		}
		releases = append(releases, strategies.Release{Time: at, Currency: r.Currency, Code: r.Code, Name: r.Name,
			Importance: strategies.ReleaseImportance(r.Importance), Timed: timed})
	}
	slices.SortStableFunc(releases, func(a, b strategies.Release) int { return a.Time.Compare(b.Time) })
	return releases, nil
}
