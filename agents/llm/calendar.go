package llm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Calendar is a source of the economic calendar: the releases of the
// currencies between from and to, with their importance.
type Calendar interface {
	Events(ctx context.Context, currencies []string, from, to time.Time) ([]Event, error)
}

// Importance is the importance of an event of the calendar.
type Importance int

const (
	ImportanceNone Importance = iota
	ImportanceLow
	ImportanceMedium
	ImportanceHigh
)

// ParseImportance reads low, medium or high, as the flags give them.
func ParseImportance(value string) (Importance, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return ImportanceLow, nil
	case "medium":
		return ImportanceMedium, nil
	case "high":
		return ImportanceHigh, nil
	}
	return ImportanceNone, fmt.Errorf("invalid importance %q: low, medium or high", value)
}

func (i Importance) String() string {
	switch i {
	case ImportanceLow:
		return "low"
	case ImportanceMedium:
		return "medium"
	case ImportanceHigh:
		return "high"
	}
	return "none"
}

// Event is a release of the economic calendar. Actual is NaN until it is
// published, and Forecast and Previous when the calendar has none.
type Event struct {
	Time       time.Time
	Currency   string
	Name       string
	Code       string
	Importance Importance
	Unit       string
	Actual     float64
	Forecast   float64
	Previous   float64
}

// calendarEvent is an Event as the model sees it, relative to the close of the
// candle just closed.
type calendarEvent struct {
	Time       time.Time `json:"time"`
	Minutes    int       `json:"minutes_from_now"` // negative for a release in the past
	Currency   string    `json:"currency"`
	Name       string    `json:"event"`
	Importance string    `json:"importance"`
	Unit       string    `json:"unit,omitempty"`
	Actual     number    `json:"actual"` // null until it is published
	Forecast   number    `json:"forecast"`
	Previous   number    `json:"previous"`
}

// CalendarContext is the economic calendar of a decision: the releases around
// the close of the candle just closed, oldest first. Available is false when
// the calendar couldn't be read, so the model doesn't take the silence for a
// calm market.
type CalendarContext struct {
	Available bool            `json:"available"`
	Events    []calendarEvent `json:"events"`
}

// newsBlock returns the release of at least importance whose window, before
// minutes before it to after minutes after it, holds at, if any: new entries
// are refused inside it.
func newsBlock(events []Event, at time.Time, before, after time.Duration, importance Importance) (Event, bool) {
	for _, event := range events {
		if event.Importance >= importance && !at.Before(event.Time.Add(-before)) && !at.After(event.Time.Add(after)) {
			return event, true
		}
	}
	return Event{}, false
}

// calendarContext returns the releases of at least importance in the window
// of the decision at at, the published values of the ones after at hidden:
// a backtest reads the calendar of its past, which knows them.
func calendarContext(events []Event, at time.Time, importance Importance, limit int) *CalendarContext {
	cc := &CalendarContext{Available: true, Events: []calendarEvent{}}
	for _, event := range events {
		if event.Importance < importance {
			continue
		}
		actual := event.Actual
		if event.Time.After(at) {
			actual = math.NaN()
		}
		cc.Events = append(cc.Events, calendarEvent{
			Time:       event.Time.UTC(),
			Minutes:    int(math.Round(event.Time.Sub(at).Minutes())),
			Currency:   event.Currency,
			Name:       event.Name,
			Importance: event.Importance.String(),
			Unit:       event.Unit,
			Actual:     number(actual),
			Forecast:   number(event.Forecast),
			Previous:   number(event.Previous),
		})
	}
	// the releases closest to the close first, when too many
	if limit > 0 && len(cc.Events) > limit {
		sort.SliceStable(cc.Events, func(i, j int) bool {
			return abs(cc.Events[i].Minutes) < abs(cc.Events[j].Minutes)
		})
		cc.Events = cc.Events[:limit]
		sort.SliceStable(cc.Events, func(i, j int) bool { return cc.Events[i].Time.Before(cc.Events[j].Time) })
	}
	return cc
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// calendarCurrencies returns the currencies of the calendar of a pair: its
// quote, then its base, eg: USD and EUR for EURUSD, only USD for XAUUSD,
// whose base isn't a currency of the calendar (the source skips those).
func calendarCurrencies(asset, quote string) []string {
	var currencies []string
	for _, code := range []string{quote, asset} {
		code = strings.ToUpper(strings.TrimSpace(code))
		if len(code) == 3 && code != "XAU" && code != "XAG" && code != "BTC" && code != "ETH" {
			currencies = append(currencies, code)
		}
	}
	return currencies
}

// MCPCalendar reads the economic calendar of a MetaTrader 5 terminal from its
// MCP server, calling only the read-only tools of the calendar (see
// mcpAllowedTools). The descriptions of the events of each currency are read
// once a day, and the releases a day at a time: the past days once, the
// current ones again every few minutes, so a backtest reads each day once.
type MCPCalendar struct {
	client *mcpClient
	offset time.Duration // of the time of the trade server from UTC
	now    func() time.Time

	mtx         sync.Mutex
	events      map[string]mcpEvents // descriptions of the events, by currency
	days        map[string]mcpDay    // releases of every currency, by day of the server
	unavailable map[string]time.Time // currencies the calendar refused, until then
}

type mcpEvents struct {
	byID map[string]mcpEventInfo
	read time.Time
}

type mcpEventInfo struct {
	Name       string `json:"name"`
	Code       string `json:"event_code"`
	Importance string `json:"importance"`
	Unit       string `json:"unit"`
}

type mcpDay struct {
	values []mcpValue
	read   time.Time
}

type mcpValue struct {
	EventID  string   `json:"event_id"`
	Time     string   `json:"time"`
	Actual   *float64 `json:"actual_value"`
	Forecast *float64 `json:"forecast_value"`
	Previous *float64 `json:"prev_value"`
	Revised  *float64 `json:"revised_prev_value"`
}

// The refresh of the cached calendar: the descriptions of the events, and the
// releases of the days not over yet.
const (
	mcpEventsTTL = 24 * time.Hour
	mcpDayTTL    = 5 * time.Minute
)

// MCPOption configures an MCPCalendar.
type MCPOption func(*MCPCalendar)

// WithMCPServerOffset sets how far the time of the trade server is from UTC,
// the times the calendar reads and writes, eg: +2h. It is 0 by default, as on
// Exness.
func WithMCPServerOffset(offset time.Duration) MCPOption {
	return func(c *MCPCalendar) { c.offset = offset }
}

// WithMCPHTTPClient sends the requests with client.
func WithMCPHTTPClient(client *http.Client) MCPOption {
	return func(c *MCPCalendar) { c.client.client = client }
}

// NewMCPCalendar returns the calendar of the MCP server of a MetaTrader 5
// terminal at url, eg: http://127.0.0.1:22346/mcp, with the token of its
// settings. It connects on the first read.
func NewMCPCalendar(url, token string, options ...MCPOption) (*MCPCalendar, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("llm: the URL of the MCP server is required")
	}
	c := &MCPCalendar{
		client:      newMCPClient(url, token, nil),
		now:         time.Now,
		events:      make(map[string]mcpEvents),
		days:        make(map[string]mcpDay),
		unavailable: make(map[string]time.Time),
	}
	for _, option := range options {
		option(c)
	}
	return c, nil
}

func (c *MCPCalendar) String() string {
	return "MT5 MCP calendar at " + c.client.url
}

// Events returns the releases of the currencies between from and to, oldest
// first. A currency the calendar doesn't know is skipped, and retried later.
func (c *MCPCalendar) Events(ctx context.Context, currencies []string, from, to time.Time) ([]Event, error) {
	var events []Event
	infos := make(map[string]mcpEventInfo)
	currencyOf := make(map[string]string)
	for _, currency := range currencies {
		byID, err := c.descriptions(ctx, currency)
		if err != nil {
			return nil, err
		}
		for id, info := range byID {
			infos[id], currencyOf[id] = info, currency
		}
	}
	if len(infos) == 0 {
		return nil, nil
	}

	for day := c.serverDay(from); !day.After(c.serverDay(to)); day = day.AddDate(0, 0, 1) {
		values, err := c.day(ctx, day)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			info, ok := infos[value.EventID]
			if !ok {
				continue
			}
			at, err := time.ParseInLocation("2006-01-02T15:04:05", value.Time, time.UTC)
			if err != nil {
				continue
			}
			at = at.Add(-c.offset) // server time into UTC
			if at.Before(from) || at.After(to) {
				continue
			}
			previous := value.Previous
			if value.Revised != nil {
				previous = value.Revised
			}
			events = append(events, Event{
				Time:       at,
				Currency:   currencyOf[value.EventID],
				Name:       info.Name,
				Code:       info.Code,
				Importance: mcpImportance(info.Importance),
				Unit:       mcpUnit(info.Unit),
				Actual:     orNaN(value.Actual),
				Forecast:   orNaN(value.Forecast),
				Previous:   orNaN(previous),
			})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	return events, nil
}

// descriptions returns the events of a currency, by id, read again once a day.
// A currency the calendar refuses has none, and is asked again an hour later.
func (c *MCPCalendar) descriptions(ctx context.Context, currency string) (map[string]mcpEventInfo, error) {
	now := c.now()
	c.mtx.Lock()
	cached, ok := c.events[currency]
	until, skipped := c.unavailable[currency]
	c.mtx.Unlock()
	if ok && now.Sub(cached.read) < mcpEventsTTL {
		return cached.byID, nil
	}
	if skipped && now.Before(until) {
		return nil, nil
	}

	var answer struct {
		OK     bool `json:"ok"`
		Events []struct {
			ID string `json:"id"`
			mcpEventInfo
		} `json:"events"`
	}
	err := c.client.callTool(ctx, "economic_calendar_list_events_by_currency",
		map[string]any{"currency_code": currency}, &answer)
	var refused *mcpToolError
	switch {
	case err != nil && !errors.As(err, &refused):
		return nil, err // the server, not the currency
	case err != nil || !answer.OK:
		c.mtx.Lock()
		c.unavailable[currency] = now.Add(time.Hour)
		c.mtx.Unlock()
		return nil, nil
	}

	byID := make(map[string]mcpEventInfo, len(answer.Events))
	for _, event := range answer.Events {
		byID[event.ID] = event.mcpEventInfo
	}
	c.mtx.Lock()
	c.events[currency] = mcpEvents{byID: byID, read: now}
	c.mtx.Unlock()
	return byID, nil
}

// day returns the releases of a day of the server, of every currency: the
// cached ones of a day over, else read again every mcpDayTTL.
func (c *MCPCalendar) day(ctx context.Context, day time.Time) ([]mcpValue, error) {
	key := day.Format(time.DateOnly)
	now := c.now()
	over := !day.AddDate(0, 0, 1).Add(-c.offset).After(now.Add(-time.Hour)) // past, with an hour for late updates
	c.mtx.Lock()
	cached, ok := c.days[key]
	c.mtx.Unlock()
	if ok && (over || now.Sub(cached.read) < mcpDayTTL) {
		return cached.values, nil
	}

	var answer struct {
		OK        bool       `json:"ok"`
		Truncated bool       `json:"truncated"`
		Values    []mcpValue `json:"values"`
	}
	err := c.client.callTool(ctx, "economic_calendar_list_values", map[string]any{
		"datetime_from": day.Format("2006-01-02T15:04:05"),
		"datetime_to":   day.AddDate(0, 0, 1).Format("2006-01-02T15:04:05"),
		"limit":         5000,
	}, &answer)
	if err != nil {
		return nil, err
	}
	c.mtx.Lock()
	c.days[key] = mcpDay{values: answer.Values, read: now}
	c.mtx.Unlock()
	return answer.Values, nil
}

// serverDay returns the day of the server holding t.
func (c *MCPCalendar) serverDay(t time.Time) time.Time {
	server := t.UTC().Add(c.offset)
	return time.Date(server.Year(), server.Month(), server.Day(), 0, 0, 0, 0, time.UTC)
}

func mcpImportance(value string) Importance {
	switch {
	case strings.HasPrefix(value, "High"):
		return ImportanceHigh
	case strings.HasPrefix(value, "Medium"):
		return ImportanceMedium
	case strings.HasPrefix(value, "Low"):
		return ImportanceLow
	}
	return ImportanceNone
}

func mcpUnit(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch {
	case lower == "percentage":
		return "%"
	case lower == "" || lower == "none" || strings.HasSuffix(lower, "not set"):
		return "" // eg: Measurement unit is not set
	}
	return lower
}

func orNaN(value *float64) float64 {
	if value == nil {
		return math.NaN()
	}
	return *value
}

// timeframeDuration returns the period of a timeframe, eg: 5m, 1h, 1d, 1w.
func timeframeDuration(timeframe string) (time.Duration, error) {
	if len(timeframe) >= 2 {
		n, err := strconv.Atoi(timeframe[:len(timeframe)-1])
		if err == nil && n > 0 {
			switch timeframe[len(timeframe)-1] {
			case 'm':
				return time.Duration(n) * time.Minute, nil
			case 'h':
				return time.Duration(n) * time.Hour, nil
			case 'd':
				return time.Duration(n) * 24 * time.Hour, nil
			case 'w':
				return time.Duration(n) * 7 * 24 * time.Hour, nil
			}
		}
	}
	return 0, fmt.Errorf("invalid timeframe %q", timeframe)
}
