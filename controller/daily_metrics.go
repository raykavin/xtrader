package controller

import (
	"strconv"
	"strings"
	"time"
)

// dailyRead is what came of reading an account for the daily limits: its
// deals of the day and its positions open, or why it wasn't read (gap).
type dailyRead struct {
	account   bridgeAccount
	deals     []bridgeDeal
	positions []bridgePosition
	gap       string
}

// dailyOwner is the magic number and the symbol of the entry of a position,
// telling its agent once it is closed, its exit carrying no magic (eg:
// closed by hand); keyed by the account and the position.
type dailyOwner struct {
	magic  int64
	symbol string
}

// dailyCarry is the profit open of a position as last read, kept while its
// exit isn't in the deals yet (the history of the terminal lags the closes),
// see dailyCarryFor.
type dailyCarry struct {
	account  string // the key of its account, see bridgeAccount.key
	agent    string
	currency string
	open     float64
	seen     time.Time
}

// dailyCarryFor is how long the profit open of a position gone is kept
// without its exit in the deals.
const dailyCarryFor = time.Minute

// dailyAmounts is a result of the day in a currency: realized and open.
type dailyAmounts struct {
	realized float64
	open     float64
}

// dailyPosition is a position of an agent open, as read for the daily
// limits.
type dailyPosition struct {
	agent    string
	ticket   int64
	symbol   string
	currency string
	open     float64
}

// dailyMetric is the result of the day of the agents of the accounts read,
// see dailyMetrics: by agent and currency (realized, open), the positions of
// the agents open, the accounts not read (missing) and their agents.
type dailyMetric struct {
	byAgent   map[string]map[string]*dailyAmounts
	positions []dailyPosition
	missing   []string
	unread    map[string]bool // the agents of the accounts not read
	carry     map[string]dailyCarry
}

// amounts returns the amounts of agent in currency, added at 0.
func (m *dailyMetric) amounts(agent, currency string) *dailyAmounts {
	if m.byAgent[agent] == nil {
		m.byAgent[agent] = make(map[string]*dailyAmounts)
	}
	if m.byAgent[agent][currency] == nil {
		m.byAgent[agent][currency] = &dailyAmounts{}
	}
	return m.byAgent[agent][currency]
}

// dailyMetrics returns the result of the day (from window.from until now,
// window.to) of the agents of accounts read (reads, in their order), in the
// currency of each account, none converted:
//   - realized: the exits of the day with their profit, swap, commission and
//     fee, and the commission and fee of the entries of the day; each deal once
//     by its ticket on its account, of the agent of the magic number of the
//     entry of its position (the deals of the day, else owners, else its own);
//   - open: the profit and the swap of their positions open now, plus the last
//     profit open of a position gone whose exit isn't in the deals yet, for
//     dailyCarryFor at most (carry, from the read before);
//
// the positions of nobody (eg: opened by hand) left out. owners is fed with
// the entries and the positions read, for the reads to come.
func dailyMetrics(window closedRange, accounts []liveAccount, reads []dailyRead, owners map[string]dailyOwner,
	carry map[string]dailyCarry) dailyMetric {
	m := dailyMetric{byAgent: make(map[string]map[string]*dailyAmounts), unread: make(map[string]bool),
		carry: make(map[string]dailyCarry)}
	seen := make(map[string]bool)
	readKeys := make(map[string]bool) // the accounts read
	exited := make(map[string]bool)   // the positions closed in the deals read, by account
	for i, account := range accounts {
		read := reads[i]
		if read.gap != "" {
			names := gapOf(account, read.gap).Agents
			m.missing = append(m.missing, "a conta de "+strings.Join(names, ", ")+": "+gapText(read.gap))
			for _, name := range names {
				m.unread[name] = true
			}
			continue
		}
		key := read.account.key(account.bridge)
		readKeys[key] = true
		currency := read.account.currency()
		positionKey := func(id int64) string { return key + "/" + strconv.FormatInt(id, 10) }
		for _, deal := range read.deals {
			if deal.Entry == dealEntryIn && deal.PositionID != 0 {
				owners[positionKey(deal.PositionID)] = dailyOwner{magic: deal.Magic, symbol: deal.Symbol}
			}
		}
		for _, position := range read.positions {
			if _, ok := owners[positionKey(position.Ticket)]; !ok {
				owners[positionKey(position.Ticket)] = dailyOwner{magic: position.Magic, symbol: position.Symbol}
			}
		}
		for _, deal := range read.deals {
			at := deal.at()
			if (deal.Type != dealTypeBuy && deal.Type != dealTypeSell) || at.Before(window.from) ||
				at.After(window.to) {
				continue
			}
			dealKey := key + "/" + strconv.FormatInt(deal.Ticket, 10)
			if seen[dealKey] {
				continue
			}
			var amount float64
			magic, symbol := deal.Magic, deal.Symbol
			switch deal.Entry {
			case dealEntryIn:
				amount = deal.Commission + deal.Fee
			case dealEntryOut, dealEntryOutBy:
				amount = deal.Profit + deal.Swap + deal.Commission + deal.Fee
				if owner, ok := owners[positionKey(deal.PositionID)]; ok {
					magic, symbol = owner.magic, owner.symbol
				}
				exited[positionKey(deal.PositionID)] = true
			default:
				continue
			}
			agent, ok := ownerOf(account.agents, magic, symbol)
			if !ok {
				continue // not of an agent, eg: opened by hand
			}
			seen[dealKey] = true
			m.amounts(agent.Name, currency).realized += amount
		}
		open := make(map[string]bool)
		for _, position := range read.positions {
			agent, ok := ownerOf(account.agents, position.Magic, position.Symbol)
			if !ok {
				continue
			}
			k := positionKey(position.Ticket)
			if open[k] {
				continue
			}
			open[k] = true
			value := position.Profit + position.Swap
			m.amounts(agent.Name, currency).open += value
			m.positions = append(m.positions, dailyPosition{agent: agent.Name, ticket: position.Ticket,
				symbol: position.Symbol, currency: currency, open: value})
			m.carry[k] = dailyCarry{account: key, agent: agent.Name, currency: currency, open: value, seen: window.to}
		}
		// a position gone without its exit yet: its last profit open, a while
		for k, c := range carry {
			if c.account != key || open[k] || exited[k] || window.to.Sub(c.seen) > dailyCarryFor {
				continue
			}
			m.amounts(c.agent, c.currency).open += c.open
			m.carry[k] = c
		}
	}
	// the positions carried of the accounts not read, kept for the next read
	for k, c := range carry {
		if !readKeys[c.account] && window.to.Sub(c.seen) <= dailyCarryFor {
			m.carry[k] = c
		}
	}
	return m
}

// gapText returns the reason an account wasn't read, in the words of the
// dashboard.
func gapText(gap string) string {
	switch gap {
	case gapRefused:
		return "a bridge recusou o token"
	case gapFailed:
		return "o terminal respondeu com erro"
	case gapUnreachable:
		return "a bridge não respondeu"
	}
	return gap
}
