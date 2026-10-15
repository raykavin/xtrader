package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/store"
)

// The values of the trade requests of MT5 the supervisor sends to a bridge,
// the ones of the adapter (see exchange.MT5).
const (
	mt5ActionDeal     = 1  // TRADE_ACTION_DEAL: a deal at the market, closing a position by its ticket
	mt5ActionRemove   = 8  // TRADE_ACTION_REMOVE: a pending order removed
	mt5FillFOK        = 0  // ORDER_FILLING_FOK
	mt5FillIOC        = 1  // ORDER_FILLING_IOC
	mt5FillReturn     = 2  // ORDER_FILLING_RETURN
	mt5FillingFlagFOK = 1  // SYMBOL_FILLING_FOK
	mt5FillingFlagIOC = 2  // SYMBOL_FILLING_IOC
	mt5Deviation      = 20 // points, the default of the adapter
	mt5TradeModeReal  = 2  // ACCOUNT_TRADE_MODE_REAL
)

// The retcodes of a trade request done (TRADE_RETCODE_DONE, PLACED,
// DONE_PARTIAL).
var mt5Done = map[int]bool{10008: true, 10009: true, 10010: true}

// bridgeTradeTimeout bounds a trade request of the supervisor: the bridge never
// sends it to the terminal after (X-Bridge-Timeout).
const bridgeTradeTimeout = 10 * time.Second

// bridgeTrader closes the positions and removes the pending orders of the
// agents not running on their bridges, for the supervisor: the request sent
// once, its outcome unknown when its answer is lost, never sent again.
type bridgeTrader struct {
	client *http.Client
}

func newBridgeTrader() *bridgeTrader {
	return &bridgeTrader{client: &http.Client{Timeout: bridgeTradeTimeout + 5*time.Second}}
}

// bridgeAgent is the agent whose positions and orders the supervisor operates
// on its bridge: the magic number and the comment of its orders, and whether
// it may trade a real account.
type bridgeAgent struct {
	magic       int64
	comment     string
	realAllowed bool
}

// bridgeAgentOf returns the agent a as the supervisor operates it on its
// bridge.
func bridgeAgentOf(a store.Agent) bridgeAgent {
	book := a.MT5Book()
	return bridgeAgent{magic: book.Magic, comment: book.Comment, realAllowed: a.Account != nil &&
		a.Account.RealAllowed}
}

// close closes the position p, of agent, by its ticket at the market, on the
// bridge at base with token: done, failed (refused, nothing done) or unknown
// (maybe done: to check on the account, never sent again), and why.
func (b *bridgeTrader) close(ctx context.Context, base, token string, p bridgePosition, agent bridgeAgent) (string,
	string) {
	if err := b.checkAccount(ctx, base, token, agent); err != nil {
		return itemFailed, err.Error()
	}
	var symbol struct {
		Digits      int `json:"digits"`
		FillingMode int `json:"filling_mode"`
	}
	if err := b.get(ctx, base, "/symbol?"+url.Values{"symbol": {p.Symbol}}.Encode(), token, &symbol); err != nil {
		return itemFailed, "o símbolo não pôde ser lido: " + err.Error()
	}
	var tick struct {
		Bid float64 `json:"bid"`
		Ask float64 `json:"ask"`
	}
	if err := b.get(ctx, base, "/tick?"+url.Values{"symbol": {p.Symbol}}.Encode(), token, &tick); err != nil {
		return itemFailed, "o preço não pôde ser lido: " + err.Error()
	}
	kind, price := 1, tick.Bid // a long closes with a sell at the bid
	if p.Type == 1 {
		kind, price = 0, tick.Ask
	}
	filling := mt5FillReturn
	switch {
	case symbol.FillingMode&mt5FillingFlagFOK != 0:
		filling = mt5FillFOK
	case symbol.FillingMode&mt5FillingFlagIOC != 0:
		filling = mt5FillIOC
	}
	scale := math.Pow10(max(symbol.Digits, 0))
	request := map[string]any{"action": mt5ActionDeal, "symbol": p.Symbol, "volume": p.Volume, "type": kind,
		"position": p.Ticket, "price": math.Round(price*scale) / scale, "deviation": mt5Deviation,
		"magic": agent.magic, "comment": agent.comment, "type_filling": filling, "type_time": 0}
	return b.trade(ctx, base, token, request)
}

// remove removes the pending order o of agent on the bridge at base with
// token, see close.
func (b *bridgeTrader) remove(ctx context.Context, base, token string, o bridgeOrder, agent bridgeAgent) (string,
	string) {
	if err := b.checkAccount(ctx, base, token, agent); err != nil {
		return itemFailed, err.Error()
	}
	return b.trade(ctx, base, token, map[string]any{"action": mt5ActionRemove, "order": o.Ticket})
}

// checkAccount refuses a real account an agent may not trade.
func (b *bridgeTrader) checkAccount(ctx context.Context, base, token string, agent bridgeAgent) error {
	var account struct {
		Login     int64 `json:"login"`
		TradeMode int   `json:"trade_mode"`
	}
	if err := b.get(ctx, base, "/account", token, &account); err != nil {
		return fmt.Errorf("a conta não pôde ser lida: %w", err)
	}
	if account.TradeMode == mt5TradeModeReal && !agent.realAllowed {
		return errors.New("conta real, que o agente não tem permissão de operar: nada enviado")
	}
	return nil
}

// trade sends the trade request once, see close.
func (b *bridgeTrader) trade(ctx context.Context, base, token string, request map[string]any) (string, string) {
	data, err := json.Marshal(request)
	if err != nil {
		return itemFailed, err.Error()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/order",
		bytes.NewReader(data))
	if err != nil {
		return itemFailed, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(exchange.MT5TimeoutHeader, strconv.FormatInt(bridgeTradeTimeout.Milliseconds(), 10))
	if token != "" {
		req.Header.Set(exchange.MT5TokenHeader, token)
	}
	response, err := b.client.Do(req)
	if err != nil {
		log.Errorf("supervisor: a trade request to the bridge %s, its outcome unknown: %v", base, err)
		return itemUnknown, "sem resposta da bridge: confira na conta, nada é reenviado"
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		var answer struct {
			Error string `json:"error"`
			Sent  *bool  `json:"sent"`
		}
		_ = json.Unmarshal(body, &answer)
		if answer.Sent != nil && !*answer.Sent || response.StatusCode < 500 {
			return itemFailed, fmt.Sprintf("recusado pela bridge (%d): %s", response.StatusCode, answer.Error)
		}
		return itemUnknown, fmt.Sprintf("a bridge falhou (%d) depois de enviar: confira na conta, nada é "+
			"reenviado: %s", response.StatusCode, answer.Error)
	}
	var result struct {
		Retcode int    `json:"retcode"`
		Comment string `json:"comment"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return itemUnknown, "resposta ilegível da bridge: confira na conta, nada é reenviado"
	}
	if !mt5Done[result.Retcode] {
		return itemFailed, fmt.Sprintf("recusado pelo terminal (retcode %d): %s", result.Retcode, result.Comment)
	}
	return itemDone, ""
}

// get reads path of the bridge at base into out, with token, if any.
func (b *bridgeTrader) get(ctx context.Context, base, path, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set(exchange.MT5TokenHeader, token)
	}
	response, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %d", path, response.StatusCode)
	}
	return json.Unmarshal(body, out)
}
