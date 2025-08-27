package llm

import (
	"fmt"
	"math"
	"time"
	_ "time/tzdata" // the zones of the sessions, whatever the host has

	"github.com/raykavin/xtrader/model"
)

// Session is the time of the close of the candle for the market: its weekday
// and hour in UTC, the sessions open, the next one to open, whether it is a
// weekend (thin crypto markets, FX and indexes closed), and the range of the
// Asian session of the day (00:00 to 07:00 UTC) when the candles cover it.
type Session struct {
	Weekday  string   `json:"weekday"`
	HourUTC  int      `json:"hour_utc"`
	Open     []string `json:"sessions_open"`
	NextOpen string   `json:"next_session_open"`
	Weekend  bool     `json:"weekend"`
	AsiaHigh number   `json:"asia_high"`
	AsiaLow  number   `json:"asia_low"`
}

// marketSession is a trading session: its hours in the time of its city, on
// its weekdays.
type marketSession struct {
	name        string
	zone        string
	open, close int // hours
	location    *time.Location
}

// marketSessions are the sessions of the markets, their daylight saving times
// followed through their zones.
var marketSessions = func() []marketSession {
	sessions := []marketSession{
		{name: "Sydney", zone: "Australia/Sydney", open: 8, close: 17},
		{name: "Tokyo", zone: "Asia/Tokyo", open: 9, close: 18},
		{name: "London", zone: "Europe/London", open: 8, close: 17},
		{name: "New York", zone: "America/New_York", open: 8, close: 17},
	}
	for i := range sessions {
		location, err := time.LoadLocation(sessions[i].zone)
		if err != nil {
			location = time.UTC
		}
		sessions[i].location = location
	}
	return sessions
}()

// isOpen tells whether the session is open at at: a weekday of its city,
// within its hours.
func (s marketSession) isOpen(at time.Time) bool {
	local := at.In(s.location)
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
		return false
	}
	return local.Hour() >= s.open && local.Hour() < s.close
}

// nextOpen returns when the session opens next after at.
func (s marketSession) nextOpen(at time.Time) time.Time {
	local := at.In(s.location)
	for day := 0; day <= 7; day++ {
		d := local.AddDate(0, 0, day)
		opening := time.Date(d.Year(), d.Month(), d.Day(), s.open, 0, 0, 0, s.location)
		if opening.After(at) && opening.Weekday() != time.Saturday && opening.Weekday() != time.Sunday {
			return opening
		}
	}
	return time.Time{}
}

// session returns the session of at, the close of the candle, with the Asian
// range of its day from the candles of df when its timeframe is an hour at
// most.
func (t *Trader) session(df *model.Dataframe, at time.Time) *Session {
	at = at.UTC()
	nan := number(math.NaN())
	session := &Session{Weekday: at.Weekday().String(), HourUTC: at.Hour(), Open: []string{}, AsiaHigh: nan,
		AsiaLow: nan, Weekend: at.Weekday() == time.Saturday || at.Weekday() == time.Sunday}

	var next time.Time
	for _, s := range marketSessions {
		if s.isOpen(at) {
			session.Open = append(session.Open, s.name)
		}
		if opening := s.nextOpen(at); !opening.IsZero() && (next.IsZero() || opening.Before(next)) {
			next = opening
			session.NextOpen = fmt.Sprintf("%s in %s", s.name, describeWait(opening.Sub(at)))
		}
	}

	if t.period > 0 && t.period <= time.Hour {
		start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		end := start.Add(7 * time.Hour)
		high, low := math.Inf(-1), math.Inf(1)
		for i := range df.Close {
			if i < len(df.Time) && i < len(df.High) && i < len(df.Low) && !df.Time[i].Before(start) &&
				df.Time[i].Before(end) {
				high, low = math.Max(high, df.High[i]), math.Min(low, df.Low[i])
			}
		}
		if !math.IsInf(high, 0) {
			session.AsiaHigh, session.AsiaLow = number(high), number(low)
		}
	}
	return session
}

// describeWait writes a wait in hours and minutes, eg: 2 h 15 min.
func describeWait(wait time.Duration) string {
	minutes := int(wait.Round(time.Minute).Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%d h", minutes/60)
	}
	return fmt.Sprintf("%d h %d min", minutes/60, minutes%60)
}
