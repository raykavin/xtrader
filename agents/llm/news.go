package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/exchange"
)

// ErrNewsDisabled is the error of a read of the calendar while the news
// feature is off for all the agents: nothing was asked to the server.
var ErrNewsDisabled = errors.New("llm: the news feature is off")

// ErrNewsNoToken is the error of a read of the calendar without the token of
// the MCP server: nothing was asked to the server.
var ErrNewsNoToken = errors.New("llm: the token of the MCP server of the news is missing")

// The reasons a calendar couldn't be read, told to the model (see
// CalendarContext.Status) and in the logs: the news feature off, its token
// missing, the token refused by the server, or the server unavailable. In none
// of them is the absence of releases a calm market.
const (
	CalendarDisabled     = "disabled"
	CalendarMissingToken = "missing_token"
	CalendarUnauthorized = "unauthorized"
	CalendarUnavailable  = "unavailable"
)

// calendarStatus returns the reason of err, a read of the calendar failing,
// see CalendarDisabled.
func calendarStatus(err error) string {
	switch {
	case errors.Is(err, ErrNewsDisabled):
		return CalendarDisabled
	case errors.Is(err, ErrNewsNoToken):
		return CalendarMissingToken
	case errors.Is(err, ErrNewsNoServer):
		return CalendarMissingServer
	case errors.Is(err, ErrMCPUnauthorized):
		return CalendarUnauthorized
	}
	return CalendarUnavailable
}

// ErrNewsNoServer is the error of a read of the calendar without the address
// of the MCP server: nothing was asked.
var ErrNewsNoServer = errors.New("llm: the address of the MCP server of the news is missing")

// CalendarMissingServer is the reason of a calendar not read for want of the
// address of its MCP server, see CalendarDisabled.
const CalendarMissingServer = "missing_server"

// NewsConfig is the news of all the agents, set on the dashboard (the tab
// Notícias): on or off, the MCP server of the economic calendar and its token,
// and the window of the news block: no entry from Before before a release of
// at least Importance to After after it, both 0 for none.
type NewsConfig struct {
	Enabled       bool
	URL, Token    string
	Before, After time.Duration
	Importance    Importance
}

// NewsSwitch is the calendar of the news of an agent, switched for all the
// agents from the dashboard (see NewsConfig): off, without the address of its
// MCP server or its token, it asks nothing to the server and its reads fail
// with why (ErrNewsDisabled, ErrNewsNoServer, ErrNewsNoToken); on, it reads
// the calendar of the server, its address, its token and its window replaced
// while the agent runs (see Set).
type NewsSwitch struct {
	options []MCPOption

	mu       sync.RWMutex
	config   NewsConfig
	calendar *MCPCalendar // of config.URL, nil without one
}

// NewNewsSwitch returns the switch of the news of config, its calendar read
// with options, eg: the offset of the trade server.
func NewNewsSwitch(config NewsConfig, options ...MCPOption) *NewsSwitch {
	n := &NewsSwitch{options: options}
	n.Set(config)
	return n
}

// Set applies config at once for the next read: the calendar of another
// server made anew, the token of the same one replaced (its session dropped).
// It reports what changed, for the logs: never the token itself.
func (n *NewsSwitch) Set(config NewsConfig) (switched, changed bool) {
	config.URL, config.Token = strings.TrimSpace(config.URL), strings.TrimSpace(config.Token)
	n.mu.Lock()
	defer n.mu.Unlock()
	old := n.config
	switch {
	case config.URL == "":
		n.calendar = nil
	case n.calendar == nil || config.URL != old.URL:
		calendar, err := NewMCPCalendar(config.URL, config.Token, n.options...)
		if err != nil {
			n.calendar = nil
		} else {
			n.calendar = calendar
		}
	default:
		n.calendar.SetToken(config.Token)
	}
	n.config = config
	return old.Enabled != config.Enabled, old != config
}

// Enabled reports whether the news are on.
func (n *NewsSwitch) Enabled() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.config.Enabled
}

// Window returns the window of the news block, see NewsConfig.
func (n *NewsSwitch) Window() (before, after time.Duration, importance Importance) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.config.Before, n.config.After, n.config.Importance
}

// Events reads the releases of the calendar while the news are on with the
// address of its server and its token, see Calendar.
func (n *NewsSwitch) Events(ctx context.Context, currencies []string, from, to time.Time) ([]Event, error) {
	n.mu.RLock()
	config, calendar := n.config, n.calendar
	n.mu.RUnlock()
	switch {
	case !config.Enabled:
		return nil, ErrNewsDisabled
	case config.URL == "" || calendar == nil:
		return nil, ErrNewsNoServer
	case config.Token == "":
		return nil, ErrNewsNoToken
	}
	return calendar.Events(ctx, currencies, from, to)
}

func (n *NewsSwitch) String() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.calendar == nil {
		return "the news calendar without a server"
	}
	return n.calendar.String()
}

// Blocking returns the release of events blocking new entries at at in the
// window of the switch, if any, see NewsConfig.
func (n *NewsSwitch) Blocking(events []Event, at time.Time) (Event, bool) {
	before, after, importance := n.Window()
	if before == 0 && after == 0 {
		return Event{}, false
	}
	return newsBlock(events, at, before, after, importance)
}

// CalendarCurrencies returns the currencies of the calendar of pair, eg: USD
// for XAUUSD, USD and EUR for EURUSD, none for a crypto quoted in USDT.
func CalendarCurrencies(pair string) []string {
	return calendarCurrencies(exchange.SplitAssetQuote(pair))
}

// ProbeNews reads the calendar of the MCP server at url with token, the
// releases of USD in the next hour: nil when it answers, ErrNewsNoToken
// without a token, an error wrapping ErrMCPUnauthorized when it refuses the
// token, any other one when it is unavailable. For the test of the settings
// of the news on the dashboard.
func ProbeNews(ctx context.Context, url, token string) error {
	if strings.TrimSpace(token) == "" {
		return ErrNewsNoToken
	}
	calendar, err := NewMCPCalendar(url, token, withMCPRetry(0))
	if err != nil {
		return err
	}
	now := time.Now()
	_, err = calendar.Events(ctx, []string{"USD"}, now, now.Add(time.Hour))
	return err
}

// withMCPRetry sets how long a tool that failed waits to be called again.
func withMCPRetry(retry time.Duration) MCPOption {
	return func(c *MCPCalendar) { c.retry = retry }
}

// SetNews applies the news of the dashboard to the calendar of the Trader,
// when it is a NewsSwitch, while it runs: on or off, the server, its token and
// the window of the news block. It reports what changed, see NewsSwitch.Set.
func (t *Trader) SetNews(config NewsConfig) (switched, changed bool) {
	news, ok := t.cfg.Calendar.(*NewsSwitch)
	if !ok {
		return false, false
	}
	return news.Set(config)
}

// News returns the switch of the news of the Trader, nil when its calendar
// isn't one, see NewsSwitch.
func (t *Trader) News() *NewsSwitch {
	news, _ := t.cfg.Calendar.(*NewsSwitch)
	return news
}

// newsWindow returns the window of the news block: the one of the dashboard
// with a NewsSwitch, else the one of the config.
func (t *Trader) newsWindow() (before, after time.Duration, importance Importance) {
	if news, ok := t.cfg.Calendar.(*NewsSwitch); ok {
		return news.Window()
	}
	return t.cfg.NewsBlockBefore, t.cfg.NewsBlockAfter, t.cfg.NewsBlockImportance
}
