package agentapi

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// clock is a time of the trading hours, HH:MM.
var clock = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidClock reports whether text is a time of the trading hours, HH:MM from
// 00:00 to 23:59.
func ValidClock(text string) bool { return clock.MatchString(text) }

// clockMinutes returns the minutes of the day of a time HH:MM.
func clockMinutes(text string) int {
	hours, minutes, _ := strings.Cut(text, ":")
	h, _ := strconv.Atoi(hours)
	m, _ := strconv.Atoi(minutes)
	return h*60 + m
}

// TradingWindow returns whether at is inside the window of the trading hours
// from start to end (HH:MM of zone, crossing midnight when start is after end;
// the end itself outside), and its next change: the end while inside, the
// start else. The controller applies it to the agents, the backtests to their
// candles.
func TradingWindow(start, end string, at time.Time, zone *time.Location) (open bool, next time.Time) {
	from, to := clockMinutes(start), clockMinutes(end)
	local := at.In(zone)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
	when := func(days, minutes int) time.Time {
		day := midnight.AddDate(0, 0, days)
		return time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, zone)
	}
	now := local.Hour()*60 + local.Minute()
	if from < to {
		switch {
		case now < from:
			return false, when(0, from)
		case now < to:
			return true, when(0, to)
		}
		return false, when(1, from)
	}
	switch {
	case now >= from:
		return true, when(1, to)
	case now < to:
		return true, when(0, to)
	}
	return false, when(0, from)
}
