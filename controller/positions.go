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
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*bridgeTimeout+time.Second)
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
			reads[i] = p.readAccount(ctx, account)
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

// bridgeAccount is the account of a bridge, of GET /account: its login and its
// currency.
type bridgeAccount struct {
	Login    int64  `json:"login"`
	Currency string `json:"currency"`
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
	Swap         float64 `json:"swap"`
	Profit       float64 `json:"profit"`
	Symbol       string  `json:"symbol"`
}

// accountRead is what came of reading an account: its positions, or the
// reason it couldn't be read.
type accountRead struct {
	account   bridgeAccount
	positions []bridgePosition
	gap       string // why the account isn't read, see the gap* reasons
	err       error
}

// readAccount reads the account and the positions of the bridge of account,
// with the token of each agent on it in turn until one is taken, without any
// when none has one.
func (p *openPositions) readAccount(ctx context.Context, account liveAccount) accountRead {
	tokens := p.tokens(account)
	var read accountRead
	for _, token := range tokens {
		read = accountRead{}
		if read.err = p.get(ctx, account.bridge, "/account", token, &read.account); read.err == nil {
			read.err = p.get(ctx, account.bridge, "/positions", token, &read.positions)
		}
		var refused *refusedToken
		if read.err == nil || !errors.As(read.err, &refused) {
			break
		}
	}
	var refused *refusedToken
	var failed *bridgeFailure
	switch {
	case read.err == nil:
	case errors.As(read.err, &refused):
		read.gap = gapRefused
	case errors.As(read.err, &failed):
		read.gap = gapFailed
	default:
		read.gap = gapUnreachable
	}
	if read.err != nil {
		log.Warnf("ui: the open positions of the agents on the bridge %s: %v", account.bridge, read.err)
	}
	return read
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
type bridgeFailure struct{ message string }

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
		return &bridgeFailure{message: fmt.Sprintf("GET %s: %d %s", path, response.StatusCode, answer.Error)}
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
// total in its currency, 0 without a position; one not read makes the sum
// incomplete.
func (p *openPositions) sum(accounts []liveAccount, reads []accountRead) ui.OpenPositionsResponse {
	response := ui.OpenPositionsResponse{Totals: []ui.CurrencyAmount{}, Complete: true,
		Positions: []ui.OpenPosition{}, At: time.Now().Unix()}
	order := make(map[string]int) // the place of the agents on the dashboard
	seen := make(map[string]bool)
	total := func(currency string) *ui.CurrencyAmount {
		for i := range response.Totals {
			if response.Totals[i].Currency == currency {
				return &response.Totals[i]
			}
		}
		response.Totals = append(response.Totals, ui.CurrencyAmount{Currency: currency})
		return &response.Totals[len(response.Totals)-1]
	}
	for i, account := range accounts {
		for _, a := range account.agents {
			order[a.agent.Name] = a.agent.Position
		}
		read := reads[i]
		if read.gap != "" {
			names := make([]string, 0, len(account.agents))
			for _, a := range account.agents {
				names = append(names, a.agent.Name)
			}
			response.Complete = false
			response.Missing = append(response.Missing, ui.OpenPositionsGap{Agents: names, Reason: read.gap})
			continue
		}
		currency := strings.ToUpper(strings.TrimSpace(read.account.Currency))
		total(currency)
		for _, position := range read.positions {
			owner, ok := ownerOf(account.agents, position)
			if !ok {
				continue // not of an agent, eg: by hand
			}
			key := fmt.Sprintf("%d/%d", read.account.Login, position.Ticket)
			if read.account.Login == 0 {
				key = account.bridge + "/" + fmt.Sprint(position.Ticket)
			}
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
				Profit: profit, Currency: currency, Ticket: position.Ticket,
				Running: p.manager != nil && p.manager.State(owner.Name).Running})
			total(currency).Amount += profit
		}
	}
	slices.SortStableFunc(response.Positions, func(a, b ui.OpenPosition) int {
		return cmp.Or(cmp.Compare(order[a.Agent], order[b.Agent]), strings.Compare(a.Agent, b.Agent),
			strings.Compare(a.Pair, b.Pair), cmp.Compare(a.Ticket, b.Ticket))
	})
	return response
}

// ownerOf returns the agent of agents a position is of, by its magic number:
// the one trading its symbol, the enabled ones first, else the first one;
// false for none, eg: a position opened by hand.
func ownerOf(agents []bookedAgent, position bridgePosition) (store.Agent, bool) {
	var owner *bookedAgent
	rank := -1
	for i := range agents {
		a := &agents[i]
		if a.book.Magic != position.Magic {
			continue
		}
		r := 0
		if slices.ContainsFunc(a.book.Pairs, func(pair string) bool { return strings.EqualFold(pair, position.Symbol) }) {
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
