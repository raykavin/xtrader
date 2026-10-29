package controller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// riskParams are the settings of the agents that bound their risk, by the name
// of their flag: what the supervisor compares an agent with, never a limit of
// its own.
var riskParams = []string{"lot", "position-fraction", "leverage", "max-risk-pct", "direction", "stop-atr",
	"trailing-atr", "take-profit-atr", "stop-value", "take-profit-value", "max-bars", "min-atr-pct",
	"llm-min-rr", "llm-min-stop-atr", "llm-max-spread-atr", "llm-min-confidence", "llm-max-entry-atr",
	"llm-consult-interval", "news-block-before", "news-block-after"}

// The windows of the realized results the supervisor gets.
const (
	windowToday  = "today"
	window24h    = "last_24h"
	windowPeriod = "period" // since the start of the current period of the agent, see store.CurrentPeriods
)

// maxDecisions is how many decisions of an LLM agent the supervisor gets, the
// last ones, and maxReason how much of their reason.
const (
	maxDecisions = 5
	maxReason    = 240
)

// heldPosition is a position of an agent on its account: the bridge of the
// account, its alias and currency.
type heldPosition struct {
	account  string
	bridge   string
	currency string
	position bridgePosition
}

// heldOrder is a pending order of an agent on its account.
type heldOrder struct {
	account string
	bridge  string
	order   bridgeOrder
}

// view is what the supervisor read of the agents at a time: the snapshot sent
// to the model, and beside it, never sent, what the actions need: the agents
// of the store, the accounts with their bridges, the positions and pending
// orders of each agent, the state the paper agents told, and why the data of
// an agent is incomplete.
type view struct {
	snapshot  ui.SupervisorSnapshot
	at        time.Time
	agents    map[string]store.Agent
	positions map[string][]heldPosition
	orders    map[string][]heldOrder
	paper     map[string]ui.SupervisionState
	gaps      map[string]string
	// protections is how the protection of the positions of each agent
	// stands, as the agents told it, see ui.PositionProtection
	protections map[string][]ui.PositionProtection
}

// agentGap returns why the data of the agent of name is incomplete, "" when it
// isn't.
func (v *view) agentGap(name string) string {
	return v.gaps[name]
}

// read reads the agents, their accounts and their results, now, and returns
// the view of them.
func (s *Supervision) read(ctx context.Context) (*view, error) {
	agents, err := s.store.Agents(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	v := &view{at: now, agents: make(map[string]store.Agent, len(agents)),
		positions: make(map[string][]heldPosition), orders: make(map[string][]heldOrder),
		paper: make(map[string]ui.SupervisionState), gaps: make(map[string]string),
		protections: make(map[string][]ui.PositionProtection)}
	zone := llm.LocalZone() // the day of the windows and of the daily limits
	v.snapshot = ui.SupervisorSnapshot{GeneratedAt: now.In(zone).Format(time.RFC3339), Timezone: zone.String(),
		Accounts: []ui.SupervisorAccount{}, Exposure: []ui.SupervisorExposure{}, Agents: []ui.SupervisorAgent{}}
	if s.daily != nil {
		status := s.daily.Status()
		v.snapshot.DailyGuard = &status
	}
	for _, a := range agents {
		v.agents[a.Name] = a
	}

	// the accounts, their positions and pending orders, of their agents
	accounts, reads, err := s.positions.readFresh(ctx)
	if err != nil {
		return nil, err
	}
	accountOf := make(map[string]string) // the alias of the account of each agent trading live
	for i, account := range accounts {
		alias := fmt.Sprintf("conta %d", i+1)
		read := reads[i]
		summary := ui.SupervisorAccount{Account: alias, Agents: []string{}}
		for _, a := range account.agents {
			summary.Agents = append(summary.Agents, a.agent.Name)
			accountOf[a.agent.Name] = alias
		}
		if read.gap != "" {
			summary.Gap = read.gap
			for _, a := range account.agents {
				v.gaps[a.agent.Name] = "its account wasn't read: " + read.gap
			}
			v.snapshot.Gaps = append(v.snapshot.Gaps, fmt.Sprintf("%s not read (%s): agents %s", alias, read.gap,
				strings.Join(summary.Agents, ", ")))
			v.snapshot.Accounts = append(v.snapshot.Accounts, summary)
			continue
		}
		currency := read.account.currency()
		balance := read.account.Balance
		summary.Currency, summary.Balance, summary.ReadAt = currency, &balance, now.Format(time.RFC3339)
		v.snapshot.Accounts = append(v.snapshot.Accounts, summary)
		for _, position := range read.positions {
			owner, ok := ownerOf(account.agents, position.Magic, position.Symbol)
			if !ok {
				continue // not of an agent, eg: by hand
			}
			v.positions[owner.Name] = append(v.positions[owner.Name], heldPosition{account: alias,
				bridge: account.bridge, currency: currency, position: position})
		}
		for _, order := range read.orders {
			owner, ok := ownerOf(account.agents, order.Magic, order.Symbol)
			if !ok {
				continue
			}
			v.orders[owner.Name] = append(v.orders[owner.Name], heldOrder{account: alias, bridge: account.bridge,
				order: order})
		}
	}

	results, windows, err := s.realized(ctx, now, agents)
	if err != nil {
		// the error logged only: the model gets the gap, never what a driver
		// tells of its connection
		log.Warnf("supervisor: the realized results of the agents: %v", err)
		v.snapshot.Gaps = append(v.snapshot.Gaps, "the realized results weren't read")
	}
	v.snapshot.Windows = windows

	// the agents worth telling: the enabled ones, and the others holding a
	// position or an order, or stopped by the supervisor
	var shown []store.Agent
	for _, a := range agents {
		supervision := a.SupervisionOf()
		if a.Enabled || len(v.positions[a.Name]) > 0 || len(v.orders[a.Name]) > 0 ||
			supervision.StopState != store.StopNone || supervision.EntriesBlocked {
			shown = append(shown, a)
		}
	}
	summaries := make([]ui.SupervisorAgent, len(shown))
	prompts := newPromptCache(s.store)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, a := range shown {
		wg.Add(1)
		go func() {
			defer wg.Done()
			summary, paper, gap := s.agentSummary(ctx, a, v, accountOf[a.Name], results, prompts)
			summaries[i] = summary
			mu.Lock()
			defer mu.Unlock()
			if paper != nil {
				v.paper[a.Name] = *paper
			}
			if gap != "" && v.gaps[a.Name] == "" {
				v.gaps[a.Name] = gap
			}
		}()
	}
	wg.Wait()
	v.snapshot.Agents = summaries
	for _, summary := range summaries {
		if len(summary.Protections) > 0 {
			v.protections[summary.Name] = summary.Protections
		}
	}
	if hidden := len(agents) - len(shown); hidden > 0 {
		v.snapshot.Gaps = append(v.snapshot.Gaps, fmt.Sprintf("%d agents disabled, without position nor order, "+
			"left out", hidden))
	}
	v.snapshot.Exposure = exposureOf(v)
	return v, nil
}

// agentResults are the realized results of the agents, by window, agent and
// currency.
type agentResults map[string]map[string][]ui.AgentClosedProfit

// realized reads the realized results of the agents in their windows: today
// (since the midnight of the controller), the last 24 hours, and the current
// period of each agent (see store.CurrentPeriods), 400 days at most; a single
// read of the deals of the accounts.
func (s *Supervision) realized(ctx context.Context, now time.Time, agents []store.Agent) (agentResults,
	[]ui.SupervisorWindow, error) {
	local := now.In(llm.LocalZone())
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	windows := []closedRange{{from: midnight, to: now}, {from: now.Add(-24 * time.Hour), to: now}}
	names := []string{windowToday, window24h}
	periods, err := s.store.CurrentPeriods(ctx)
	if err != nil {
		return nil, nil, err
	}
	starts := make(map[time.Time]int) // the index of the window of each start of a period
	for _, a := range agents {
		period, ok := periods[a.Name]
		if !ok || !a.Enabled {
			continue
		}
		start := period.StartedAt
		if oldest := now.Add(-400 * 24 * time.Hour); start.Before(oldest) {
			start = oldest
		}
		if _, known := starts[start]; !known && start.Before(now) {
			starts[start] = len(windows)
			windows = append(windows, closedRange{from: start, to: now})
			names = append(names, windowPeriod)
		}
	}
	shown := []ui.SupervisorWindow{{Name: windowToday, From: midnight.Format(time.RFC3339),
		To: now.Format(time.RFC3339)}, {Name: window24h, From: now.Add(-24 * time.Hour).Format(time.RFC3339),
		To: now.Format(time.RFC3339)}, {Name: windowPeriod, From: "the start of the period of each agent",
		To: now.Format(time.RFC3339)}}
	responses, err := s.closed.readWindows(ctx, windows)
	if err != nil {
		return nil, shown, err
	}
	results := agentResults{}
	for i, response := range responses {
		for _, profit := range response.Agents {
			key := names[i]
			if key == windowPeriod {
				period, ok := periods[profit.Agent]
				if !ok || starts[maxTime(period.StartedAt, now.Add(-400*24*time.Hour))] != i {
					continue // the window of another start
				}
			}
			if results[key] == nil {
				results[key] = make(map[string][]ui.AgentClosedProfit)
			}
			results[key][profit.Agent] = append(results[key][profit.Agent], profit)
		}
	}
	return results, shown, nil
}

// maxTime returns the later of a and b.
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// promptCache reads the prompts of the agents once each.
type promptCache struct {
	store   *store.Store
	mu      sync.Mutex
	prompts map[int64]store.Prompt
}

func newPromptCache(s *store.Store) *promptCache {
	return &promptCache{store: s, prompts: make(map[int64]store.Prompt)}
}

// get returns the prompt of id.
func (c *promptCache) get(ctx context.Context, id int64) (store.Prompt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prompt, ok := c.prompts[id]; ok {
		return prompt, nil
	}
	prompt, err := c.store.Prompt(ctx, id)
	if err != nil {
		return store.Prompt{}, err
	}
	c.prompts[id] = prompt
	return prompt, nil
}

// agentSummary returns the agent a as the supervisor knows it, the state its
// API tells in paper, and why its data is incomplete.
func (s *Supervision) agentSummary(ctx context.Context, a store.Agent, v *view, account string,
	results agentResults, prompts *promptCache) (ui.SupervisorAgent, *ui.SupervisionState, string) {
	params := a.AllParams()
	summary := ui.SupervisorAgent{Name: a.Name, Kind: a.Kind, Mode: a.Mode, Enabled: a.Enabled, Account: account,
		Positions: []ui.SupervisorPosition{}, Pending: []ui.SupervisorPending{}, OpenProfit: []ui.CurrencyAmount{},
		Realized: []ui.SupervisorRealized{}, Pairs: strings.Split(cmp.Or(params["pair"], agentapi.DefaultPair), ";"),
		Timeframe: params["timeframe"]}
	summary.Strategy = cmp.Or(params["strategy"], agentapi.DefaultStrategy(a.Kind))

	state := s.stateOf(a.Name)
	summary.Restarts, summary.LastExit, summary.LaunchError = state.Restarts, state.LastExit, state.LaunchError
	summary.RestartPending = state.RestartPending
	switch {
	case state.Running && state.Restarting:
		summary.State = "restarting"
	case state.Running:
		summary.State = "running"
	case !a.Enabled:
		summary.State = "disabled"
	case state.LaunchError != "":
		summary.State = "launch_error"
	case state.Exited:
		summary.State = "exited"
	default:
		summary.State = "stopped"
	}
	summary.Outbox = s.manager.Outbox(a.ID)
	supervision := a.SupervisionOf()
	summary.EntriesBlocked, summary.EntriesReason = supervision.EntriesBlocked, supervision.EntriesReason
	if supervision.StopState != store.StopNone {
		summary.StopState, summary.StopReason = supervision.StopState, supervision.StopReason
	}
	risk := make(map[string]string)
	for _, name := range riskParams {
		if value, ok := params[name]; ok {
			risk[name] = value
		}
	}
	if len(risk) > 0 {
		summary.Risk = risk
	}
	if period, ok := v.agentPeriod(ctx, s, a.Name); ok {
		summary.PeriodStart = period
	}

	// the language model, its prompt and its daily limits
	if l := a.LLM; l != nil {
		summary.Model = l.Provider + " " + l.Model
		if l.PromptID != nil {
			if prompt, err := prompts.get(ctx, *l.PromptID); err == nil {
				summary.Prompt = fmt.Sprintf("%s v%d", prompt.Name, prompt.Version)
				if gain, loss, currency, err := llm.StrategyDailyLimits(prompt.Content); err == nil &&
					(gain > 0 || loss > 0) {
					summary.DailyLimits = &ui.SupervisorDailyLimits{Gain: gain, Loss: loss, Currency: currency}
				}
			}
		}
		if status := l.PromptStatus; status != nil {
			summary.PromptStatus = "applied"
			if !status.Applied {
				summary.PromptStatus = "refused: " + status.Error
			}
		}
		decisions, err := s.store.Decisions(ctx, a.ID, "", maxDecisions)
		if err == nil {
			for _, d := range decisions {
				reason := d.Reason
				if runes := []rune(reason); len(runes) > maxReason {
					reason = string(runes[:maxReason]) + "…"
				}
				summary.Decisions = append(summary.Decisions, ui.SupervisorDecision{
					At: time.Unix(d.DecidedAt, 0).In(v.at.Location()).Format(time.RFC3339), Pair: d.Pair,
					Action: d.Action, Confidence: d.Confidence, Outcome: d.Outcome, Reason: reason})
			}
		}
	}

	// the positions and pending orders: on the account, or told by the agent
	// in paper
	var paper *ui.SupervisionState
	gap := ""
	if state.Running && !state.Restarting {
		summary.Reachable = s.dashboard.Online(ctx, a.Name)
		if !summary.Reachable {
			gap = "its API doesn't answer"
		}
	}
	if a.Mode == "paper" {
		if summary.Reachable {
			told, err := s.supervision(ctx, a.Name)
			if err != nil {
				log.Warnf("supervisor: the state of the agent %s: %v", a.Name, err)
				gap = "its state can't be read"
			} else {
				paper = &told
				for _, p := range told.Positions {
					summary.Positions = append(summary.Positions, ui.SupervisorPosition{Pair: p.Pair, Side: p.Side,
						Volume: p.Quantity, Paper: true})
				}
				for _, o := range told.Pending {
					summary.Pending = append(summary.Pending, ui.SupervisorPending{Ticket: o.ID, Pair: o.Pair,
						Side: o.Side, Type: o.Type, Price: o.Price, Volume: o.Quantity, Entry: o.Entry, Paper: true})
				}
			}
		}
	} else {
		for _, held := range v.positions[a.Name] {
			p := held.position
			side := "BUY"
			if p.Type == 1 {
				side = "SELL"
			}
			position := ui.SupervisorPosition{Ticket: p.Ticket, Pair: p.Symbol, Side: side, Volume: p.Volume,
				Entry: p.PriceOpen, Price: p.PriceCurrent, StopLoss: p.SL, TakeProfit: p.TP,
				Profit: round2(p.Profit + p.Swap), Currency: held.currency}
			if p.TimeMsc > 0 {
				position.OpenedAt = time.UnixMilli(p.TimeMsc).In(v.at.Location()).Format(time.RFC3339)
			}
			summary.Positions = append(summary.Positions, position)
			amountIn(&summary.OpenProfit, held.currency).Amount += p.Profit + p.Swap
		}
		for _, held := range v.orders[a.Name] {
			o := held.order
			summary.Pending = append(summary.Pending, ui.SupervisorPending{Ticket: o.Ticket, Pair: o.Symbol,
				Side: o.side(), Type: pendingType(o.Type), Price: o.PriceOpen, Volume: o.VolumeCurrent,
				StopLoss: o.SL, TakeProfit: o.TP, Entry: true})
		}
	}
	// the protection of its positions, as the agent tells it
	if settings := a.ProtectionOf(); settings.Active() {
		summary.Protection = &settings
		switch {
		case paper != nil:
			summary.Protections = paper.Protections
		case summary.Reachable:
			if told, err := s.supervision(ctx, a.Name); err == nil {
				summary.Protections = told.Protections
			} else {
				log.Warnf("supervisor: the protection of the positions of the agent %s: %v", a.Name, err)
			}
		}
	}
	for i := range summary.OpenProfit {
		summary.OpenProfit[i].Amount = round2(summary.OpenProfit[i].Amount)
	}
	for _, window := range []string{windowToday, window24h, windowPeriod} {
		for _, profit := range results[window][a.Name] {
			summary.Realized = append(summary.Realized, ui.SupervisorRealized{Window: window,
				Amount: round2(profit.Amount), Currency: profit.Currency, Positions: profit.Positions,
				Wins: profit.Wins, Losses: profit.Losses, GrossProfit: round2(profit.GrossProfit),
				GrossLoss: round2(profit.GrossLoss)})
		}
	}
	return summary, paper, gap
}

// agentPeriod returns the start of the current period of the agent of name,
// formatted, false before its first reset.
func (v *view) agentPeriod(ctx context.Context, s *Supervision, name string) (string, bool) {
	start, ok := s.dashboard.currentPeriod(ctx, name)
	if !ok {
		return "", false
	}
	return start.In(v.at.Location()).Format(time.RFC3339), true
}

// pendingType names the type of a pending order of MT5 (ORDER_TYPE_*).
func pendingType(kind int) string {
	switch kind {
	case 2, 3:
		return "LIMIT"
	case 4, 5:
		return "STOP"
	case 6, 7:
		return "STOP_LIMIT"
	}
	return fmt.Sprintf("TYPE_%d", kind)
}

// round2 rounds a value of money to cents, for the model.
func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

// exposureOf sums the positions of the agents of v by account and pair: the
// lots bought and sold, the positions and the agents on each side, and their
// open profit, in the currency of the account, each position counted once
// (see openPositions.sum).
func exposureOf(v *view) []ui.SupervisorExposure {
	type key struct{ account, pair string }
	byKey := make(map[key]*ui.SupervisorExposure)
	for _, name := range slices.Sorted(maps.Keys(v.positions)) {
		for _, held := range v.positions[name] {
			k := key{held.account, held.position.Symbol}
			e := byKey[k]
			if e == nil {
				e = &ui.SupervisorExposure{Account: held.account, Currency: held.currency, Pair: held.position.Symbol}
				byKey[k] = e
			}
			if held.position.Type == 1 {
				e.SellLots, e.SellPositions = e.SellLots+held.position.Volume, e.SellPositions+1
				if !slices.Contains(e.SellAgents, name) {
					e.SellAgents = append(e.SellAgents, name)
				}
			} else {
				e.BuyLots, e.BuyPositions = e.BuyLots+held.position.Volume, e.BuyPositions+1
				if !slices.Contains(e.BuyAgents, name) {
					e.BuyAgents = append(e.BuyAgents, name)
				}
			}
			e.OpenProfit += held.position.Profit + held.position.Swap
		}
	}
	exposure := make([]ui.SupervisorExposure, 0, len(byKey))
	for _, e := range byKey {
		e.BuyLots, e.SellLots = math.Round(e.BuyLots*1000)/1000, math.Round(e.SellLots*1000)/1000
		e.OpenProfit = round2(e.OpenProfit)
		exposure = append(exposure, *e)
	}
	slices.SortFunc(exposure, func(a, b ui.SupervisorExposure) int {
		return cmp.Or(strings.Compare(a.Account, b.Account), strings.Compare(a.Pair, b.Pair))
	})
	return exposure
}

// errNoAgent is the error of an action on an agent the controller doesn't
// serve, eg: disabled, its API unknown.
var errNoAgent = errors.New("the agent isn't served by the controller")

// supervision reads the state of the agent of name on the channel of the
// supervisor, see ui.SupervisionState.
func (s *Supervision) supervision(ctx context.Context, name string) (ui.SupervisionState, error) {
	var state ui.SupervisionState
	err := s.callAgent(ctx, name, "GET", "supervision", nil, &state)
	return state, err
}

// callAgent calls the API of the agent of name on the channel of the
// supervisor, see Dashboard.callWith.
func (s *Supervision) callAgent(ctx context.Context, name, method, path string, body, out any) error {
	a, ok := s.dashboard.agents.get(name)
	if !ok {
		return fmt.Errorf("%s: %w", name, errNoAgent)
	}
	return s.dashboard.callWith(ctx, s.client, a, ui.ChannelSupervisor, method, path, body, out)
}
