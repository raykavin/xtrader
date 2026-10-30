package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
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
	case errors.Is(err, ErrMCPUnauthorized):
		return CalendarUnauthorized
	}
	return CalendarUnavailable
}

// NewsSwitch is the calendar of the news of an agent, switched for all the
// agents from the dashboard: off, or without the token of the MCP server, it
// asks nothing to the server and its reads fail with why (ErrNewsDisabled,
// ErrNewsNoToken); on, it reads the calendar of the MCP server with the token
// of the settings, replaced while the agent runs (see Set).
type NewsSwitch struct {
	calendar *MCPCalendar

	mu      sync.RWMutex
	enabled bool
	token   bool // whether a token is set
}

// NewNewsSwitch returns the switch of calendar, on or off, with token.
func NewNewsSwitch(calendar *MCPCalendar, enabled bool, token string) *NewsSwitch {
	n := &NewsSwitch{calendar: calendar}
	n.Set(enabled, token)
	return n
}

// Set switches the news on or off and replaces the token of the MCP server,
// at once for the next read. It reports what changed, for the logs: never the
// token itself.
func (n *NewsSwitch) Set(enabled bool, token string) (switched, tokenChanged bool) {
	token = strings.TrimSpace(token)
	tokenChanged = n.calendar.SetToken(token)
	n.mu.Lock()
	defer n.mu.Unlock()
	switched = n.enabled != enabled
	n.enabled, n.token = enabled, token != ""
	return switched, tokenChanged
}

// Enabled reports whether the news are on.
func (n *NewsSwitch) Enabled() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.enabled
}

// Events reads the releases of the calendar while the news are on and a token
// is set, see Calendar.
func (n *NewsSwitch) Events(ctx context.Context, currencies []string, from, to time.Time) ([]Event, error) {
	n.mu.RLock()
	enabled, token := n.enabled, n.token
	n.mu.RUnlock()
	switch {
	case !enabled:
		return nil, ErrNewsDisabled
	case !token:
		return nil, ErrNewsNoToken
	}
	return n.calendar.Events(ctx, currencies, from, to)
}

func (n *NewsSwitch) String() string {
	return n.calendar.String()
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

// SetNews switches the news of the calendar of the Trader on or off and
// replaces the token of its MCP server, when its calendar is a NewsSwitch:
// the settings of the news of the dashboard, applied while it runs. It
// reports what changed, see NewsSwitch.Set.
func (t *Trader) SetNews(enabled bool, token string) (switched, tokenChanged bool) {
	news, ok := t.cfg.Calendar.(*NewsSwitch)
	if !ok {
		return false, false
	}
	return news.Set(enabled, token)
}
