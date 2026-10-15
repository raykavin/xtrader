package controller

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/ui"
)

// closedProfitTTL is how long a read of the closed profit of a range answers
// the dashboards before the bridges are read again, see GET
// /api/positions/closed.
var closedProfitTTL = 10 * time.Second

// closedLookback is how long before a range the deals are read for the
// entries of the positions closed in it: the agent of a position is the one
// of the magic number of its entry, a position closed outside the bot (by
// hand) leaving its exit without it.
var closedLookback = 30 * 24 * time.Hour

// maxClosedRange bounds a range of the closed profit: the 400 days of a
// custom period of the dashboard, a day more for a change of the clock of the
// browser, eg: daylight saving time.
const maxClosedRange = 401 * 24 * time.Hour

// The entries of the deals of MT5 (ENUM_DEAL_ENTRY), and the types of the
// trades (ENUM_DEAL_TYPE), the others being the operations of the balance, eg:
// a deposit.
const (
	dealEntryIn    = 0
	dealEntryOut   = 1
	dealEntryOutBy = 3
	dealTypeBuy    = 0
	dealTypeSell   = 1
)

// bridgeDeal is a deal of an account, of GET /deals: its profit, swap,
// commission and fee in the currency of the account.
type bridgeDeal struct {
	Ticket     int64   `json:"ticket"`
	Time       int64   `json:"time"`
	TimeMsc    int64   `json:"time_msc"`
	Type       int     `json:"type"`
	Entry      int     `json:"entry"`
	Magic      int64   `json:"magic"`
	PositionID int64   `json:"position_id"`
	Commission float64 `json:"commission"`
	Swap       float64 `json:"swap"`
	Profit     float64 `json:"profit"`
	Fee        float64 `json:"fee"`
	Symbol     string  `json:"symbol"`
}

// at returns the time of the deal, the one of the server of the account,
// UTC.
func (d bridgeDeal) at() time.Time {
	if d.TimeMsc > 0 {
		return time.UnixMilli(d.TimeMsc)
	}
	return time.Unix(d.Time, 0)
}

// closedRange is a range of the closed profit: from, until before to.
type closedRange struct{ from, to time.Time }

// closedRead is a read of the closed profit of a range, see closedProfits.
type closedRead struct {
	response *ui.ClosedProfitResponse
	at       time.Time
	reading  chan struct{} // closed when the read in progress ends, nil without one
}

// closedProfits reads the closed profit of the agents in a range from the
// deals of their MT5 accounts, from the bridges the open positions are read
// from (see openPositions), for the card of the dashboard: of every agent
// trading live, running or not, as the accounts booked it, in the currency of
// each account.
type closedProfits struct {
	positions *openPositions

	mu    sync.Mutex
	reads map[closedRange]*closedRead
}

func newClosedProfits(positions *openPositions) *closedProfits {
	return &closedProfits{positions: positions, reads: make(map[closedRange]*closedRead)}
}

// handle answers the closed profit of the agents from the query from until
// before to, in Unix seconds, see ui.ClosedProfitResponse.
func (c *closedProfits) handle(w http.ResponseWriter, r *http.Request) {
	window, err := closedRangeOf(r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := c.read(r.Context(), window)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Errorf("ui: the closed profit of the agents: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "the closed profit of the agents can't be read now")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
}

// closedRangeOf returns the range of the query: from and to, in Unix seconds,
// from before to, maxClosedRange apart at most.
func closedRangeOf(query url.Values) (closedRange, error) {
	from, errFrom := strconv.ParseInt(query.Get("from"), 10, 64)
	to, errTo := strconv.ParseInt(query.Get("to"), 10, 64)
	switch {
	case errFrom != nil || errTo != nil || from < 0:
		return closedRange{}, errors.New("from and to: the range, in Unix seconds")
	case to <= from:
		return closedRange{}, errors.New("to must be after from")
	case time.Duration(to-from)*time.Second > maxClosedRange:
		return closedRange{}, errors.New("a range of 400 days at most")
	}
	return closedRange{from: time.Unix(from, 0), to: time.Unix(to, 0)}, nil
}

// read returns the closed profit of the agents in window, read again once the
// last read of it is closedProfitTTL old, a read of it at a time for all the
// requests; the reads of the other ranges, older, dropped.
func (c *closedProfits) read(ctx context.Context, window closedRange) (ui.ClosedProfitResponse, error) {
	for {
		c.mu.Lock()
		for key, read := range c.reads {
			if read.reading == nil && time.Since(read.at) > time.Minute {
				delete(c.reads, key)
			}
		}
		read := c.reads[window]
		if read == nil {
			read = &closedRead{}
			c.reads[window] = read
		}
		if read.response != nil && time.Since(read.at) < closedProfitTTL {
			response := *read.response
			c.mu.Unlock()
			return response, nil
		}
		if wait := read.reading; wait != nil {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ui.ClosedProfitResponse{}, ctx.Err()
			}
		}
		done := make(chan struct{})
		read.reading = done
		c.mu.Unlock()

		// the read goes on for the others when the request ends
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*bridgeTimeout+time.Second)
		response, err := c.readAll(readCtx, window)
		cancel()
		c.mu.Lock()
		if err == nil {
			read.response, read.at = &response, time.Now()
		}
		read.reading = nil
		close(done)
		c.mu.Unlock()
		return response, err
	}
}

// dealsRead is what came of reading the deals of an account: the account and
// its deals, or the reason it couldn't be read.
type dealsRead struct {
	account bridgeAccount
	deals   []bridgeDeal
	gap     string
}

// readAll reads the deals of the accounts of the agents trading live, at once,
// from closedLookback before window.
func (c *closedProfits) readAll(ctx context.Context, window closedRange) (ui.ClosedProfitResponse, error) {
	accounts, reads, err := c.readDeals(ctx, window)
	if err != nil {
		return ui.ClosedProfitResponse{}, err
	}
	return c.sum(window, accounts, reads), nil
}

// readWindows returns the closed profit of the agents in each of windows, of a
// single read of the deals of their accounts, now, whatever the reads the
// dashboards share (see read): the windows of the supervisor.
func (c *closedProfits) readWindows(ctx context.Context, windows []closedRange) ([]ui.ClosedProfitResponse,
	error) {
	if len(windows) == 0 {
		return nil, nil
	}
	span := windows[0]
	for _, window := range windows[1:] {
		if window.from.Before(span.from) {
			span.from = window.from
		}
		if window.to.After(span.to) {
			span.to = window.to
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*bridgeTimeout+time.Second)
	defer cancel()
	accounts, reads, err := c.readDeals(readCtx, span)
	if err != nil {
		return nil, err
	}
	responses := make([]ui.ClosedProfitResponse, 0, len(windows))
	for _, window := range windows {
		responses = append(responses, c.sum(window, accounts, reads))
	}
	return responses, nil
}

// readDeals reads the deals of the accounts of the agents trading live, at
// once, from closedLookback before window.
func (c *closedProfits) readDeals(ctx context.Context, window closedRange) ([]liveAccount, []dealsRead, error) {
	agents, err := c.positions.store.Agents(ctx)
	if err != nil {
		return nil, nil, err
	}
	accounts := liveAccounts(agents)
	query := "?" + url.Values{
		"from": {strconv.FormatInt(window.from.Add(-closedLookback).Unix(), 10)},
		"to":   {strconv.FormatInt(window.to.Unix(), 10)},
	}.Encode()
	reads := make([]dealsRead, len(accounts))
	var wg sync.WaitGroup
	for i, account := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := c.positions
			read := &reads[i]
			read.gap, _ = p.readBridge(ctx, account, "the closed profit", func(token string) error {
				*read = dealsRead{}
				if err := p.get(ctx, account.bridge, "/account", token, &read.account); err != nil {
					return err
				}
				return p.get(ctx, account.bridge, "/deals"+query, token, &read.deals)
			})
		}()
	}
	wg.Wait()
	return accounts, reads, nil
}

// sum sums the exits in window of the positions of the agents of the accounts
// read, by the currency of their account (none converted): each deal once by
// its ticket on its account, of the agent of the magic number of the entry of
// its position (see ownerOf), else of its own, eg: an entry older than the
// deals read; the profit, swap, commission and fee of each exit, as the net
// profit of an agent counts a close. Every account read has a total in its
// currency, 0 without an exit; one not read makes the sum incomplete.
func (c *closedProfits) sum(window closedRange, accounts []liveAccount, reads []dealsRead) ui.ClosedProfitResponse {
	response := ui.ClosedProfitResponse{From: window.from.Unix(), To: window.to.Unix(), Totals: []ui.CurrencyAmount{},
		Agents: []ui.AgentClosedProfit{}, Complete: true, At: time.Now().Unix()}
	order := make(map[string]int) // the place of the agents on the dashboard
	seen := make(map[string]bool)
	closed := make(map[string]map[string]bool)  // the positions of each agent closed in window
	results := make(map[int]map[string]float64) // the profit of each position closed, by agent of the response
	for i, account := range accounts {
		for _, a := range account.agents {
			order[a.agent.Name] = a.agent.Position
		}
		read := reads[i]
		if read.gap != "" {
			response.Complete = false
			response.Missing = append(response.Missing, gapOf(account, read.gap))
			continue
		}
		currency := read.account.currency()
		total := amountIn(&response.Totals, currency)
		entries := make(map[int64]bridgeDeal) // by position
		for _, deal := range read.deals {
			if deal.Entry == dealEntryIn && deal.PositionID != 0 {
				entries[deal.PositionID] = deal
			}
		}
		for _, deal := range read.deals {
			at := deal.at()
			if (deal.Type != dealTypeBuy && deal.Type != dealTypeSell) ||
				(deal.Entry != dealEntryOut && deal.Entry != dealEntryOutBy) ||
				at.Before(window.from) || !at.Before(window.to) {
				continue
			}
			magic, symbol := deal.Magic, deal.Symbol
			if entry, ok := entries[deal.PositionID]; ok {
				magic, symbol = entry.Magic, entry.Symbol
			}
			owner, ok := ownerOf(account.agents, magic, symbol)
			if !ok {
				continue // not of an agent, eg: opened by hand
			}
			key := read.account.key(account.bridge) + "/" + strconv.FormatInt(deal.Ticket, 10)
			if seen[key] {
				continue
			}
			seen[key] = true
			profit := deal.Profit + deal.Swap + deal.Commission + deal.Fee
			total.Amount += profit
			i := slices.IndexFunc(response.Agents, func(a ui.AgentClosedProfit) bool {
				return a.Agent == owner.Name && a.Currency == currency
			})
			if i < 0 {
				response.Agents = append(response.Agents, ui.AgentClosedProfit{Agent: owner.Name, Currency: currency,
					Running: c.positions.running(owner.Name)})
				i = len(response.Agents) - 1
			}
			response.Agents[i].Amount += profit
			position := read.account.key(account.bridge) + "/" + strconv.FormatInt(deal.PositionID, 10)
			if closed[owner.Name] == nil {
				closed[owner.Name] = make(map[string]bool)
			}
			if !closed[owner.Name][position] {
				closed[owner.Name][position] = true
				response.Agents[i].Positions++
			}
			if results[i] == nil {
				results[i] = make(map[string]float64)
			}
			results[i][position] += profit
		}
	}
	// the positions won and lost, each by the profit of all its exits
	for i, positions := range results {
		for _, profit := range positions {
			switch {
			case profit > 0:
				response.Agents[i].Wins++
				response.Agents[i].GrossProfit += profit
			case profit < 0:
				response.Agents[i].Losses++
				response.Agents[i].GrossLoss -= profit
			}
		}
	}
	slices.SortStableFunc(response.Agents, func(a, b ui.AgentClosedProfit) int {
		return cmp.Or(cmp.Compare(order[a.Agent], order[b.Agent]), strings.Compare(a.Agent, b.Agent),
			strings.Compare(a.Currency, b.Currency))
	})
	return response
}
