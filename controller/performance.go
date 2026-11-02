package controller

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/ui"
)

// performanceTTL is how long a read of the performance of a range answers the
// dashboards before the bridges are read again, see GET /api/performance.
var performanceTTL = 30 * time.Second

// performanceVolumeTolerance is the volume, in lots, under which what is left
// of a position after its exits counts as nothing, the float noise of their
// sums.
const performanceVolumeTolerance = 1e-6

// performance reads the trades of the agents trading live closed in a range,
// from the deals of their MT5 accounts, for the page of the performance of the
// dashboard, see ui.PerformanceResponse.
type performance struct {
	positions *openPositions
	reads     rangeReads[ui.PerformanceResponse]
}

func newPerformance(positions *openPositions) *performance {
	return &performance{positions: positions}
}

// handle answers the performance of the agents from the query from until
// before to, in Unix seconds, see ui.PerformanceResponse.
func (p *performance) handle(w http.ResponseWriter, r *http.Request) {
	window, err := closedRangeOf(r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := p.reads.read(r.Context(), window, performanceTTL, p.readAll)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Errorf("ui: the performance of the agents: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Não foi possível ler a performance dos agentes agora. "+
			"Tente de novo em instantes.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
}

// readAll reads the deals of the accounts of the agents trading live, at once,
// from closedLookback before window (the entries of the positions closed in
// it) until now (the balance of each account at the start of window), and
// makes them the trades of window.
func (p *performance) readAll(ctx context.Context, window closedRange) (ui.PerformanceResponse, error) {
	agents, err := p.positions.store.Agents(ctx)
	if err != nil {
		return ui.PerformanceResponse{}, err
	}
	accounts := liveAccounts(agents)
	now := time.Now()
	query := "?" + url.Values{
		"from": {strconv.FormatInt(window.from.Add(-closedLookback).Unix(), 10)},
		"to":   {strconv.FormatInt(now.Add(time.Minute).Unix(), 10)},
	}.Encode()
	reads := make([]dealsRead, len(accounts))
	var wg sync.WaitGroup
	for i, account := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := p.positions
			read := &reads[i]
			read.gap, _ = o.readBridge(ctx, account, "the performance", func(token string) error {
				*read = dealsRead{}
				if err := o.get(ctx, account.bridge, "/account", token, &read.account); err != nil {
					return err
				}
				return o.get(ctx, account.bridge, "/deals"+query, token, &read.deals)
			})
		}()
	}
	wg.Wait()
	return performanceOf(window, accounts, reads, now), nil
}

// performanceOf makes the trades of window of the accounts read, see
// ui.PerformanceResponse: each position of an agent (the one of the magic
// number of its entry, see ownerOf; manual ones left out) whose last exit is
// in window and of which nothing is left open, each account once whatever the
// bridges it is read from; the balance of each account at the start of window
// its balance now less every deal booked since. One account not read makes it
// incomplete.
func performanceOf(window closedRange, accounts []liveAccount, reads []dealsRead,
	now time.Time) ui.PerformanceResponse {
	response := ui.PerformanceResponse{From: window.from.Unix(), To: window.to.Unix(),
		Trades: []ui.PerformanceTrade{}, Accounts: []ui.PerformanceAccount{}, Complete: true, At: now.Unix()}
	for i, account := range accounts {
		read := reads[i]
		if read.gap != "" {
			response.Complete = false
			response.Missing = append(response.Missing, gapOf(account, read.gap))
			continue
		}
		key := read.account.key(account.bridge)
		currency := read.account.currency()
		names := make([]string, 0, len(account.agents))
		for _, a := range account.agents {
			names = append(names, a.agent.Name)
		}
		if i := slices.IndexFunc(response.Accounts, func(a ui.PerformanceAccount) bool {
			return a.Account == key
		}); i >= 0 {
			// the same account read from another bridge: its agents added, its deals once
			for _, name := range names {
				if !slices.Contains(response.Accounts[i].Agents, name) {
					response.Accounts[i].Agents = append(response.Accounts[i].Agents, name)
				}
			}
			continue
		}
		start := read.account.Balance
		for _, deal := range read.deals {
			if !deal.at().Before(window.from) {
				start -= deal.amount()
			}
		}
		response.Accounts = append(response.Accounts, ui.PerformanceAccount{Account: key, Currency: currency,
			Agents: names, Balance: read.account.Balance, StartBalance: round2(start)})
		response.Trades = append(response.Trades, tradesOf(window, key, currency, account, read.deals)...)
	}
	slices.SortStableFunc(response.Trades, func(a, b ui.PerformanceTrade) int {
		return cmp.Or(cmp.Compare(a.ClosedAt, b.ClosedAt), cmp.Compare(a.Position, b.Position))
	})
	return response
}

// tradesOf returns the trades of the agents of account closed in window, see
// performanceOf, of its deals, in its currency.
func tradesOf(window closedRange, key, currency string, account liveAccount,
	deals []bridgeDeal) []ui.PerformanceTrade {
	type position struct {
		entry *bridgeDeal
		exits []bridgeDeal
	}
	positions := make(map[int64]*position)
	var order []int64
	seen := make(map[int64]bool) // the tickets, each deal once
	for _, deal := range deals {
		if (deal.Type != dealTypeBuy && deal.Type != dealTypeSell) || deal.PositionID == 0 || seen[deal.Ticket] {
			continue
		}
		seen[deal.Ticket] = true
		p := positions[deal.PositionID]
		if p == nil {
			p = &position{}
			positions[deal.PositionID] = p
			order = append(order, deal.PositionID)
		}
		switch deal.Entry {
		case dealEntryIn:
			if p.entry == nil {
				d := deal
				p.entry = &d
			}
		case dealEntryOut, dealEntryOutBy:
			p.exits = append(p.exits, deal)
		}
	}

	var trades []ui.PerformanceTrade
	for _, id := range order {
		p := positions[id]
		if len(p.exits) == 0 {
			continue // still open
		}
		last := p.exits[0]
		var exited, priced float64
		for _, exit := range p.exits {
			if exit.at().After(last.at()) {
				last = exit
			}
			exited += exit.Volume
			priced += exit.Volume * exit.Price
		}
		at := last.at()
		if at.Before(window.from) || !at.Before(window.to) {
			continue
		}
		magic, symbol := last.Magic, last.Symbol
		if p.entry != nil {
			magic, symbol = p.entry.Magic, p.entry.Symbol
			if p.entry.Volume-exited > performanceVolumeTolerance {
				continue // a part of it still open
			}
		}
		owner, ok := ownerOf(account.agents, magic, symbol)
		if !ok {
			continue // not of an agent, eg: opened by hand
		}
		trade := ui.PerformanceTrade{Position: id, Account: key, Agent: owner.Name, Symbol: symbol,
			Volume: exited, ClosedAt: at.Unix(), Currency: currency}
		if exited > 0 {
			trade.Exit = priced / exited
		}
		var gross, costs float64
		for _, exit := range p.exits {
			gross += exit.Profit
			costs += exit.Swap + exit.Commission + exit.Fee
		}
		// the side of the entry, else the opposite of an exit's
		switch {
		case p.entry != nil:
			trade.Side, trade.Volume, trade.Entry = sideOf(p.entry.Type), p.entry.Volume, p.entry.Price
			trade.OpenedAt = p.entry.at().Unix()
			gross += p.entry.Profit
			costs += p.entry.Swap + p.entry.Commission + p.entry.Fee
		case last.Type == dealTypeBuy:
			trade.Side = sideOf(dealTypeSell)
		default:
			trade.Side = sideOf(dealTypeBuy)
		}
		trade.Gross, trade.Costs = round2(gross), round2(costs)
		trade.Net = round2(gross + costs)
		trades = append(trades, trade)
	}
	return trades
}

// sideOf returns the side of a deal of type, BUY or SELL.
func sideOf(dealType int) string {
	if dealType == dealTypeSell {
		return "SELL"
	}
	return "BUY"
}
