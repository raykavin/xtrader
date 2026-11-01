package strategies

import (
	"context"
	"time"
)

// The economic releases a strategy may trade around (see NewsBreakout): the
// scheduled times of the calendar, given to the strategy by the runner, from
// the news of the controller live and from a snapshot of the period in a
// backtest. Only what is known in advance travels: the time, the event and
// its importance, never the values released, so a backtest doesn't look
// ahead.

// ReleaseImportance is the importance of a release, as the calendar tells it.
type ReleaseImportance int

// The importances of a release, from none to high.
const (
	ReleaseNone ReleaseImportance = iota
	ReleaseLow
	ReleaseMedium
	ReleaseHigh
)

// Release is a scheduled economic release: its time (UTC), the currency of
// the event, its code (the MQL5 event_code, the stable key of an event) and
// name, its importance, and whether its time is exact (Timed): an all-day or
// tentative release has no time to trade around.
type Release struct {
	Time       time.Time
	Currency   string
	Code       string
	Name       string
	Importance ReleaseImportance
	Timed      bool
}

// Releases reads the releases of the currencies of pair (eg: USD for XAUUSDm)
// scheduled from from to to.
type Releases interface {
	Releases(ctx context.Context, pair string, from, to time.Time) ([]Release, error)
}

// ReleasesUser is a strategy trading around the releases: the runner gives it
// their source, see Releases.
type ReleasesUser interface {
	SetReleases(source Releases)
}
