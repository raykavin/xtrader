package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"
)

// The seasonality report measures the bias of the returns by time of day in
// candle files: what the seasonality strategy trades only where it shows,
// told before an agent trades it. The files are the CSVs of `xtrader
// download` or of the candle cache of the backtests of the controller
// (data/backtests/candles): a header with time (Unix seconds, the open of the
// candle, UTC), open, close, low and high.

// seasonCandle is a candle of a file: its open time and prices.
type seasonCandle struct {
	at                     time.Time
	open, high, low, close float64
}

// seasonWindow is a window of the day: its start, HH:MM in its zone, and its
// length.
type seasonWindow struct {
	label string
	start time.Duration // from the local midnight
	hold  time.Duration
	loc   *time.Location
}

// seasonStats are the returns of a window, one per day, in percent.
type seasonStats struct {
	Window string  `json:"window"`
	Days   int     `json:"days"`
	Mean   float64 `json:"mean_pct"`
	Stdev  float64 `json:"stdev_pct"`
	T      float64 `json:"t"`
	Up     float64 `json:"up_share"`
	Bias   string  `json:"bias"`
}

var seasonalityCommand = &cli.Command{
	Name:     "seasonality",
	HelpName: "seasonality",
	Usage: "Measure the bias of the returns by window of the day (and by hour) in candle files: " +
		"the mean, the t statistic and the share of up days",
	Flags: []cli.Flag{
		&cli.StringSliceFlag{Name: "csv", Usage: "candle file(s): time,open,close,low,high[,volume]", Required: true},
		&cli.StringFlag{
			Name:  "windows",
			Usage: "windows HH:MM/minutes@Zone, ';' between them",
			Value: "10:30/60@Europe/London;15:00/60@Europe/London;09:30/60@America/New_York",
		},
		&cli.StringFlag{Name: "zone", Usage: "the time zone of the table by hour", Value: "UTC"},
		&cli.Float64Flag{Name: "min-t", Usage: "the |t| a bias is told from", Value: 2},
		&cli.IntFlag{Name: "min-days", Usage: "the days a window needs to be told", Value: 20},
		&cli.TimestampFlag{Name: "from", Usage: "the first day, eg 2026-08-01", Layout: "2006-01-02"},
		&cli.TimestampFlag{Name: "to", Usage: "the day after the last one, eg 2026-11-01", Layout: "2006-01-02"},
		&cli.BoolFlag{Name: "json", Usage: "print JSON instead of tables"},
	},
	Action: func(c *cli.Context) error {
		windows, err := parseSeasonWindows(c.String("windows"))
		if err != nil {
			return err
		}
		zone, err := time.LoadLocation(c.String("zone"))
		if err != nil {
			return fmt.Errorf("invalid zone: %w", err)
		}
		var from, to time.Time
		if t := c.Timestamp("from"); t != nil {
			from = *t
		}
		if t := c.Timestamp("to"); t != nil {
			to = *t
		}
		type report struct {
			File    string        `json:"file"`
			Candles int           `json:"candles"`
			Windows []seasonStats `json:"windows"`
			Hours   []seasonStats `json:"hours"`
		}
		var reports []report
		for _, file := range c.StringSlice("csv") {
			candles, err := readSeasonCandles(file, from, to)
			if err != nil {
				return err
			}
			r := report{File: file, Candles: len(candles)}
			for _, w := range windows {
				r.Windows = append(r.Windows, seasonOf(w.label, windowReturns(candles, w),
					c.Float64("min-t"), c.Int("min-days")))
			}
			for h := 0; h < 24; h++ {
				w := seasonWindow{label: fmt.Sprintf("%02d:00 %s", h, zone), start: time.Duration(h) * time.Hour,
					hold: time.Hour, loc: zone}
				r.Hours = append(r.Hours, seasonOf(w.label, windowReturns(candles, w), c.Float64("min-t"),
					c.Int("min-days")))
			}
			reports = append(reports, r)
		}
		if c.Bool("json") {
			encoder := json.NewEncoder(c.App.Writer)
			encoder.SetIndent("", "  ")
			return encoder.Encode(reports)
		}
		for _, r := range reports {
			_, _ = fmt.Fprintf(c.App.Writer, "%s: %d candles\n\n", r.File, r.Candles)
			printSeason(c.App.Writer, "window", r.Windows)
			printSeason(c.App.Writer, "hour", r.Hours)
		}
		return nil
	},
}

// parseSeasonWindows parses windows HH:MM/minutes@Zone, ';' between them.
func parseSeasonWindows(value string) ([]seasonWindow, error) {
	var windows []seasonWindow
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		spec, zone, ok := strings.Cut(part, "@")
		start, minutes, ok2 := strings.Cut(spec, "/")
		if !ok || !ok2 {
			return nil, fmt.Errorf("invalid window %q: HH:MM/minutes@Zone", part)
		}
		clock, err := time.Parse("15:04", start)
		if err != nil {
			return nil, fmt.Errorf("invalid start of the window %q: %w", part, err)
		}
		hold, err := strconv.Atoi(minutes)
		if err != nil || hold <= 0 || hold > 24*60 {
			return nil, fmt.Errorf("invalid minutes of the window %q", part)
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return nil, fmt.Errorf("invalid zone of the window %q: %w", part, err)
		}
		windows = append(windows, seasonWindow{label: part,
			start: time.Duration(clock.Hour())*time.Hour + time.Duration(clock.Minute())*time.Minute,
			hold:  time.Duration(hold) * time.Minute, loc: loc})
	}
	if len(windows) == 0 {
		return nil, errors.New("no window")
	}
	return windows, nil
}

// readSeasonCandles reads the candles of file opened from from to to (zero:
// unbounded), in order of time.
func readSeasonCandles(file string, from, to time.Time) ([]seasonCandle, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	index := make(map[string]int)
	for i, name := range header {
		index[strings.ToLower(strings.TrimSpace(name))] = i
	}
	for _, name := range []string{"time", "open", "close", "low", "high"} {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("%s: no %s column", file, name)
		}
	}
	var candles []seasonCandle
	for {
		line, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		seconds, err := strconv.ParseInt(line[index["time"]], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid time %q", file, line[index["time"]])
		}
		at := time.Unix(seconds, 0).UTC()
		if !from.IsZero() && at.Before(from) || !to.IsZero() && !at.Before(to) {
			continue
		}
		var prices [4]float64
		for i, name := range []string{"open", "high", "low", "close"} {
			if prices[i], err = strconv.ParseFloat(line[index[name]], 64); err != nil {
				return nil, fmt.Errorf("%s: invalid %s %q", file, name, line[index[name]])
			}
		}
		candles = append(candles, seasonCandle{at: at, open: prices[0], high: prices[1], low: prices[2],
			close: prices[3]})
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].at.Before(candles[j].at) })
	if len(candles) < 2 {
		return nil, fmt.Errorf("%s: fewer than 2 candles", file)
	}
	return candles, nil
}

// windowReturns returns the return of the window w on each day it traded
// whole, in percent: from the open of its first candle to the close of its
// last one, the candles opened inside it, the first one at its start and the
// last one ending at its end (the step of the file).
func windowReturns(candles []seasonCandle, w seasonWindow) []float64 {
	step := candles[1].at.Sub(candles[0].at)
	for i := 2; i < len(candles); i++ {
		if d := candles[i].at.Sub(candles[i-1].at); d > 0 && d < step {
			step = d
		}
	}
	type span struct {
		first, last int
	}
	days := make(map[string]*span)
	var order []string
	for i, c := range candles {
		local := c.at.In(w.loc)
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, w.loc)
		start := midnight.Add(w.start)
		if c.at.Before(start) || !c.at.Before(start.Add(w.hold)) {
			continue
		}
		key := midnight.Format("2006-01-02")
		if s, ok := days[key]; ok {
			s.last = i
		} else {
			days[key] = &span{first: i, last: i}
			order = append(order, key)
		}
	}
	var returns []float64
	for _, key := range order {
		s := days[key]
		first, last := candles[s.first], candles[s.last]
		local := first.at.In(w.loc)
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, w.loc).Add(w.start)
		if !first.at.Equal(start) || !last.at.Add(step).Equal(start.Add(w.hold)) || first.open <= 0 {
			continue // not traded whole that day
		}
		returns = append(returns, (last.close/first.open-1)*100)
	}
	return returns
}

// seasonOf returns the statistics of the returns of a window: a bias told
// when |t| reaches minT on at least minDays days.
func seasonOf(label string, returns []float64, minT float64, minDays int) seasonStats {
	s := seasonStats{Window: label, Days: len(returns), Bias: "-"}
	if len(returns) < 2 {
		return s
	}
	var sum, up float64
	for _, r := range returns {
		sum += r
		if r > 0 {
			up++
		}
	}
	n := float64(len(returns))
	s.Mean, s.Up = sum/n, up/n
	var squares float64
	for _, r := range returns {
		squares += (r - s.Mean) * (r - s.Mean)
	}
	s.Stdev = math.Sqrt(squares / (n - 1))
	if s.Stdev > 0 {
		s.T = s.Mean / (s.Stdev / math.Sqrt(n))
	}
	switch {
	case len(returns) < minDays:
		s.Bias = "poucos dias"
	case s.T >= minT:
		s.Bias = "alta"
	case s.T <= -minT:
		s.Bias = "baixa"
	}
	return s
}

func printSeason(w io.Writer, title string, stats []seasonStats) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "%s\tdays\tmean %%\tstdev %%\tt\tup\tbias\n", title)
	for _, s := range stats {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%+.4f\t%.4f\t%+.2f\t%.0f%%\t%s\n", s.Window, s.Days, s.Mean, s.Stdev, s.T,
			s.Up*100, s.Bias)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
}
