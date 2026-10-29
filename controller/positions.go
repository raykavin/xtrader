package controller

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// openPositionsTTL is how long a read of the open positions answers the
// dashboards before the bridges are read again: the dashboards and their tabs
// poll it, see GET /api/positions/open.
var openPositionsTTL = 2 * time.Second

// bridgeTimeout bounds a request to a bridge: a bridge busy, eg: reconnecting
// its terminal, makes the read incomplete rather than holding the dashboard.
var bridgeTimeout = 4 * time.Second

// The reasons an account of the agents isn't read, see ui.OpenPositionsGap.
const (
	gapUnreachable = "unreachable" // the bridge didn't answer
	gapRefused     = "refused"     // the bridge refused the tokens of the agents
	gapFailed      = "failed"      // the bridge answered an error, eg: the terminal disconnected
)

// openPositions reads the positions the agents hold open on their MT5
// accounts, from the bridges of the accounts (see agentapi.MT5Book), for the
// card of the open profit of the dashboard: of every agent trading live,
// running or not (stopped, exited, disabled, run apart), as the terminal
// reports them, in the currency of their account, each one counted once by its
// ticket on its account, whatever the agents sharing it; the closed trades and
// the pending orders left out.
type openPositions struct {
	store   *store.Store
	manager *Manager
	client  *http.Client

	mu      sync.Mutex
	last    *ui.OpenPositionsResponse
	at      time.Time
	reading chan struct{} // closed when the read in progress ends, nil without one
}

func newOpenPositions(s *store.Store, manager *Manager) *openPositions {
	return &openPositions{store: s, manager: manager, client: &http.Client{Timeout: bridgeTimeout}}
}

// handle answers the open positions of the agents, see ui.OpenPositionsResponse.
func (p *openPositions) handle(w http.ResponseWriter, r *http.Request) {
	response, err := p.read(r.Context())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Errorf("ui: the open positions of the agents: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "the open positions of the agents can't be read now")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
}

// read returns the open positions of the agents, read again once the last
// read is openPositionsTTL old, a read at a time for all the requests.
func (p *openPositions) read(ctx context.Context) (ui.OpenPositionsResponse, error) {
	for {
		p.mu.Lock()
		if p.last != nil && time.Since(p.at) < openPositionsTTL {
			last := *p.last
			p.mu.Unlock()
			return last, nil
		}
		if wait := p.reading; wait != nil {
			p.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return ui.OpenPositionsResponse{}, ctx.Err()
			}
		}
		done := make(chan struct{})
		p.reading = done
		p.mu.Unlock()

		// the read goes on for the others when the request ends
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
			2*bridgeTimeout+time.Second,
		)
		response, err := p.readAll(readCtx)
		cancel()
		p.mu.Lock()
		if err == nil {
			p.last, p.at = &response, time.Now()
		}
		p.reading = nil
		close(done)
		p.mu.Unlock()
		return response, err
	}
}

// bookedAgent is an agent trading live on an account, and what it trades
// there.
type bookedAgent struct {
	agent store.Agent
	book  agentapi.MT5Book
}

// liveAccount is an account of the agents trading live, a bridge, and the
// agents on it.
type liveAccount struct {
	bridge string
	agents []bookedAgent
}

// readAll reads the accounts of the agents trading live, at once.
func (p *openPositions) readAll(ctx context.Context) (ui.OpenPositionsResponse, error) {
	agents, err := p.store.Agents(ctx)
	if err != nil {
		return ui.OpenPositionsResponse{}, err
	}
	accounts := liveAccounts(agents)
	reads := make([]accountRead, len(accounts))
	var wg sync.WaitGroup
	for i, account := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reads[i] = p.readAccount(ctx, account, false)
		}()
	}
	wg.Wait()
	return p.sum(accounts, reads), nil
}

// liveAccounts returns the accounts of the agents trading live (in live mode,
// enabled or not), by bridge (see agentapi.SameMT5Bridge), in the order of the
// dashboard. An agent in paper holds no position: it reads the market only.
func liveAccounts(agents []store.Agent) []liveAccount {
	var accounts []liveAccount
	for _, a := range agents {
		if a.Mode != "live" {
			continue
		}
		book := a.MT5Book()
		i := slices.IndexFunc(accounts, func(account liveAccount) bool {
			return agentapi.SameMT5Bridge(account.bridge, book.Bridge)
		})
		if i < 0 {
			accounts = append(accounts, liveAccount{bridge: book.Bridge})
			i = len(accounts) - 1
		}
		accounts[i].agents = append(accounts[i].agents, bookedAgent{agent: a, book: book})
	}
	return accounts
}

// bridgeAccount is the account of a bridge, of GET /account: its login, its
// currency and its balance.
type bridgeAccount struct {
	Login    int64   `json:"login"`
	Currency string  `json:"currency"`
	Balance  float64 `json:"balance"`
}

// currency returns the currency of the account, as the totals key it.
func (a bridgeAccount) currency() string { return strings.ToUpper(strings.TrimSpace(a.Currency)) }

// key returns what tells the account apart, eg: 198974329: its login, else the
// bridge read, for a bridge without one.
func (a bridgeAccount) key(bridge string) string {
	if a.Login == 0 {
		return bridge
	}
	return fmt.Sprint(a.Login)
}

// bridgePosition is a position of an account, of GET /positions: its profit
// and swap in the currency of the account, as the terminal shows them.
type bridgePosition struct {
	Ticket       int64   `json:"ticket"`
	Type         int     `json:"type"` // 0 buy, 1 sell
	Magic        int64   `json:"magic"`
	Volume       float64 `json:"volume"`
	PriceOpen    float64 `json:"price_open"`
	PriceCurrent float64 `json:"price_current"`
	SL           float64 `json:"sl"` // the stop loss at the broker, 0 for none
	TP           float64 `json:"tp"` // the take profit at the broker, 0 for none
	Swap         float64 `json:"swap"`
	Profit       float64 `json:"profit"`
	Symbol       string  `json:"symbol"`
	TimeMsc      int64   `json:"time_msc"` // of its opening, by the clock of the server
}

// bridgeOrder is a pending order of an account, of GET /orders: its type is
// the one of MT5 (ORDER_TYPE_*), 2 a buy limit, 3 a sell limit, 4 a buy stop,
// 5 a sell stop, 6 and 7 the stop limits.
type bridgeOrder struct {
	Ticket        int64   `json:"ticket"`
	Type          int     `json:"type"`
	Magic         int64   `json:"magic"`
	Symbol        string  `json:"symbol"`
	PriceOpen     float64 `json:"price_open"`
	VolumeCurrent float64 `json:"volume_current"`
	SL            float64 `json:"sl"`
	TP            float64 `json:"tp"`
	TimeSetupMsc  int64   `json:"time_setup_msc"`
}

// side returns the side of the pending order, BUY or SELL.
func (o bridgeOrder) side() string {
	if o.Type%2 == 1 {
		return "SELL"
	}
	return "BUY"
}

// accountRead is what came of reading an account: its positions, its pending
// orders when read, or the reason it couldn't be read.
type accountRead struct {
	account   bridgeAccount
	positions []bridgePosition
	orders    []bridgeOrder // read with the positions when asked, see readAccount
	gap       string        // why the account isn't read, see the gap* reasons
	err       error
}

// readAccount reads the account and the positions of the bridge of account,
// and its pending orders withOrders, see readBridge.
func (p *openPositions) readAccount(ctx context.Context, account liveAccount, withOrders bool) accountRead {
	var read accountRead
	read.gap, read.err = p.readBridge(ctx, account, "the open positions", func(token string) error {
		read = accountRead{}
		if err := p.get(ctx, account.bridge, "/account", token, &read.account); err != nil {
			return err
		}
		if err := p.get(ctx, account.bridge, "/positions", token, &read.positions); err != nil {
			return err
		}
		if !withOrders {
			return nil
		}
		return p.get(ctx, account.bridge, "/orders", token, &read.orders)
	})
	return read
}

// readFresh reads the accounts of the agents trading live at once, now,
// whatever the read the dashboards share (see read), with their pending
// orders: what the supervisor checks the agents with.
func (p *openPositions) readFresh(ctx context.Context) ([]liveAccount, []accountRead, error) {
	agents, err := p.store.Agents(ctx)
	if err != nil {
		return nil, nil, err
	}
	accounts := liveAccounts(agents)
	reads := make([]accountRead, len(accounts))
	readCtx, cancel := context.WithTimeout(ctx, 3*bridgeTimeout+time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, account := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reads[i] = p.readAccount(readCtx, account, true)
		}()
	}
	wg.Wait()
	return accounts, reads, nil
}

// readBridge reads the bridge of account with read, given the token of each
// agent on it in turn until one is taken, none when no agent has one: what
// is read tells in its error, and the reason the account isn't read, see the
// gap* reasons, logged.
func (p *openPositions) readBridge(ctx context.Context, account liveAccount, what string,
	read func(token string) error) (string, error) {
	var err error
	for _, token := range p.tokens(account) {
		err = read(token)
		var refused *refusedToken
		if err == nil || !errors.As(err, &refused) {
			break
		}
	}
	if err == nil {
		return "", nil
	}
	log.Warnf("ui: %s of the agents on the bridge %s: %v", what, account.bridge, err)
	var refused *refusedToken
	var failed *bridgeFailure
	switch {
	case errors.As(err, &refused):
		return gapRefused, err
	case errors.As(err, &failed):
		return gapFailed, err
	}
	return gapUnreachable, err
}

// tokens returns the distinct tokens of the bridge of the agents on account,
// none ("") when no agent has one: a bridge may take none.
func (p *openPositions) tokens(account liveAccount) []string {
	var tokens []string
	for _, a := range account.agents {
		secrets, err := p.store.SecretsOf(a.agent)
		if err != nil {
			log.Warnf("ui: the token of the bridge of the agent %s: %v", a.agent.Name, err)
			continue
		}
		if secrets.BridgeToken != "" && !slices.Contains(tokens, secrets.BridgeToken) {
			tokens = append(tokens, secrets.BridgeToken)
		}
	}
	if len(tokens) == 0 {
		tokens = []string{""}
	}
	return tokens
}

// refusedToken is a request the bridge refused for its token (401 or 403).
type refusedToken struct{ status int }

func (e *refusedToken) Error() string { return fmt.Sprintf("the token refused (%d)", e.status) }

// bridgeFailure is an answer of the bridge that isn't the one asked for: an
// error status, or a payload that isn't one.
type bridgeFailure struct {
	message string
	status  int // the HTTP status of the answer, 0 for an answer not understood
}

func (e *bridgeFailure) Error() string { return e.message }

// get reads path of the bridge at base into out, with token, if any.
func (p *openPositions) get(ctx context.Context, base, path, token string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set(exchange.MT5TokenHeader, token)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	switch {
	case err != nil:
		return err
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return &refusedToken{status: response.StatusCode}
	case response.StatusCode < 200 || response.StatusCode > 299:
		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &answer)
		return &bridgeFailure{message: fmt.Sprintf("GET %s: %d %s", path, response.StatusCode, answer.Error),
			status: response.StatusCode}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &bridgeFailure{message: fmt.Sprintf("GET %s: invalid answer: %v", path, err)}
	}
	return nil
}

// sum sums the positions of the agents of the accounts read, by the currency
// of their account (none converted): each position once by its ticket on its
// account (its login, the bridge without one), the one of the agent of its
// magic number trading its symbol first, else of the first agent of its magic
// number, the others not of the agents left out. Every account read has a
// total in its currency, 0 without a position, and its balance, once; one not
// read makes the sum incomplete.
func (p *openPositions) sum(accounts []liveAccount, reads []accountRead) ui.OpenPositionsResponse {
	response := ui.OpenPositionsResponse{Totals: []ui.CurrencyAmount{}, Balances: []ui.CurrencyAmount{},
		Complete: true, Positions: []ui.OpenPosition{}, At: time.Now().Unix()}
	order := make(map[string]int) // the place of the agents on the dashboard
	seen := make(map[string]bool)
	counted := make(map[string]bool) // the accounts of the balances
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
		if key := read.account.key(account.bridge); !counted[key] {
			counted[key] = true
			response.Accounts++
			amountIn(&response.Balances, currency).Amount += read.account.Balance
		}
		for _, position := range read.positions {
			owner, ok := ownerOf(account.agents, position.Magic, position.Symbol)
			if !ok {
				continue // not of an agent, eg: by hand
			}
			key := read.account.key(account.bridge) + "/" + fmt.Sprint(position.Ticket)
			if seen[key] {
				continue
			}
			seen[key] = true
			profit := position.Profit + position.Swap
			side := "BUY"
			if position.Type == 1 {
				side = "SELL"
			}
			response.Positions = append(response.Positions, ui.OpenPosition{Agent: owner.Name, Pair: position.Symbol,
				Side: side, Volume: position.Volume, Entry: position.PriceOpen, Price: position.PriceCurrent,
				StopLoss: position.SL, TakeProfit: position.TP, Profit: profit, Currency: currency,
				Ticket: position.Ticket, Running: p.running(owner.Name)})
			total.Amount += profit
		}
	}
	slices.SortStableFunc(response.Positions, func(a, b ui.OpenPosition) int {
		return cmp.Or(cmp.Compare(order[a.Agent], order[b.Agent]), strings.Compare(a.Agent, b.Agent),
			strings.Compare(a.Pair, b.Pair), cmp.Compare(a.Ticket, b.Ticket))
	})
	return response
}

// running reports whether the agent of name runs.
func (p *openPositions) running(name string) bool {
	return p.manager != nil && p.manager.State(name).Running
}

// amountIn returns the amount of amounts in currency, added at their end, 0,
// when missing.
func amountIn(amounts *[]ui.CurrencyAmount, currency string) *ui.CurrencyAmount {
	for i := range *amounts {
		if (*amounts)[i].Currency == currency {
			return &(*amounts)[i]
		}
	}
	*amounts = append(*amounts, ui.CurrencyAmount{Currency: currency})
	return &(*amounts)[len(*amounts)-1]
}

// gapOf returns the gap of account, not read for reason: its agents.
func gapOf(account liveAccount, reason string) ui.OpenPositionsGap {
	names := make([]string, 0, len(account.agents))
	for _, a := range account.agents {
		names = append(names, a.agent.Name)
	}
	return ui.OpenPositionsGap{Agents: names, Reason: reason}
}

// ownerOf returns the agent of agents a position of magic on symbol is of, by
// its magic number: the one trading its symbol, the enabled ones first, else
// the first one; false for none, eg: a position opened by hand.
func ownerOf(agents []bookedAgent, magic int64, symbol string) (store.Agent, bool) {
	var owner *bookedAgent
	rank := -1
	for i := range agents {
		a := &agents[i]
		if a.book.Magic != magic {
			continue
		}
		r := 0
		if slices.ContainsFunc(a.book.Pairs, func(pair string) bool { return strings.EqualFold(pair, symbol) }) {
			r += 2
		}
		if a.agent.Enabled {
			r++
		}
		if r > rank {
			owner, rank = a, r
		}
	}
	if owner == nil {
		return store.Agent{}, false
	}
	return owner.agent, true
}
