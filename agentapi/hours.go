package agentapi

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/raykavin/xtrader/ui"
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

// MaxTradingWindows is the number of windows the trading hours take at most.
const MaxTradingWindows = 24

// TradingWindows returns the window of windows (see TradingWindow, each of
// zone) at is inside (open, -1 for none), the window that opens next while in
// none (upcoming, -1 while open or without windows), and the next change: the
// end of the window open, else the start of the next one, today or tomorrow.
func TradingWindows(windows []ui.TradingWindow, at time.Time, zone *time.Location) (open, upcoming int,
	next time.Time) {
	open, upcoming = -1, -1
	for i, w := range windows {
		inside, change := TradingWindow(w.Start, w.End, at, zone)
		if inside {
			return i, -1, change
		}
		if upcoming < 0 || change.Before(next) {
			upcoming, next = i, change
		}
	}
	return open, upcoming, next
}

// WindowEnd returns when the window w, opened at openedAt, ends: its end on
// the day it opened (the next day for a window crossing midnight opened before
// it).
func WindowEnd(w ui.TradingWindow, openedAt time.Time, zone *time.Location) time.Time {
	local := openedAt.In(zone)
	minutes := clockMinutes(w.End)
	end := time.Date(local.Year(), local.Month(), local.Day(), minutes/60, minutes%60, 0, 0, zone)
	if !end.After(local) {
		end = end.AddDate(0, 0, 1)
	}
	return end
}

// HoursWindows returns the windows of trading hours: windows when there are,
// else the single window from start to end with flatten and cancelPending (the
// hours of before), none without it.
func HoursWindows(windows []ui.TradingWindow, start, end string, flatten, cancelPending bool) []ui.TradingWindow {
	if len(windows) > 0 {
		return windows
	}
	if start == "" && end == "" {
		return nil
	}
	return []ui.TradingWindow{{Start: start, End: end, Flatten: flatten, CancelPending: cancelPending}}
}

// SortedWindows returns windows in the order of the day, by their start.
func SortedWindows(windows []ui.TradingWindow) []ui.TradingWindow {
	sorted := slices.Clone(windows)
	slices.SortStableFunc(sorted, func(a, b ui.TradingWindow) int {
		return cmp.Compare(clockMinutes(a.Start), clockMinutes(b.Start))
	})
	return sorted
}

// WindowLabel returns the window w in words, eg: 08:00–10:00.
func WindowLabel(w ui.TradingWindow) string { return w.Start + "–" + w.End }

// WindowsProblem returns why windows can't be trading hours, in Portuguese,
// "" when they can: MaxTradingWindows at most, each start and end HH:MM, the
// start before the end (a window never crossing midnight), and none
// overlapping nor touching another (one then).
func WindowsProblem(windows []ui.TradingWindow) string {
	if len(windows) > MaxTradingWindows {
		return fmt.Sprintf("use no máximo %d janelas", MaxTradingWindows)
	}
	for i, w := range windows {
		switch {
		case !ValidClock(w.Start):
			return fmt.Sprintf("o início da janela %d deve ser um horário HH:MM, de 00:00 a 23:59", i+1)
		case !ValidClock(w.End):
			return fmt.Sprintf("o fim da janela %d deve ser um horário HH:MM, de 00:00 a 23:59", i+1)
		case clockMinutes(w.Start) >= clockMinutes(w.End):
			return fmt.Sprintf("a janela %s deve começar antes de terminar: uma janela não atravessa a meia-noite",
				WindowLabel(w))
		}
	}
	for _, w := range windows {
		for i, name := range w.Agents {
			switch {
			case strings.TrimSpace(name) == "":
				return fmt.Sprintf("a janela %s tem um agente sem nome", WindowLabel(w))
			case slices.Contains(w.Agents[:i], name):
				return fmt.Sprintf("a janela %s repete o agente %s", WindowLabel(w), name)
			}
		}
	}
	sorted := SortedWindows(windows)
	for i := 1; i < len(sorted); i++ {
		if clockMinutes(sorted[i].Start) <= clockMinutes(sorted[i-1].End) {
			return fmt.Sprintf("as janelas %s e %s se sobrepõem ou se encostam: junte-as numa só ou deixe um "+
				"intervalo entre elas", WindowLabel(sorted[i-1]), WindowLabel(sorted[i]))
		}
	}
	return ""
}

// WindowAllows reports whether the window w lets the agent of name open
// positions in it: any agent while it names none.
func WindowAllows(w ui.TradingWindow, name string) bool {
	return len(w.Agents) == 0 || slices.Contains(w.Agents, name)
}

// WindowsFor returns the windows of windows the agent of name may open
// positions in, without their agents.
func WindowsFor(windows []ui.TradingWindow, name string) []ui.TradingWindow {
	var kept []ui.TradingWindow
	for _, w := range windows {
		if WindowAllows(w, name) {
			w.Agents = nil
			kept = append(kept, w)
		}
	}
	return kept
}

// SameWindow reports whether a and b are the same window: the same times,
// agents (in any order) and actions at its end.
func SameWindow(a, b ui.TradingWindow) bool {
	if a.Start != b.Start || a.End != b.End || a.Flatten != b.Flatten || a.CancelPending != b.CancelPending ||
		len(a.Agents) != len(b.Agents) {
		return false
	}
	agents := slices.Sorted(slices.Values(a.Agents))
	return slices.Equal(agents, slices.Sorted(slices.Values(b.Agents)))
}
