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

	"github.com/raykavin/xtrader/tools/log"
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
// published, and Forecast and Previous when the calendar has none. A release
// of a day rather than of a time (see TimeMode) has the start of its day, of
// the trade server, as its Time.
type Event struct {
	Time       time.Time
	TimeMode   TimeMode
	Currency   string
	Name       string
	Code       string
	Importance Importance
	Unit       string
	Actual     float64
	Forecast   float64
	Previous   float64
	dayStart   bool // Time the start of its day on the trade server, see Timed
}

// TimeMode is how the calendar knows the time of a release (time_mode of the
// events of MQL5): at a time, or only its day.
type TimeMode int

const (
	TimeExact     TimeMode = iota // published at its time, the default
	TimeAllDay                    // the event takes all day
	TimeNone                      // the source publishes no time
	TimeTentative                 // the day known, the time only once it happens
)

// timeModeOf reads the time_mode of an event of the MCP server of MetaTrader 5,
// the description of ENUM_CALENDAR_EVENT_TIMEMODE, eg: "Source publishes an
// exact time of an event", or its name, eg: CALENDAR_TIMEMODE_DATE. Anything
// else, a missing one included, is TimeExact, as it was before it was read.
func timeModeOf(value string) TimeMode {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(lower, "tentative") || strings.Contains(lower, "a day of an event"):
		return TimeTentative
	case strings.Contains(lower, "notime") || strings.Contains(lower, "no time"):
		return TimeNone
	case strings.Contains(lower, "all day") || lower == "calendar_timemode_date":
		return TimeAllDay
	}
	return TimeExact
}

func (m TimeMode) String() string {
	switch m {
	case TimeAllDay:
		return "all_day"
	case TimeNone:
		return "no_time"
	case TimeTentative:
		return "tentative"
	}
	return "exact"
}

// Timed reports whether the release is at its Time, rather than some time of
// its day: an event with an exact time, or a tentative one, or one without
// time, once the calendar gives its time (any but the start of the day).
func (e Event) Timed() bool {
	switch e.TimeMode {
	case TimeExact:
		return true
	case TimeAllDay:
		return false
	}
	return !e.dayStart
}

// end returns when the release is over: its Time, or the end of its day when
// it isn't Timed.
func (e Event) end() time.Time {
	if e.Timed() {
		return e.Time
	}
	return e.Time.Add(24 * time.Hour)
}

// within reports whether the release is between from and to: its time, or
// any time of its day when it isn't Timed.
func (e Event) within(from, to time.Time) bool {
	if e.Timed() {
		return !e.Time.Before(from) && !e.Time.After(to)
	}
	return !e.end().Before(from) && !e.Time.After(to)
}

// calendarEvent is an Event as the model sees it, relative to the time of the
// analysis, see CalendarContext.
type calendarEvent struct {
	Time       time.Time `json:"time"`
	Minutes    int       `json:"minutes_from_now"`    // negative for a release in the past
	TimeMode   string    `json:"time_mode,omitempty"` // all_day, no_time or tentative: some time of its day
	Currency   string    `json:"currency"`
	Name       string    `json:"event"`
	Importance string    `json:"importance"`
	Unit       string    `json:"unit,omitempty"`
	Actual     number    `json:"actual"` // null until it is published
	Forecast   number    `json:"forecast"`
	Previous   number    `json:"previous"`
}

// CalendarContext is the economic calendar of a decision: the releases around
// the time of the analysis (now, live, the candle in progress included; else
// the close of the candle just closed), oldest first. Available is false when
// the calendar couldn't be read, so the model doesn't take the silence for a
// calm market, and Status tells why: the news off, the token missing or
// refused, the server unavailable (see CalendarDisabled).
type CalendarContext struct {
	Available bool            `json:"available"`
	Status    string          `json:"status,omitempty"`
	Events    []calendarEvent `json:"events"`
}

// newsBlock returns the release of at least importance whose window, before
// minutes before it to after minutes after it, holds at, if any: new entries
// are refused inside it. A release without a time (see Event.Timed), whose
// Time is only the start of its day, blocks nothing.
func newsBlock(events []Event, at time.Time, before, after time.Duration, importance Importance) (Event, bool) {
	for _, event := range events {
		if event.Importance >= importance && event.Timed() && !at.Before(event.Time.Add(-before)) && !at.After(event.Time.Add(after)) {
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
		// a release of a day: its value hidden until the day is over, the
		// time it was published unknown
		actual := event.Actual
		if event.end().After(at) {
			actual = math.NaN()
		}
		mode := ""
		if !event.Timed() {
			mode = event.TimeMode.String()
		}
		cc.Events = append(cc.Events, calendarEvent{
			Time:       event.Time.UTC(),
			Minutes:    int(math.Round(event.Time.Sub(at).Minutes())),
			TimeMode:   mode,
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
	retry  time.Duration // before a tool that failed is called again, see call

	mtx     sync.Mutex
	events  map[string]mcpEvents // descriptions of the events, by currency
	days    map[string]mcpDay    // releases of every currency, by day of the server
	unknown map[string]time.Time // currencies the calendar doesn't know, not asked again until then
	empty   map[string]time.Time // currencies without any event read, logged until then
}

// mcpStatus is the status of the answer of a tool of the calendar: ok false,
// with its error, is a tool failing, as isError is.
type mcpStatus struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (s *mcpStatus) status() *mcpStatus { return s }

type mcpEvents struct {
	byID map[string]mcpEventInfo
	read time.Time
}

type mcpEventInfo struct {
	Name       string `json:"name"`
	Code       string `json:"event_code"`
	Importance string `json:"importance"`
	Unit       string `json:"unit"`
	TimeMode   string `json:"time_mode"`
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

// The refresh of the cached calendar: the descriptions of the events, the
// releases of the days not over yet, the ones of the days over (dropped from
// the cache then, a revision read again), and a currency the calendar doesn't
// know. A tool failing for another reason, eg: the terminal busy, is called
// again once after mcpRetry, then the read fails, the next one calling it
// again.
const (
	mcpEventsTTL  = 24 * time.Hour
	mcpDayTTL     = 5 * time.Minute
	mcpDayOverTTL = 24 * time.Hour
	mcpUnknownTTL = time.Hour
	mcpRetry      = 500 * time.Millisecond
)

// The releases of a range of time are read at most mcpValuesLimit at a time:
// a range the server truncates is read again in halves, down to
// mcpValuesMinSpan, a range still truncated failing the read rather than
// leaving releases out.
const (
	mcpValuesLimit   = 5000
	mcpValuesMinSpan = 15 * time.Minute
)

// mcpTimeLayout is the layout of the times of the server, in its time zone.
const mcpTimeLayout = "2006-01-02T15:04:05"

// errNoCurrencyEvents is a currency without any event in the calendar:
// unknown to it, or none read.
var errNoCurrencyEvents = errors.New("no event of the currency in the calendar")

// mcpInvalidParams is the code of the JSON-RPC error of a request whose
// arguments the server refuses: for the events of a currency, the currency.
const mcpInvalidParams = -32602

// mt5CalendarCurrencies are the currencies of the countries of the economic
// calendar of MetaTrader 5 (CalendarCountries of the MQL5 reference), ALL the
// one of the worldwide events. A call failing for another code, whatever its
// error, eg: for XTI of XTIUSD, refuses a currency the calendar doesn't know;
// for one of them, only the server refusing the argument does, see
// unknownCurrency.
var mt5CalendarCurrencies = map[string]bool{"ALL": true, "AUD": true, "BRL": true, "CAD": true, "CHF": true,
	"CNY": true, "EUR": true, "GBP": true, "HKD": true, "INR": true, "JPY": true, "KRW": true, "MXN": true,
	"NOK": true, "NZD": true, "SEK": true, "SGD": true, "USD": true, "ZAR": true}

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
		client:  newMCPClient(url, token, nil),
		now:     time.Now,
		retry:   mcpRetry,
		events:  make(map[string]mcpEvents),
		days:    make(map[string]mcpDay),
		unknown: make(map[string]time.Time),
		empty:   make(map[string]time.Time),
	}
	for _, option := range options {
		option(c)
	}
	return c, nil
}

func (c *MCPCalendar) String() string {
	return "MT5 MCP calendar at " + c.client.url
}

// SetToken replaces the token of the MCP server, eg: changed on the
// dashboard, the session of the former one dropped; the releases read stay,
// the same whatever the token.
func (c *MCPCalendar) SetToken(token string) bool {
	return c.client.setToken(token)
}

// Events returns the releases of the currencies between from and to, oldest
// first. A currency without any event in the calendar, eg: one it doesn't
// know, asked again an hour later, is skipped; none of the currencies with
// events is an error, as is any read failing: the calendar is then
// unavailable, never taken for a calm one.
func (c *MCPCalendar) Events(ctx context.Context, currencies []string, from, to time.Time) ([]Event, error) {
	if len(currencies) == 0 {
		return nil, nil
	}
	var events []Event
	var skipped []string
	infos := make(map[string]mcpEventInfo)
	currencyOf := make(map[string]string)
	for _, currency := range currencies {
		byID, err := c.descriptions(ctx, currency)
		if errors.Is(err, errNoCurrencyEvents) {
			skipped = append(skipped, err.Error())
			continue
		}
		if err != nil {
			return nil, err
		}
		for id, info := range byID {
			infos[id], currencyOf[id] = info, currency
		}
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("llm: the calendar has no event of the currencies: %s", strings.Join(skipped, "; "))
	}

	// a release read twice, eg: a server whose datetime_to is inclusive, once
	seen := make(map[string]bool)
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
			server, err := time.ParseInLocation(mcpTimeLayout, value.Time, time.UTC)
			if err != nil || seen[value.EventID+"@"+value.Time] {
				continue
			}
			previous := value.Previous
			if value.Revised != nil {
				previous = value.Revised
			}
			event := Event{
				Time:       server.Add(-c.offset), // server time into UTC
				TimeMode:   timeModeOf(info.TimeMode),
				Currency:   currencyOf[value.EventID],
				Name:       info.Name,
				Code:       info.Code,
				Importance: mcpImportance(info.Importance),
				Unit:       mcpUnit(info.Unit),
				Actual:     orNaN(value.Actual),
				Forecast:   orNaN(value.Forecast),
				Previous:   orNaN(previous),
				dayStart:   server.Equal(server.Truncate(24 * time.Hour)),
			}
			if !event.within(from, to) {
				continue
			}
			seen[value.EventID+"@"+value.Time] = true
			events = append(events, event)
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	return events, nil
}

// descriptions returns the events of a currency, by id, read again once a day.
// A currency the calendar doesn't know, asked again an hour later, or without
// any event, asked again on the next read, is errNoCurrencyEvents.
func (c *MCPCalendar) descriptions(ctx context.Context, currency string) (map[string]mcpEventInfo, error) {
	now := c.now()
	c.mtx.Lock()
	cached, ok := c.events[currency]
	until, unknown := c.unknown[currency]
	c.mtx.Unlock()
	if ok && now.Sub(cached.read) < mcpEventsTTL {
		return cached.byID, nil
	}
	if unknown && now.Before(until) {
		return nil, fmt.Errorf("%s: %w (unknown to it)", currency, errNoCurrencyEvents)
	}

	var answer struct {
		mcpStatus
		Events []struct {
			ID string `json:"id"`
			mcpEventInfo
		} `json:"events"`
	}
	err := c.call(ctx, "economic_calendar_list_events_by_currency", map[string]any{"currency_code": currency},
		&answer)
	switch {
	case unknownCurrency(err, currency):
		c.mtx.Lock()
		c.unknown[currency] = now.Add(mcpUnknownTTL)
		c.mtx.Unlock()
		log.Warnf("[LLM] the economic calendar: the currency %s skipped for %s, unknown to it: %v", currency,
			mcpUnknownTTL, err)
		return nil, fmt.Errorf("%s: %w (unknown to it)", currency, errNoCurrencyEvents)
	case err != nil:
		return nil, err // the server or the tool failing, not the currency: asked again on the next read
	case len(answer.Events) == 0:
		c.mtx.Lock()
		told := now.Before(c.empty[currency])
		if !told {
			c.empty[currency] = now.Add(mcpUnknownTTL)
		}
		c.mtx.Unlock()
		if !told {
			log.Warnf("[LLM] the economic calendar: the currency %s skipped, none of its events read: asked "+
				"again on the next reads", currency)
		}
		return nil, fmt.Errorf("%s: %w (none read)", currency, errNoCurrencyEvents)
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
// cached ones of a day over, read again after mcpDayOverTTL, else read again
// every mcpDayTTL. The days read more than mcpDayOverTTL ago leave the cache.
func (c *MCPCalendar) day(ctx context.Context, day time.Time) ([]mcpValue, error) {
	key := day.Format(time.DateOnly)
	now := c.now()
	over := !day.AddDate(0, 0, 1).Add(-c.offset).After(now.Add(-time.Hour)) // past, with an hour for late updates
	c.mtx.Lock()
	cached, ok := c.days[key]
	c.mtx.Unlock()
	if ok && (over && now.Sub(cached.read) < mcpDayOverTTL || now.Sub(cached.read) < mcpDayTTL) {
		return cached.values, nil
	}

	values, err := c.values(ctx, day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	c.mtx.Lock()
	for key, cached := range c.days {
		if now.Sub(cached.read) >= mcpDayOverTTL {
			delete(c.days, key)
		}
	}
	c.days[key] = mcpDay{values: values, read: now}
	c.mtx.Unlock()
	return values, nil
}

// values reads the releases from from to to, times of the server, to
// excluded: the ones the server gives outside the range left out, and a range
// it truncates read again in halves, see mcpValuesLimit.
func (c *MCPCalendar) values(ctx context.Context, from, to time.Time) ([]mcpValue, error) {
	var answer struct {
		mcpStatus
		Truncated bool       `json:"truncated"`
		Values    []mcpValue `json:"values"`
	}
	err := c.call(ctx, "economic_calendar_list_values", map[string]any{
		"datetime_from": from.Format(mcpTimeLayout),
		"datetime_to":   to.Format(mcpTimeLayout),
		"limit":         mcpValuesLimit,
	}, &answer)
	if err != nil {
		return nil, err
	}
	if answer.Truncated || len(answer.Values) >= mcpValuesLimit {
		span := to.Sub(from)
		if span <= mcpValuesMinSpan {
			return nil, fmt.Errorf("llm: the calendar truncated its releases from %s to %s (server time), "+
				"%d read", from.Format(mcpTimeLayout), to.Format(mcpTimeLayout), len(answer.Values))
		}
		middle := from.Add(span / 2)
		first, err := c.values(ctx, from, middle)
		if err != nil {
			return nil, err
		}
		second, err := c.values(ctx, middle, to)
		if err != nil {
			return nil, err
		}
		return append(first, second...), nil
	}

	values := answer.Values[:0]
	for _, value := range answer.Values {
		at, err := time.ParseInLocation(mcpTimeLayout, value.Time, time.UTC)
		if err == nil && (at.Before(from) || !at.Before(to)) {
			continue // eg: a datetime_to inclusive, the release of the next range
		}
		values = append(values, value)
	}
	return values, nil
}

// call calls a tool of the calendar, decoding its result into out. A tool
// failing (isError, or ok false) for another reason than an unknown currency
// (see unknownCurrency), eg: the terminal busy, is called again once after
// c.retry.
func (c *MCPCalendar) call(ctx context.Context, tool string, args map[string]any,
	out interface{ status() *mcpStatus }) error {

	currency, _ := args["currency_code"].(string)
	for attempt := 0; ; attempt++ {
		*out.status() = mcpStatus{}
		err := c.client.callTool(ctx, tool, args, out)
		if status := out.status(); err == nil && !status.OK {
			message := status.Error
			if message == "" {
				message = "ok false"
			}
			err = &mcpToolError{Tool: tool, Message: message}
		}
		var failed *mcpToolError
		if err == nil || attempt > 0 || !errors.As(err, &failed) || unknownCurrency(err, currency) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(c.retry):
		}
	}
}

// unknownCurrency reports whether err, of a call for the events of currency,
// refuses a currency the calendar doesn't know, rather than failing: any
// error for a code that isn't a currency of the calendar (see
// mt5CalendarCurrencies), eg: XTI; for one of them, only the server refusing
// the argument of the call (mcpInvalidParams, not of the opening of the
// session), deterministic. The message of a tool failing for one of them,
// whatever it says, eg: "currency EUR not available", is never taken for
// it: it may be transient, eg: the terminal syncing.
func unknownCurrency(err error, currency string) bool {
	if err == nil || currency == "" {
		return false
	}
	if !mt5CalendarCurrencies[strings.ToUpper(currency)] {
		return true
	}
	var opening *mcpOpenError
	var refused *mcpError
	return !errors.As(err, &opening) && errors.As(err, &refused) && refused.Code == mcpInvalidParams
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
