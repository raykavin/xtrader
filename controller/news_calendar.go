package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/ui"
)

// calendarSource reads the economic calendar of the news: an llm.NewsSwitch,
// its settings the ones of Configurations › Notícias, set before each read.
type calendarSource interface {
	Set(config llm.NewsConfig) (switched, changed bool)
	Events(ctx context.Context, currencies []string, from, to time.Time) ([]llm.Event, error)
}

// newsCalendarTTL is how long a read of the calendar serves the card of the
// news of every tab: the card reads it again every minute.
const newsCalendarTTL = time.Minute

// newsCalendarCached bounds the reads kept, by period and currencies: past
// it, they are all dropped.
const newsCalendarCached = 64

// newsCalendar serves the economic calendar to the card of the news of the
// dashboard (GET /api/news/calendar), read from the MCP server of the news of
// the agents with its token, each read kept for newsCalendarTTL.
type newsCalendar struct {
	source calendarSource
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]cachedCalendar
}

type cachedCalendar struct {
	at       time.Time
	response ui.NewsCalendarResponse
}

func newNewsCalendar() *newsCalendar {
	return &newsCalendar{source: llm.NewNewsSwitch(llm.NewsConfig{}), now: time.Now,
		cache: map[string]cachedCalendar{}}
}

// newsConfigOf returns the news of the agents as the calendar takes them, as
// the agents read them (see newsguard.ConfigOf, internal to the agents).
func newsConfigOf(n *agentapi.News) llm.NewsConfig {
	if n == nil {
		return llm.NewsConfig{}
	}
	importance, err := llm.ParseImportance(n.BlockImportance)
	if err != nil {
		importance = llm.ImportanceHigh
	}
	return llm.NewsConfig{Enabled: n.Enabled, URL: n.URL, Token: n.Token,
		Before: time.Duration(n.BlockBefore) * time.Minute, After: time.Duration(n.BlockAfter) * time.Minute,
		Importance: importance}
}

// calendarStatusOf returns why the calendar wasn't read, see
// ui.NewsCalendarResponse.
func calendarStatusOf(err error) string {
	switch {
	case errors.Is(err, llm.ErrNewsDisabled):
		return llm.CalendarDisabled
	case errors.Is(err, llm.ErrNewsNoServer):
		return llm.CalendarMissingServer
	case errors.Is(err, llm.ErrNewsNoToken):
		return llm.CalendarMissingToken
	case errors.Is(err, llm.ErrMCPUnauthorized):
		return llm.CalendarUnauthorized
	}
	return llm.CalendarUnavailable
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// calendarQuery returns the period and the currencies of the request, see
// ui.NewsCalendarResponse, or why they are refused.
func calendarQuery(r *http.Request) (from, to time.Time, currencies []string, problem string) {
	q := r.URL.Query()
	start, err1 := strconv.ParseInt(q.Get("from"), 10, 64)
	end, err2 := strconv.ParseInt(q.Get("to"), 10, 64)
	switch {
	case err1 != nil || err2 != nil || start <= 0 || end <= start:
		return from, to, nil, "Informe o período: from e to em segundos Unix, to depois de from."
	case end-start > int64(ui.MaxNewsCalendarDays)*86400:
		return from, to, nil, fmt.Sprintf("O período das notícias vai até %d dias.", ui.MaxNewsCalendarDays)
	}
	for _, code := range strings.Split(q.Get("currencies"), ",") {
		code = strings.ToUpper(strings.TrimSpace(code))
		if code == "" || slices.Contains(currencies, code) {
			continue
		}
		if !currencyCode.MatchString(code) {
			return from, to, nil, fmt.Sprintf("Moeda inválida: %q (use o código de 3 letras, ex.: USD).", code)
		}
		currencies = append(currencies, code)
	}
	switch {
	case len(currencies) == 0:
		return from, to, nil, "Informe as moedas, ex.: currencies=USD,EUR."
	case len(currencies) > ui.MaxNewsCalendarCurrencies:
		return from, to, nil, fmt.Sprintf("Use no máximo %d moedas.", ui.MaxNewsCalendarCurrencies)
	}
	return time.Unix(start, 0), time.Unix(end, 0), currencies, ""
}

// valueOf returns v, nil for none (NaN, the calendar without it).
func valueOf(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// calendarEvents returns the releases of events from from until before to, as
// the card shows them (a release of a day when its day overlaps the period),
// oldest first: blocking the entries of the agents when config blocks them.
func calendarEvents(events []llm.Event, from, to time.Time, config llm.NewsConfig) []ui.NewsEvent {
	blocks := config.Enabled && (config.Before > 0 || config.After > 0)
	list := make([]ui.NewsEvent, 0, len(events))
	for _, e := range events {
		timed := e.Timed()
		end := e.Time
		if !timed {
			end = e.Time.Add(24 * time.Hour)
		}
		if (timed && (e.Time.Before(from) || !e.Time.Before(to))) || (!timed && (!e.Time.Before(to) || !end.After(from))) {
			continue
		}
		list = append(list, ui.NewsEvent{At: e.Time.Unix(), Timed: timed, Currency: e.Currency, Name: e.Name,
			Code: e.Code, Importance: e.Importance.String(), Unit: e.Unit, Actual: valueOf(e.Actual),
			Forecast: valueOf(e.Forecast), Previous: valueOf(e.Previous),
			Blocks: blocks && e.Importance >= config.Importance})
	}
	slices.SortStableFunc(list, func(a, b ui.NewsEvent) int {
		switch {
		case a.At != b.At:
			return int(a.At - b.At)
		case a.Currency != b.Currency:
			return strings.Compare(a.Currency, b.Currency)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return list
}

// read returns the calendar of the period and the currencies with config, the
// one read less than newsCalendarTTL ago when there is one, the settings
// unchanged since.
func (n *newsCalendar) read(ctx context.Context, from, to time.Time, currencies []string,
	config llm.NewsConfig) ui.NewsCalendarResponse {
	now := n.now()
	key := fmt.Sprintf("%d-%d-%s", from.Unix(), to.Unix(), strings.Join(currencies, ","))
	n.mu.Lock()
	if _, changed := n.source.Set(config); changed {
		clear(n.cache) // the news switched, or their server, their token or their window changed
	}
	if cached, ok := n.cache[key]; ok && now.Sub(cached.at) < newsCalendarTTL {
		n.mu.Unlock()
		return cached.response
	}
	n.mu.Unlock()

	response := ui.NewsCalendarResponse{From: from.Unix(), To: to.Unix(), Currencies: currencies, Status: "ok",
		Events: []ui.NewsEvent{}, At: now.Unix()}
	if config.Enabled && (config.Before > 0 || config.After > 0) {
		response.Window = &ui.NewsWindow{Before: int(config.Before / time.Minute),
			After: int(config.After / time.Minute), Importance: config.Importance.String()}
	}
	events, err := n.source.Events(ctx, currencies, from, to)
	if err != nil {
		response.Status, response.Detail = calendarStatusOf(err), err.Error()
		if config.Token != "" {
			response.Detail = strings.ReplaceAll(response.Detail, config.Token, "[redacted]")
		}
		if response.Status == llm.CalendarUnavailable || response.Status == llm.CalendarUnauthorized {
			log.Warnf("ui: the economic calendar of the news card: %s", response.Detail)
		}
	} else {
		response.Events = calendarEvents(events, from, to, config)
	}
	if ctx.Err() != nil {
		return response // the request gone: its answer not kept
	}
	n.mu.Lock()
	if len(n.cache) >= newsCalendarCached {
		clear(n.cache)
	}
	n.cache[key] = cachedCalendar{at: now, response: response}
	n.mu.Unlock()
	return response
}

// handleNewsCalendar answers the economic calendar of the period and the
// currencies asked, see ui.NewsCalendarResponse: why it wasn't read in its
// status, never an error of the request for it.
func (s *settings) handleNewsCalendar(w http.ResponseWriter, r *http.Request) {
	from, to, currencies, problem := calendarQuery(r)
	if problem != "" {
		writeJSONError(w, http.StatusBadRequest, problem)
		return
	}
	news, err := newsOf(r.Context(), s.store)
	if err != nil {
		s.fail(w, err)
		return
	}
	response := s.calendar.read(r.Context(), from, to, currencies, newsConfigOf(news))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
}
