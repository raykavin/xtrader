package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/tools/log"
)

// The MT5 adapter is split in three files: this one (the options, NewMT5, the
// HTTP client of the bridge, the symbols, the sizes and the conversions),
// mt5_market.go (the candles and the quotes) and mt5_trading.go (the book of
// the account, the netting and every order method).

var (
	_ service.Exchange         = (*MT5)(nil)
	_ service.ProtectionBroker = (*MT5)(nil)
	_ service.OrderHistory     = (*MT5)(nil)
	_ service.ProtectedCloser  = (*MT5)(nil)
	_ service.PositionReporter = (*MT5)(nil)
)

const (
	// MT5DefaultBaseURL is the address of the bridge by default, the one
	// tools/mt5bridge/mt5bridge.py listens at.
	MT5DefaultBaseURL = "http://127.0.0.1:18812"
	// MT5TokenHeader is the header carrying the token of the bridge, see
	// WithMT5Token.
	MT5TokenHeader = "X-Bridge-Token"
	// MT5DeadlineHeader is the header carrying the deadline of a trade request,
	// in Unix milliseconds: the bridge never sends it to the terminal after it,
	// refusing it with "sent": false.
	MT5DeadlineHeader = "X-Bridge-Deadline"
	// MT5TimeoutHeader is the header carrying the same deadline as the time
	// left until it when the request is sent, in milliseconds: the bridge takes
	// the earliest of the two, this one counted by its own clock from the
	// receipt of the request, so that a clock of the bridge behind the one of
	// the agent, eg: on another host, never makes it send a request later.
	MT5TimeoutHeader = "X-Bridge-Timeout"

	// mt5TradeDeadline is how long the bridge may hold a trade request before
	// sending it to the terminal, eg: behind the requests of other bots or while
	// the terminal reconnects; below the timeout of the HTTP client (30s), so
	// that the bridge gives up before the adapter does.
	mt5TradeDeadline = 20 * time.Second
	// mt5ReconcileMargin is how long after the deadline of a trade request
	// whose answer was lost it is looked for the last time, see reconcile.
	mt5ReconcileMargin = 2 * time.Second
	// mt5ReconcileReads bounds the reads of the account of the last look of
	// reconcile, and of each look of awaitOutcome.
	mt5ReconcileReads = 10 * time.Second
	// mt5UnsettledRetry is the longest wait between two looks of awaitOutcome.
	mt5UnsettledRetry = 30 * time.Second
	// mt5ClockAhead is how far ahead of the clock of the agent the one of the
	// bridge is warned about: it shortens the deadline of the trade requests.
	mt5ClockAhead = time.Second

	// mt5DefaultPoll is the default of WithMT5PollInterval.
	mt5DefaultPoll = time.Second
	// mt5DefaultDeviation is the default of WithMT5Deviation, in points.
	mt5DefaultDeviation = 20
	// mt5DefaultComment is the comment of the bot's orders without
	// WithMT5Comment.
	mt5DefaultComment = "xtrader"
	// mt5MaxComment is the longest comment MT5 keeps: longer ones are cut.
	mt5MaxComment = 31

	// mt5MaxRates is the most candles one /rates request returns (MAX_RATES of
	// the bridge).
	mt5MaxRates = 100000
	// mt5RangeRates is the most periods of one /rates range request of
	// CandlesByPeriod, so a page is never truncated by the terminal.
	mt5RangeRates = 10000

	// mt5Retries is how many times a GET failing with a transport error or an
	// HTTP 5xx is sent again, waiting retryDelay more each time. The trading
	// requests (POST) are never sent again: they may have been executed.
	mt5Retries    = 3
	mt5RetryDelay = 250 * time.Millisecond

	// mt5SyncWait bounds how long CandlesByLimit waits for the terminal to
	// sync the history of a symbol, see awaitHistory, polled every
	// mt5SyncPoll.
	mt5SyncWait = 30 * time.Second
	mt5SyncPoll = time.Second

	// mt5CloseDelay is how long after its period a candle is complete, leaving
	// time for its last ticks to be recorded, when the terminal confirms it
	// (confirmsClose); mt5HeldWarn after it, one it doesn't is logged.
	mt5CloseDelay = 2 * time.Second
	mt5HeldWarn   = 10 * time.Second

	// The clock of the bot ahead of the trade server's, see clockAhead: told
	// by the ticks read, the min of the last mt5SkewSamples taken within
	// mt5SkewKeep, each of a tick new since a read mt5SkewGap polls before at
	// most; more than mt5SkewMax ahead is no clock, eg: a server on another
	// time zone, left alone.
	mt5SkewSamples = 20
	mt5SkewKeep    = 30 * time.Minute
	mt5SkewGap     = 2
	mt5SkewMax     = 10 * time.Minute

	// mt5HistoryLookback is how far back the deals and the orders of the
	// history are read on startup; OrderSince reads further back when asked.
	mt5HistoryLookback = 7 * 24 * time.Hour
	// mt5HistoryOverlap is how much each history read overlaps the previous
	// one, as the records of the last moments may come late.
	mt5HistoryOverlap = time.Minute
	// mt5HistoryAhead is how far after now the history reads end, so that a
	// server clock ahead of the bot's never hides the last records.
	mt5HistoryAhead = 24 * time.Hour
	// mt5ResolveWait is how long a pending order or a position gone from the
	// book waits for its outcome in the history, read again every poll
	// meanwhile, before it is settled without one. The exits of the bot's
	// positions are read for longer, see awaitedExits.
	mt5ResolveWait = time.Minute
	// mt5AwaitCeiling is the longest wait between two reads of the history for
	// the exits awaited after mt5ResolveWait, and mt5AwaitMax how long they are
	// awaited at most, see awaitedExits.
	mt5AwaitCeiling = 5 * time.Minute
	mt5AwaitMax     = 24 * time.Hour
	// mt5SettleRetry is how long the netting of a pair waits after a failure
	// before the poll tries it again.
	mt5SettleRetry = 30 * time.Second
	// mt5HistoryKept is how long the book keeps the history, pruned every
	// mt5PruneInterval.
	mt5HistoryKept   = 30 * 24 * time.Hour
	mt5PruneInterval = time.Hour

	// mt5UnlimitedLeverage is the leverage an "unlimited" account reports; it
	// counts as mt5MaxLeverage, the highest finite one.
	mt5UnlimitedLeverage = 2000000000
	mt5MaxLeverage       = 2000

	// mt5HashBit is set on every ExchangeID derived from a hash (the legs and
	// their groups): MT5 tickets are far below it, so they never collide.
	mt5HashBit = int64(1) << 62
	// mt5MaxProtections is how many protections of a position are looked up
	// when rebuilding a leg of a previous run, see mt5LegID.
	mt5MaxProtections = 64
)

// The MT5 enums of the MetaTrader5 Python package.
const (
	// order types (ORDER_TYPE_*), the deals and the positions use 0 and 1
	mt5TypeBuy       = 0
	mt5TypeSell      = 1
	mt5TypeBuyLimit  = 2
	mt5TypeSellLimit = 3
	mt5TypeBuyStop   = 4
	mt5TypeSellStop  = 5
	mt5TypeCloseBy   = 8

	// trade actions (TRADE_ACTION_*)
	mt5ActionDeal    = 1
	mt5ActionPending = 5
	mt5ActionSLTP    = 6
	mt5ActionModify  = 7
	mt5ActionRemove  = 8
	mt5ActionCloseBy = 10

	// filling (ORDER_FILLING_*) and the flags of the symbol filling_mode
	mt5FillFOK        = 0
	mt5FillIOC        = 1
	mt5FillReturn     = 2
	mt5FillingFlagFOK = 1
	mt5FillingFlagIOC = 2

	// ORDER_TIME_GTC
	mt5TimeGTC = 0

	// order states (ORDER_STATE_*)
	mt5StateStarted  = 0
	mt5StatePlaced   = 1
	mt5StateCanceled = 2
	mt5StatePartial  = 3
	mt5StateFilled   = 4
	mt5StateRejected = 5
	mt5StateExpired  = 6

	// deal entries (DEAL_ENTRY_*)
	mt5EntryIn    = 0
	mt5EntryOut   = 1
	mt5EntryInOut = 2
	mt5EntryOutBy = 3

	// deal reasons (DEAL_REASON_*)
	mt5ReasonClient = 0
	mt5ReasonExpert = 3
	mt5ReasonSL     = 4
	mt5ReasonTP     = 5
	mt5ReasonSO     = 6

	// account trade_mode and margin_mode
	mt5AccountReal   = 2
	mt5MarginHedging = 2

	// symbol trade_mode and the close-by flag of order_mode
	mt5SymbolDisabled = 0
	mt5SymbolFull     = 4
	mt5OrderCloseBy   = 64
)

// The MT5 retcodes of the trade requests.
const (
	mt5CodeRequote       = 10004
	mt5CodeReject        = 10006
	mt5CodePlaced        = 10008
	mt5CodeDone          = 10009
	mt5CodeDonePartial   = 10010
	mt5CodeInvalid       = 10013
	mt5CodeInvalidVolume = 10014
	mt5CodeInvalidPrice  = 10015
	mt5CodeInvalidStops  = 10016
	mt5CodeMarketClosed  = 10018
	mt5CodeNoMoney       = 10019
	mt5CodeNoChanges     = 10025
	mt5CodeFrozen        = 10029
	mt5CodeInvalidFill   = 10030
)

// The legs of an OCO order: the take profit and the stop loss levels of a
// position, or of the pending order opening it.
const (
	mt5TakeProfit = "tp"
	mt5StopLoss   = "sl"
)

var (
	// ErrMT5AccountChanged is the error of the trading calls while the terminal
	// is logged in another account than the one NewMT5 connected to, or the
	// account turned real without WithMT5Real, eg: switched by hand.
	ErrMT5AccountChanged = errors.New("mt5: the terminal is not logged in the account of the bot")
	// ErrMT5OutcomeUnknown is the error of a trade request whose answer was lost
	// and that the adapter could neither find in the account nor confirm as not
	// executed: it may have been executed, so it must not be sent again before
	// the account is checked.
	ErrMT5OutcomeUnknown = errors.New("mt5: the outcome of the trade request is unknown")
	// ErrMT5Unsettled is the error of a trade request opening or closing a
	// position of a pair while the outcome of another one of the pair is
	// unknown, the account unreadable since (see ErrMT5OutcomeUnknown): not
	// sent, until a read of the account tells it.
	ErrMT5Unsettled = errors.New("mt5: the outcome of a trade request of the pair is unknown yet")
)

// MT5Error is an error answer of the bridge (HTTP status, message, and the
// last_error code of the terminal when it has one), or a trade request the
// terminal did not execute (HTTP 200, the retcode and comment of order_send).
type MT5Error struct {
	Method  string
	Path    string
	Status  int    // HTTP status of the answer
	Retcode int    // trade retcode, or the terminal error code, 0 when unknown
	Message string // error of the bridge, or comment of the trade result
	NotSent bool   // a trade request the bridge refused before order_send ("sent": false)
}

func (e *MT5Error) Error() string {
	message := fmt.Sprintf("mt5: %s %s", e.Method, e.Path)
	if e.Status != http.StatusOK {
		message += fmt.Sprintf(": HTTP %d", e.Status)
	}
	if e.Retcode != 0 {
		message += fmt.Sprintf(": retcode %d", e.Retcode)
		if name := mt5RetcodeName(e.Retcode); name != "" {
			message += " (" + name + ")"
		}
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

// Unwrap maps the retcodes to the errors of the package: invalid volume to
// ErrInvalidQuantity, invalid stops to ErrInvalidProtection, no money to
// ErrInsufficientFunds, a bridge refusing the trading (read-only, HTTP 403)
// to ErrNotSupported, and refusing it on another account (HTTP 409) to
// ErrMT5AccountChanged.
func (e *MT5Error) Unwrap() error {
	switch {
	case e.Retcode == mt5CodeInvalidVolume:
		return ErrInvalidQuantity
	case e.Retcode == mt5CodeInvalidStops:
		return ErrInvalidProtection
	case e.Retcode == mt5CodeNoMoney:
		return ErrInsufficientFunds
	case e.Status == http.StatusForbidden:
		return ErrNotSupported
	case e.Status == http.StatusConflict:
		return ErrMT5AccountChanged
	}
	return nil
}

func mt5RetcodeName(code int) string {
	switch code {
	case mt5CodeRequote:
		return "requote"
	case mt5CodeReject:
		return "rejected"
	case mt5CodeInvalid:
		return "invalid request"
	case mt5CodeInvalidVolume:
		return "invalid volume"
	case mt5CodeInvalidPrice:
		return "invalid price"
	case mt5CodeInvalidStops:
		return "invalid stops"
	case mt5CodeMarketClosed:
		return "market closed"
	case mt5CodeNoMoney:
		return "no money"
	case mt5CodeFrozen:
		return "frozen"
	case mt5CodeInvalidFill:
		return "invalid filling"
	}
	return ""
}

// MT5 trades a MetaTrader 5 account through tools/mt5bridge, a local HTTP
// bridge running with the terminal, logged in the account, which wraps the
// official MetaTrader5 Python package.
//
// The pairs are the symbols of the terminal, eg: XAUUSDm, quoted in their
// profit currency (currency_profit) whatever the currency of the account.
// Every quantity crossing the adapter is in units of the base asset (units =
// lots x trade_contract_size, eg: 100 ounces in a lot of XAUUSDm), floored to
// the volume step; see ContractSize, Lots and Units.
//
// The account must be hedging, the only mode of Exness. The adapter nets the
// positions of the bot, the ones of its magic number (WithMT5Magic), as the
// order controller expects: a market order against the bot's net closes its
// opposite positions first in, first out, and a pending order filled against
// it is closed by them (close-by). Manual trades and the other bots, of other
// magic numbers, are ignored, so they share the account.
//
// Candles are bid prices, as MT5 draws them, timed at their open in UTC: the
// server time of the account must be UTC, as the one of Exness.
// CandlesSubscription polls the latest candles every poll interval.
//
// The positions and the pending orders are polled every poll interval into a
// local book, and the history (deals and orders) is read when something left
// the book: Order and OrderSince, asked every second by the order controller,
// answer from the book.
//
// A trade request is sent once, with a deadline the bridge never sends it to
// the terminal after; one whose answer is lost is looked for in the account
// before failing (reconcile), the trading of its pair only waiting meanwhile.
// While the terminal is logged in another account
// than the one NewMT5 connected to, or the account turned real without
// WithMT5Real, nothing is traded (ErrMT5AccountChanged).
type MT5 struct {
	ctx          context.Context
	client       *http.Client
	baseURL      string
	token        string
	symbols      []string
	magic        int64
	comment      string
	allowReal    bool
	dataOnly     bool
	leverage     bool // positions commit their margin, see WithMT5Leverage
	pollInterval time.Duration
	deviation    int
	retryDelay   time.Duration
	syncWait     time.Duration // see awaitHistory
	resolveWait  time.Duration // mt5ResolveWait but in the tests
	overlap      time.Duration // mt5HistoryOverlap but in the tests
	deadline     time.Duration // of the trade requests, see tradeDeadline
	margin       time.Duration // after their deadline, see reconcile
	deadlines    bool          // the bridge honors MT5DeadlineHeader (GET /health)
	timeouts     bool          // the bridge honors MT5TimeoutHeader (GET /health)

	login       int64
	currency    string
	demo        bool
	accountLv   float64
	suffixes    []string             // of the symbol names, eg: "m" for XAUUSDm
	instruments map[string]mt5Symbol // the symbols traded
	assetsInfo  map[string]model.AssetInfo

	catalogMtx sync.Mutex
	catalog    map[string]*mt5Symbol // every symbol looked up, nil when missing, by name

	conversionsMtx sync.Mutex
	conversions    map[string]mt5Conversion // symbols converting currencies, by from + to

	quotesMtx sync.Mutex
	quotes    map[string]mt5Quote // last ticks read, by symbol
	skews     []mt5Skew           // how far the clock of the bot looks ahead, see clockAhead

	healthMtx  sync.Mutex
	health     mt5Seen // the last GET /health, see confirmsClose
	healthBusy bool    // a GET /health of askHealth in flight

	loadedMtx sync.Mutex
	loaded    map[string]time.Time // last complete candle returned by CandlesByLimit, by pair and period

	costRatesMtx sync.Mutex
	costRates    map[string]mt5CostRate // the account currency in the quote one, see PositionCosts

	book *mt5Book // the account, see mt5_trading.go
}

// MT5Option configures NewMT5.
type MT5Option func(*MT5)

// WithMT5BaseURL sets the address of the bridge, MT5DefaultBaseURL by default.
func WithMT5BaseURL(baseURL string) MT5Option {
	return func(m *MT5) {
		m.baseURL = strings.TrimSuffix(baseURL, "/")
	}
}

// WithMT5Token sends the token the bridge requires (its --token) in the
// X-Bridge-Token header.
func WithMT5Token(token string) MT5Option {
	return func(m *MT5) {
		m.token = token
	}
}

// WithMT5Symbols sets the symbols traded, eg: XAUUSDm, BTCUSDm.
func WithMT5Symbols(symbols ...string) MT5Option {
	return func(m *MT5) {
		m.symbols = append(m.symbols, symbols...)
	}
}

// WithMT5Magic sets the magic number of the bot, required to trade: only the
// positions and orders of this magic number are the bot's, so bots of magic
// numbers of their own share an account, next to the manual trades (magic 0).
func WithMT5Magic(magic int64) MT5Option {
	return func(m *MT5) {
		m.magic = magic
	}
}

// WithMT5Comment sets the comment of the bot's orders, a readable tag of at
// most 31 characters, "xtrader" by default. Ownership is decided by the magic
// number, not by the comment. Each order is commented with it followed by "#"
// and a tag of its own, eg: "xtrader#k3f9a2", cut to make room when needed:
// an order whose answer is lost is found by it (see trade).
func WithMT5Comment(comment string) MT5Option {
	return func(m *MT5) {
		m.comment = comment
	}
}

// WithMT5Real allows a real account, refused otherwise.
func WithMT5Real() MT5Option {
	return func(m *MT5) {
		m.allowReal = true
	}
}

// WithMT5DataOnly reads the market data only, eg: for the paper mode or a
// download: the adapter starts no book, never nets nor closes a position, and
// every trading call returns ErrNotSupported before any request.
func WithMT5DataOnly() MT5Option {
	return func(m *MT5) {
		m.dataOnly = true
	}
}

// WithMT5Leverage trades with the leverage of the account: an open position
// commits its margin rather than its whole value, and Position returns the free
// margin for new positions. Without it, positions commit their whole value, as
// on a spot market.
func WithMT5Leverage() MT5Option {
	return func(m *MT5) {
		m.leverage = true
	}
}

// WithMT5PollInterval sets how often the positions and pending orders are
// polled, and CandlesSubscription the candles, a second by default.
func WithMT5PollInterval(interval time.Duration) MT5Option {
	return func(m *MT5) {
		m.pollInterval = interval
	}
}

// WithMT5Deviation sets the deviation of the market orders, the most the fill
// may slip from the requested price, in points, 20 by default.
func WithMT5Deviation(points int) MT5Option {
	return func(m *MT5) {
		m.deviation = points
	}
}

// WithMT5HTTPClient sets the HTTP client of the requests to the bridge, one with
// a 30s timeout by default.
func WithMT5HTTPClient(client *http.Client) MT5Option {
	return func(m *MT5) {
		if client != nil {
			m.client = client
		}
	}
}

// withMT5RetryDelay sets the wait between the retries of a GET, for the tests.
// withMT5SyncWait sets how long CandlesByLimit waits for the history of the
// terminal, mt5SyncWait by default: for the tests.
func withMT5SyncWait(wait time.Duration) MT5Option {
	return func(m *MT5) {
		m.syncWait = wait
	}
}

func withMT5RetryDelay(delay time.Duration) MT5Option {
	return func(m *MT5) {
		m.retryDelay = delay
	}
}

// withMT5HistoryWaits sets mt5ResolveWait and mt5HistoryOverlap: for the
// tests.
func withMT5HistoryWaits(resolveWait, overlap time.Duration) MT5Option {
	return func(m *MT5) {
		m.resolveWait, m.overlap = resolveWait, overlap
	}
}

// withMT5TradeDeadline sets the deadline of the trade requests and the margin
// of reconcile after it, mt5TradeDeadline and mt5ReconcileMargin by default:
// for the tests.
func withMT5TradeDeadline(deadline, margin time.Duration) MT5Option {
	return func(m *MT5) {
		m.deadline, m.margin = deadline, margin
	}
}

// mt5Health is the answer of GET /health.
type mt5Health struct {
	Connected    bool    `json:"connected"`
	TradeAllowed bool    `json:"trade_allowed"`
	Build        int     `json:"build"`
	Login        int64   `json:"login"`
	Server       string  `json:"server"`
	ReadOnly     bool    `json:"read_only"`
	Deadline     bool    `json:"deadline"` // MT5DeadlineHeader honored
	Timeout      bool    `json:"timeout"`  // MT5TimeoutHeader honored
	Time         float64 `json:"time"`
}

// mt5Seen is a GET /health read: whether the terminal was connected to its
// server, between asked and answered.
type mt5Seen struct {
	connected       bool
	asked, answered time.Time
}

// mt5Account is the answer of GET /account (account_info).
type mt5Account struct {
	Login      int64   `json:"login"`
	TradeMode  int     `json:"trade_mode"` // 0 demo, 1 contest, 2 real
	Leverage   float64 `json:"leverage"`
	MarginMode int     `json:"margin_mode"` // 2 hedging
	Balance    float64 `json:"balance"`
	Equity     float64 `json:"equity"`
	Profit     float64 `json:"profit"`
	Margin     float64 `json:"margin"`
	MarginFree float64 `json:"margin_free"`
	Currency   string  `json:"currency"`
	Server     string  `json:"server"`
	Name       string  `json:"name"`
}

// mt5Symbol is the answer of GET /symbol (symbol_info).
type mt5Symbol struct {
	Name           string  `json:"name"`
	Digits         int     `json:"digits"`
	Point          float64 `json:"point"`
	ContractSize   float64 `json:"trade_contract_size"`
	VolumeMin      float64 `json:"volume_min"`
	VolumeStep     float64 `json:"volume_step"`
	VolumeMax      float64 `json:"volume_max"`
	TradeMode      int     `json:"trade_mode"`
	FillingMode    int     `json:"filling_mode"`
	ExecMode       int     `json:"trade_exemode"`
	OrderMode      int     `json:"order_mode"`
	StopsLevel     int     `json:"trade_stops_level"`  // points between the market and a new level or pending price
	FreezeLevel    int     `json:"trade_freeze_level"` // points from the market within which nothing is modified
	TickSize       float64 `json:"trade_tick_size"`    // the step of the prices, a multiple of point
	CurrencyBase   string  `json:"currency_base"`
	CurrencyProfit string  `json:"currency_profit"`
	CurrencyMargin string  `json:"currency_margin"`
	Bid            float64 `json:"bid"`
	Ask            float64 `json:"ask"`
	Spread         int     `json:"spread"`
}

// NewMT5 connects to the bridge, checks the terminal and the account, loads the
// symbols, registering them as pairs, and, unless WithMT5DataOnly, starts the
// book of the account, polled until ctx is done:
//
//  1. the options: at least one symbol, a positive poll interval, a comment of
//     1 to 31 printable characters not starting with "[" (MT5 comments the
//     exits by the levels "[sl x]"), and a positive magic number to trade;
//  2. GET /health: a terminal disconnected from its server fails, and one with
//     AlgoTrading off is a warning (every order would be refused), as is a
//     read-only bridge an error, when trading;
//  3. GET /account: a real account needs WithMT5Real; trading needs a hedging
//     account (margin_mode 2);
//  4. GET /symbol of every symbol: unknown ones are ErrInvalidAsset, disabled
//     ones fail when trading; each is registered (RegisterMT5Pair,
//     RegisterMT5Contract);
//  5. when trading, the symbols converting the account currency into the
//     quote of every symbol (Position), and the book: a first poll and the
//     history of mt5HistoryLookback, synchronous, then the netting of the fills
//     missed while the bot was off, and the poll loop.
func NewMT5(ctx context.Context, options ...MT5Option) (*MT5, error) {
	m := &MT5{
		ctx:          ctx,
		client:       &http.Client{Timeout: 30 * time.Second},
		baseURL:      MT5DefaultBaseURL,
		comment:      mt5DefaultComment,
		pollInterval: mt5DefaultPoll,
		deviation:    mt5DefaultDeviation,
		retryDelay:   mt5RetryDelay,
		syncWait:     mt5SyncWait,
		resolveWait:  mt5ResolveWait,
		overlap:      mt5HistoryOverlap,
		deadline:     mt5TradeDeadline,
		margin:       mt5ReconcileMargin,
		instruments:  make(map[string]mt5Symbol),
		assetsInfo:   make(map[string]model.AssetInfo),
		catalog:      make(map[string]*mt5Symbol),
		conversions:  make(map[string]mt5Conversion),
		quotes:       make(map[string]mt5Quote),
		loaded:       make(map[string]time.Time),
	}
	for _, option := range options {
		option(m)
	}
	m.symbols = mt5Unique(m.symbols)

	switch {
	case len(m.symbols) == 0:
		return nil, errors.New("mt5: no symbols, see WithMT5Symbols")
	case m.pollInterval <= 0:
		return nil, errors.New("mt5: the poll interval must be positive")
	case m.deviation < 0:
		return nil, errors.New("mt5: the deviation must not be negative")
	case !m.dataOnly && m.magic <= 0:
		return nil, errors.New("mt5: the magic number of the bot is required to trade, see WithMT5Magic")
	case m.magic < 0:
		return nil, errors.New("mt5: the magic number must not be negative")
	}
	if err := validMT5Comment(m.comment); err != nil {
		return nil, err
	}

	var health mt5Health
	asked := time.Now()
	if err := m.get(ctx, "/health", nil, &health); err != nil {
		return nil, fmt.Errorf("mt5: the bridge at %s: %w", m.baseURL, err)
	}
	answered := time.Now()
	m.health = mt5Seen{connected: health.Connected, asked: asked, answered: answered}
	switch {
	case !health.Connected && !m.dataOnly:
		return nil, fmt.Errorf("mt5: the terminal of the account %d is not connected to its server %s",
			health.Login, health.Server)
	case !health.Connected:
		log.Warnf("[MT5] the terminal of the account %d is not connected to its server %s", health.Login,
			health.Server)
	case health.ReadOnly && !m.dataOnly:
		return nil, errors.New("mt5: the bridge is read-only (--read-only), it refuses every order")
	case !health.TradeAllowed && !m.dataOnly:
		log.Warnf("[MT5] AlgoTrading is off in the terminal of the account %d: every order will be refused "+
			"until it is turned on", health.Login)
	}
	m.deadlines, m.timeouts = health.Deadline, health.Timeout
	switch {
	case m.dataOnly:
	case !m.deadlines:
		log.Warnf("[MT5] the bridge at %s doesn't honor %s, update tools/mt5bridge: it may send a trade request "+
			"after the bot gave up on it, or answer no positions when the terminal fails to list them, and a trade "+
			"request whose answer is lost can't be confirmed as not executed", m.baseURL, MT5DeadlineHeader)
	case !m.timeouts:
		log.Warnf("[MT5] the bridge at %s doesn't honor %s, update tools/mt5bridge: its clock behind the bot's "+
			"would send a trade request after its deadline, so a trade request whose answer is lost can't be "+
			"confirmed as not executed", m.baseURL, MT5TimeoutHeader)
	}
	if ahead := mt5ClockSkew(health.Time, asked, answered); ahead > mt5ClockAhead && !m.dataOnly {
		log.Warnf("[MT5] the clock of the bridge at %s is %s ahead of the bot's: the trade requests have that "+
			"much less than their %s to reach the terminal (%s), and are all refused unsent past it: sync the "+
			"clocks", m.baseURL, ahead.Round(time.Millisecond), m.deadline, MT5DeadlineHeader)
	}

	var account mt5Account
	if err := m.get(ctx, "/account", nil, &account); err != nil {
		return nil, fmt.Errorf("mt5: reading the account: %w", err)
	}
	switch {
	case account.TradeMode == mt5AccountReal && !m.allowReal:
		return nil, fmt.Errorf("mt5: the account %d is real, allow it with WithMT5Real", account.Login)
	case account.MarginMode != mt5MarginHedging && !m.dataOnly:
		return nil, fmt.Errorf("mt5: the account %d has margin mode %d, only hedging accounts (%d) are supported",
			account.Login, account.MarginMode, mt5MarginHedging)
	case account.Currency == "":
		return nil, fmt.Errorf("mt5: the account %d has no currency", account.Login)
	}
	m.login, m.currency, m.demo = account.Login, account.Currency, account.TradeMode != mt5AccountReal
	m.accountLv = account.Leverage

	for _, name := range m.symbols {
		symbol, err := m.symbol(ctx, name)
		if err != nil {
			return nil, err
		}
		if symbol == nil {
			return nil, fmt.Errorf("%w: mt5: no symbol %s in the terminal", ErrInvalidAsset, name)
		}
		switch {
		case symbol.TradeMode == mt5SymbolDisabled && !m.dataOnly:
			return nil, fmt.Errorf("mt5: trading %s is disabled on the account %d", name, m.login)
		case symbol.TradeMode != mt5SymbolFull && !m.dataOnly:
			log.Warnf("[MT5] %s has the trade mode %d, not full (%d): some orders will be refused", name,
				symbol.TradeMode, mt5SymbolFull)
		case symbol.ContractSize <= 0 || symbol.VolumeStep <= 0:
			return nil, fmt.Errorf("mt5: %s has no contract size or volume step", name)
		}
		if !m.dataOnly && symbol.OrderMode != 0 && symbol.OrderMode&mt5OrderCloseBy == 0 {
			log.Warnf("[MT5] %s doesn't allow close-by: a pending order filled against the bot's net can't be "+
				"netted and stays open on both sides", name)
		}
		m.instruments[name] = *symbol
		base := RegisterMT5Pair(name, symbol.CurrencyBase, symbol.CurrencyProfit)
		RegisterMT5Contract(name, symbol.ContractSize)
		m.assetsInfo[name] = symbol.assetInfo(base)
		if suffix := mt5Suffix(*symbol); !mt5Contains(m.suffixes, suffix) {
			m.suffixes = append(m.suffixes, suffix)
		}
	}

	mode := "demo"
	if !m.demo {
		mode = "real"
	}
	if m.dataOnly {
		log.Infof("[SETUP] Using MT5 exchange (bridge %s): %s account %d on %s, %s, market data only", m.baseURL,
			mode, m.login, account.Server, m.currency)
		return m, nil
	}

	for _, name := range m.symbols {
		if _, err := m.conversion(ctx, m.currency, m.instruments[name].CurrencyProfit); err != nil {
			return nil, err
		}
	}
	if err := m.startTrading(ctx); err != nil {
		return nil, err
	}
	log.Infof("[SETUP] Using MT5 exchange (bridge %s): %s account %d on %s, %s, magic %d, comment %q", m.baseURL,
		mode, m.login, account.Server, m.currency, m.magic, m.comment)
	return m, nil
}

// validMT5Comment checks the comment of the bot: 1 to 31 printable ASCII
// characters, not starting with "[", which MT5 uses for the comments of the
// exits by the levels ("[sl x]", "[tp x]", "[so ...]").
func validMT5Comment(comment string) error {
	switch {
	case comment == "":
		return errors.New("mt5: the comment is empty, see WithMT5Comment")
	case len(comment) > mt5MaxComment:
		return fmt.Errorf("mt5: the comment %q is longer than %d characters, the longest MT5 keeps", comment,
			mt5MaxComment)
	case strings.HasPrefix(comment, "["):
		return fmt.Errorf("mt5: the comment %q starts with [, as the comments of the exits by the levels", comment)
	}
	for _, r := range comment {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("mt5: the comment %q has a character other than printable ASCII", comment)
		}
	}
	return nil
}

// RegisterMT5Pair registers a symbol quoted in quote with its base asset as
// NewMT5 does, eg: to backtest candles downloaded from MT5 (XAUUSDm, XAU, USD).
// The asset is base, or when base is empty or the quote itself, the symbol
// name before the quote (XAU of XAUUSDm), or else the symbol. It returns the
// asset registered. See RegisterMT5Contract for the sizes.
func RegisterMT5Pair(symbol, base, quote string) (asset string) {
	asset = base
	if asset == "" || asset == quote {
		asset = symbol
		if i := strings.Index(symbol, quote); quote != "" && i > 0 {
			asset = symbol[:i]
		}
	}
	RegisterPair(symbol, asset, quote)
	return asset
}

var (
	mt5ContractsMtx sync.RWMutex
	// mt5Contracts is the contract size of the symbols, by symbol: the ones of
	// Exness validated live, and the ones registered by NewMT5 and
	// RegisterMT5Contract.
	mt5Contracts = map[string]float64{
		"XAUUSDm": 100,
		"BTCUSDm": 1,
		"EURUSDm": 100000,
		"USDBRLm": 100000,
	}
)

// RegisterMT5Contract records the contract size of a symbol, the units of the
// base asset in a lot, eg: to convert lots in a backtest. NewMT5 registers the
// ones of the symbols traded.
func RegisterMT5Contract(symbol string, contractSize float64) {
	if contractSize <= 0 || math.IsNaN(contractSize) || math.IsInf(contractSize, 0) {
		return
	}
	mt5ContractsMtx.Lock()
	defer mt5ContractsMtx.Unlock()
	mt5Contracts[symbol] = contractSize
}

// MT5ContractSize returns the contract size of a symbol registered by NewMT5
// or RegisterMT5Contract, or known by the package.
func MT5ContractSize(symbol string) (float64, bool) {
	mt5ContractsMtx.RLock()
	defer mt5ContractsMtx.RUnlock()
	size, ok := mt5Contracts[symbol]
	return size, ok
}

// assetInfo reports the sizes of the symbol in units of its base asset.
func (s mt5Symbol) assetInfo(base string) model.AssetInfo {
	contract := s.contract()
	step := s.VolumeStep * contract
	info := model.AssetInfo{
		BaseAsset:          base,
		QuoteAsset:         s.CurrencyProfit,
		MinQuantity:        s.VolumeMin * contract,
		MaxQuantity:        s.VolumeMax * contract,
		StepSize:           step,
		MaxPrice:           math.MaxFloat64,
		TickSize:           s.tick(),
		QuotePrecision:     s.Digits,
		BaseAssetPrecision: int(model.NumDecPlaces(mt5Clean(step))),
	}
	if info.MaxQuantity <= 0 {
		info.MaxQuantity = math.MaxFloat64
	}
	if info.TickSize <= 0 {
		info.TickSize = math.Pow10(-s.Digits)
	}
	return info
}

func (s mt5Symbol) contract() float64 {
	if s.ContractSize > 0 {
		return s.ContractSize
	}
	return 1
}

// steps converts lots into a whole number of volume steps, rounded.
func (s mt5Symbol) steps(lots float64) int64 {
	if s.VolumeStep <= 0 {
		return int64(math.Round(lots * 100))
	}
	return int64(math.Round(lots / s.VolumeStep))
}

// lotsOf converts volume steps into lots, without the float noise.
func (s mt5Symbol) lotsOf(steps int64) float64 {
	step := s.VolumeStep
	if step <= 0 {
		step = 0.01
	}
	lots, _ := strconv.ParseFloat(strconv.FormatFloat(float64(steps)*step, 'f',
		int(model.NumDecPlaces(mt5Clean(step))), 64), 64)
	return lots
}

// lots converts units into lots, floored to the volume step.
func (s mt5Symbol) lots(units float64) float64 {
	lots := units / s.contract()
	if s.VolumeStep > 0 {
		return s.lotsOf(int64(math.Floor(lots/s.VolumeStep + 1e-9)))
	}
	return lots
}

// units converts lots into units of the base asset, without the float noise.
func (s mt5Symbol) units(lots float64) float64 {
	units := lots * s.contract()
	precision := int(model.NumDecPlaces(mt5Clean(s.VolumeStep*s.contract()))) + 2
	units, _ = strconv.ParseFloat(strconv.FormatFloat(units, 'f', precision, 64), 64)
	return units
}

// tick returns the step of the prices of the symbol: its tick size, else its
// point.
func (s mt5Symbol) tick() float64 {
	if s.TickSize > 0 {
		return s.TickSize
	}
	return s.Point
}

// price rounds a price to the tick size of the symbol, when it has one coarser
// than its point (eg: 0.25 for an index), and to its digits, as MT5 takes
// them; 0 stays 0.
func (s mt5Symbol) price(price float64) float64 {
	if price == 0 || s.Name == "" {
		return price
	}
	if s.TickSize > 0 && s.TickSize > s.Point*(1+1e-9) {
		price = math.Round(price/s.TickSize) * s.TickSize
	}
	return mt5Round(price, s.Digits)
}

// filling returns the filling of the market orders: fill or kill when the
// symbol allows it, else immediate or cancel, else return.
func (s mt5Symbol) filling() int {
	switch {
	case s.FillingMode&mt5FillingFlagFOK != 0:
		return mt5FillFOK
	case s.FillingMode&mt5FillingFlagIOC != 0:
		return mt5FillIOC
	}
	return mt5FillReturn
}

// mt5Clean drops the float noise of a product, eg: 0.01 x 100 = 1.0000000000000002.
func mt5Clean(value float64) float64 {
	cleaned, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'g', 12, 64), 64)
	return cleaned
}

// mt5Round rounds a price to digits decimals, removing the float noise of the
// terminal, eg: 4293.5070000000005.
func mt5Round(value float64, digits int) float64 {
	if digits < 0 {
		return value
	}
	scale := math.Pow10(digits)
	return math.Round(value*scale) / scale
}

// mt5Suffix returns the suffix of the name of a symbol after its currencies,
// eg: "m" for XAUUSDm, "" when unknown.
func mt5Suffix(s mt5Symbol) string {
	prefix := s.CurrencyBase + s.CurrencyProfit
	if prefix != "" && strings.HasPrefix(s.Name, prefix) {
		return s.Name[len(prefix):]
	}
	return ""
}

func mt5Contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// mt5Unique returns the values without blanks nor duplicates, in their order.
func mt5Unique(values []string) []string {
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !mt5Contains(unique, value) {
			unique = append(unique, value)
		}
	}
	return unique
}

// symbol returns the symbol of that name, from the catalog or GET /symbol, nil
// when the terminal has none (HTTP 404).
func (m *MT5) symbol(ctx context.Context, name string) (*mt5Symbol, error) {
	m.catalogMtx.Lock()
	symbol, ok := m.catalog[name]
	m.catalogMtx.Unlock()
	if ok {
		return symbol, nil
	}

	var info mt5Symbol
	err := m.get(ctx, "/symbol", url.Values{"symbol": {name}}, &info)
	var bridgeErr *MT5Error
	switch {
	case errors.As(err, &bridgeErr) && bridgeErr.Status == http.StatusNotFound:
		symbol = nil
	case err != nil:
		return nil, fmt.Errorf("mt5: reading the symbol %s: %w", name, err)
	default:
		if info.Name == "" {
			info.Name = name
		}
		symbol = &info
	}
	m.catalogMtx.Lock()
	m.catalog[name] = symbol
	m.catalogMtx.Unlock()
	return symbol, nil
}

// instrument returns a symbol traded, or else of the catalog, or an empty one.
func (m *MT5) instrument(name string) mt5Symbol {
	if symbol, ok := m.instruments[name]; ok {
		return symbol
	}
	m.catalogMtx.Lock()
	defer m.catalogMtx.Unlock()
	if symbol := m.catalog[name]; symbol != nil {
		return *symbol
	}
	return mt5Symbol{}
}

// mt5Conversion is the symbol converting one currency into another: its price
// is the second currency per unit of the first, or the inverse.
type mt5Conversion struct {
	symbol  string // empty for the same currency
	inverse bool
}

// conversion finds the symbol converting from into to: from+to or to+from
// (inverted), with the suffixes of the symbols traded, eg: USDBRLm inverted for
// BRL into USD.
func (m *MT5) conversion(ctx context.Context, from, to string) (mt5Conversion, error) {
	if from == to || from == "" || to == "" {
		return mt5Conversion{}, nil
	}
	m.conversionsMtx.Lock()
	conversion, ok := m.conversions[from+to]
	m.conversionsMtx.Unlock()
	if ok {
		return conversion, nil
	}

	suffixes := append(append([]string{}, m.suffixes...), "")
	var tried []string
	for _, suffix := range mt5Unique(suffixes) {
		for _, candidate := range []mt5Conversion{{symbol: from + to + suffix}, {symbol: to + from + suffix,
			inverse: true}} {
			tried = append(tried, candidate.symbol)
			symbol, err := m.symbol(ctx, candidate.symbol)
			if err != nil {
				return mt5Conversion{}, err
			}
			if symbol == nil {
				continue
			}
			m.conversionsMtx.Lock()
			m.conversions[from+to] = candidate
			m.conversions[to+from] = mt5Conversion{symbol: candidate.symbol, inverse: !candidate.inverse}
			m.conversionsMtx.Unlock()
			return candidate, nil
		}
	}
	return mt5Conversion{}, fmt.Errorf("mt5: no symbol converts %s into %s, tried %s", from, to,
		strings.Join(tried, ", "))
}

// rate returns the value in to of one unit of from, at the mid price of the
// symbol converting them.
func (m *MT5) rate(ctx context.Context, from, to string) (float64, error) {
	conversion, err := m.conversion(ctx, from, to)
	if err != nil || conversion.symbol == "" {
		return 1, err
	}
	tick, err := m.quote(ctx, conversion.symbol)
	if err != nil {
		return 0, err
	}
	price := mid(tick.Bid, tick.Ask)
	if price <= 0 {
		return 0, fmt.Errorf("mt5: no price for %s, converting %s into %s", conversion.symbol, from, to)
	}
	if conversion.inverse {
		return 1 / price, nil
	}
	return price, nil
}

// mt5Tick is the answer of GET /tick (symbol_info_tick).
type mt5Tick struct {
	Time    int64   `json:"time"`
	Bid     float64 `json:"bid"`
	Ask     float64 `json:"ask"`
	Last    float64 `json:"last"`
	TimeMsc int64   `json:"time_msc"`
}

// mt5Quote is a tick read, and when.
type mt5Quote struct {
	tick mt5Tick
	at   time.Time
}

// mt5Skew is how far the clock of the bot looked ahead of the trade server's
// as a tick was read, at, see clockAhead.
type mt5Skew struct {
	ahead time.Duration
	at    time.Time
}

// tick reads the last tick of a symbol (GET /tick), kept for quote.
func (m *MT5) tick(ctx context.Context, symbol string) (mt5Tick, error) {
	var tick mt5Tick
	if err := m.get(ctx, "/tick", url.Values{"symbol": {symbol}}, &tick); err != nil {
		return mt5Tick{}, fmt.Errorf("mt5: reading the tick of %s: %w", symbol, err)
	}
	now := time.Now()
	m.quotesMtx.Lock()
	previous, read := m.quotes[symbol]
	if read && tick.at().After(previous.tick.at()) && now.Sub(previous.at) <= mt5SkewGap*m.pollInterval {
		// a tick new since the previous read, a moment ago: received at most
		// that moment and the latency of the terminal after the server timed
		// it, it tells how far ahead the clock of the bot is, a bit more
		m.skews = append(m.skews, mt5Skew{ahead: now.Sub(tick.at()), at: now})
		if len(m.skews) > mt5SkewSamples {
			m.skews = m.skews[len(m.skews)-mt5SkewSamples:]
		}
	}
	m.quotes[symbol] = mt5Quote{tick: tick, at: now}
	m.quotesMtx.Unlock()
	return tick, nil
}

// clockAhead returns how far the clock of the bot is ahead of the trade
// server's, past mt5CloseDelay, 0 otherwise or unknown: the end of a candle
// on the clock of the bot comes that much before the server's, see
// confirmsClose. Told by the ticks read new (see tick): the least of the last
// ones, each of them that much ahead or a bit more, never less.
func (m *MT5) clockAhead() time.Duration {
	m.quotesMtx.Lock()
	defer m.quotesMtx.Unlock()
	var ahead time.Duration
	known := false
	for _, skew := range m.skews {
		if time.Since(skew.at) > mt5SkewKeep {
			continue
		}
		if !known || skew.ahead < ahead {
			ahead, known = skew.ahead, true
		}
	}
	if !known || ahead <= mt5CloseDelay || ahead > mt5SkewMax {
		return 0
	}
	return ahead
}

// quote returns the last tick of a symbol, read again when older than the poll
// interval.
func (m *MT5) quote(ctx context.Context, symbol string) (mt5Tick, error) {
	m.quotesMtx.Lock()
	quote, ok := m.quotes[symbol]
	m.quotesMtx.Unlock()
	if ok && time.Since(quote.at) < m.pollInterval {
		return quote.tick, nil
	}
	return m.tick(ctx, symbol)
}

// get sends a GET to the bridge and decodes its answer into out, sending it
// again after a transport error or an HTTP 5xx, up to mt5Retries times.
func (m *MT5) get(ctx context.Context, path string, query url.Values, out any) error {
	for attempt := 0; ; attempt++ {
		err := m.send(ctx, http.MethodGet, path, query, nil, time.Time{}, out)
		var bridgeErr *MT5Error
		retry := err != nil && ctx.Err() == nil &&
			(!errors.As(err, &bridgeErr) || bridgeErr.Status >= http.StatusInternalServerError)
		if !retry || attempt >= mt5Retries {
			return err
		}
		if err := sleep(ctx, time.Duration(attempt+1)*m.retryDelay); err != nil {
			return err
		}
	}
}

// post sends a POST to the bridge, once: a trade request may have been
// executed even when its answer is lost. The bridge never sends it to the
// terminal after deadline (MT5DeadlineHeader, and MT5TimeoutHeader for a
// bridge of another clock).
func (m *MT5) post(ctx context.Context, path string, body any, deadline time.Time, out any) error {
	return m.send(ctx, http.MethodPost, path, nil, body, deadline, out)
}

// tradeDeadline returns the deadline of a trade request sent now: the
// deadline of the adapter, within two thirds of the timeout of the HTTP client,
// so that the bridge gives up on it before the adapter does.
func (m *MT5) tradeDeadline() time.Time {
	wait := m.deadline
	if timeout := m.client.Timeout; timeout > 0 && wait > timeout*2/3 {
		wait = timeout * 2 / 3
	}
	return time.Now().Add(wait)
}

// mt5ClockSkew returns how far the clock of the bridge, at, in Unix seconds,
// read between asked and answered, is ahead of the one of the agent, at least:
// negative when behind, 0 when unknown.
func mt5ClockSkew(at float64, asked, answered time.Time) time.Duration {
	if at <= 0 {
		return 0
	}
	bridge := time.Unix(0, int64(at*float64(time.Second)))
	switch {
	case bridge.After(answered):
		return bridge.Sub(answered)
	case bridge.Before(asked):
		return bridge.Sub(asked)
	}
	return 0
}

// confirmsAbsence reports whether a trade request absent from the account
// margin after its deadline was not executed: the bridge honors both
// deadlines, MT5DeadlineHeader and MT5TimeoutHeader, which it takes the
// earliest of, so that it never sends one after it, whatever its clock.
func (m *MT5) confirmsAbsence() bool {
	return m.deadlines && m.timeouts
}

// mt5Unsent reports whether a trade request that failed surely never reached
// the terminal: refused by the bridge (HTTP 4xx, or "sent": false, eg: its
// deadline passed while the terminal was busy), or never delivered (the
// connection to the bridge refused). Any other failure, a transport error, a
// timeout or an error of the bridge after order_send, leaves its outcome
// unknown.
func mt5Unsent(err error) bool {
	var bridgeErr *MT5Error
	if errors.As(err, &bridgeErr) {
		return bridgeErr.NotSent || (bridgeErr.Status >= 400 && bridgeErr.Status < 500)
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// send sends one request, turning an error status into an *MT5Error; a zero
// deadline sends none.
func (m *MT5) send(ctx context.Context, method, path string, query url.Values, body any, deadline time.Time,
	out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	target := m.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if m.token != "" {
		request.Header.Set(MT5TokenHeader, m.token)
	}
	if !deadline.IsZero() {
		request.Header.Set(MT5DeadlineHeader, strconv.FormatInt(deadline.UnixMilli(), 10))
		request.Header.Set(MT5TimeoutHeader, strconv.FormatInt(max(time.Until(deadline).Milliseconds(), 0), 10))
	}

	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("mt5: %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("mt5: %s %s: %w", method, path, err)
	}

	if response.StatusCode < 200 || response.StatusCode > 299 {
		bridgeErr := &MT5Error{Method: method, Path: path, Status: response.StatusCode}
		var answer struct {
			Error   string `json:"error"`
			Retcode int    `json:"retcode"`
			Sent    *bool  `json:"sent"`
		}
		if json.Unmarshal(data, &answer) == nil {
			bridgeErr.Message, bridgeErr.Retcode = answer.Error, answer.Retcode
			bridgeErr.NotSent = answer.Sent != nil && !*answer.Sent
		}
		return bridgeErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("mt5: %s %s: invalid answer: %w", method, path, err)
	}
	return nil
}

// ContractSize returns the units of the base asset in a lot of pair, eg: 100
// ounces for XAUUSDm, or 0 when unknown.
func (m *MT5) ContractSize(pair string) float64 {
	if symbol, ok := m.instruments[pair]; ok && symbol.ContractSize > 0 {
		return symbol.ContractSize
	}
	size, _ := MT5ContractSize(pair)
	return size
}

// Lots converts units of the base asset of pair into lots, eg: 1 ounce of
// XAUUSDm is 0.01 lot. It is 0 for a pair of unknown contract size.
func (m *MT5) Lots(pair string, units float64) float64 {
	size := m.ContractSize(pair)
	if size <= 0 {
		return 0
	}
	return mt5Clean(units / size)
}

// Units converts lots of pair into units of its base asset, eg: 0.01 lot of
// XAUUSDm is 1 ounce. It is 0 for a pair of unknown contract size.
func (m *MT5) Units(pair string, lots float64) float64 {
	return mt5Clean(lots * m.ContractSize(pair))
}

// Leverage returns the leverage of the account, the same on every symbol of
// pair ("unlimited" counts as 1:2000): the value of the positions it can open
// is the margin they commit times the leverage. See WithMT5Leverage.
func (m *MT5) Leverage(string) float64 {
	leverage := m.accountLv
	if leverage >= mt5UnlimitedLeverage {
		leverage = mt5MaxLeverage
	}
	return math.Max(leverage, 1)
}

// AccountCurrency returns the currency of the account, eg: BRL.
func (m *MT5) AccountCurrency() string {
	return m.currency
}

// Login returns the number of the account traded.
func (m *MT5) Login() int64 {
	return m.login
}

// Magic returns the magic number of the bot, see WithMT5Magic.
func (m *MT5) Magic() int64 {
	return m.magic
}

// Comment returns the comment of the bot's orders, see WithMT5Comment.
func (m *MT5) Comment() string {
	return m.comment
}

// DataOnly reports whether the adapter reads the market data only, see
// WithMT5DataOnly.
func (m *MT5) DataOnly() bool {
	return m.dataOnly
}

func (m *MT5) AssetsInfo(pair string) model.AssetInfo {
	return m.assetsInfo[pair]
}

// dealSize floors units to the volume step of the symbol and checks them
// against its volume limits, returning the lots to send and the units they are.
func (m *MT5) dealSize(pair string, units float64) (lots, size float64, err error) {
	symbol, ok := m.instruments[pair]
	if !ok {
		return 0, 0, &OrderError{Err: fmt.Errorf("%w: mt5: %s is not traded", ErrInvalidAsset, pair), Pair: pair,
			Quantity: units}
	}
	info := m.assetsInfo[pair]
	lots = symbol.lots(units)
	size = symbol.units(lots)
	if lots <= 0 || size < info.MinQuantity-1e-9*info.MinQuantity || size > info.MaxQuantity*(1+1e-9) {
		return 0, 0, &OrderError{
			Err:      fmt.Errorf("%w: min: %v max: %v", ErrInvalidQuantity, info.MinQuantity, info.MaxQuantity),
			Pair:     pair,
			Quantity: units,
		}
	}
	return lots, size, nil
}

// mt5HashID derives an ExchangeID from a key: the same key always gets the same
// id, so the orders stored by a bot are found after it restarts. mt5HashBit is
// set, so it never collides with a ticket.
func mt5HashID(key string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(key))
	return int64(hash.Sum64()&uint64(mt5HashBit-1)) | mt5HashBit
}

// mt5IsTicket reports whether an ExchangeID is an MT5 ticket rather than a
// hashed id.
func mt5IsTicket(id int64) bool {
	return id > 0 && id < mt5HashBit
}

// mt5LegID is the ExchangeID of a leg of the protection number generation (0
// for the first one) of the position opened by the order ticket: as the
// position ticket is the one of the order opening it, the same id serves while
// the order is pending and once the position is open. A position protected
// again after its protection was removed gets legs of new ids, so the legs
// canceled, which the order controller may still follow, never come back.
func mt5LegID(leg string, ticket int64, generation int) int64 {
	key := "mt5:" + leg + ":" + strconv.FormatInt(ticket, 10)
	if generation > 0 {
		key += ":" + strconv.Itoa(generation)
	}
	return mt5HashID(key)
}

// mt5GroupID is the GroupID of the two legs of a protection, see mt5LegID.
func mt5GroupID(ticket int64, generation int) int64 {
	key := "mt5:oco:" + strconv.FormatInt(ticket, 10)
	if generation > 0 {
		key += ":" + strconv.Itoa(generation)
	}
	return mt5HashID(key)
}

// mt5Side is the side of an MT5 order, deal or position type: buy for 0, 2 and
// 4.
func mt5Side(kind int) model.SideType {
	if kind%2 == 0 {
		return model.SideTypeBuy
	}
	return model.SideTypeSell
}

// mt5MarketType is the MT5 type of a market order on side.
func mt5MarketType(side model.SideType) int {
	if side == model.SideTypeSell {
		return mt5TypeSell
	}
	return mt5TypeBuy
}

// mt5PendingType is the MT5 type of a pending order on side: a limit, or a
// stop when stop is set.
func mt5PendingType(side model.SideType, stop bool) int {
	kind := mt5TypeBuyLimit
	if stop {
		kind = mt5TypeBuyStop
	}
	if side == model.SideTypeSell {
		kind++
	}
	return kind
}

// mt5PendingKind reports whether an MT5 order type is a pending one: 2 to 5.
func mt5PendingKind(kind int) bool {
	return kind >= mt5TypeBuyLimit && kind <= mt5TypeSellStop
}

// mt5OrderType is the xtrader type of an MT5 order type: a limit, a stop loss
// for the stops, or else a market order.
func mt5OrderType(kind int) model.OrderType {
	switch kind {
	case mt5TypeBuyStop, mt5TypeSellStop:
		return model.OrderTypeStopLoss
	case mt5TypeBuyLimit, mt5TypeSellLimit:
		return model.OrderTypeLimit
	}
	return model.OrderTypeMarket
}

// mt5Status maps the state of an MT5 order to the status of a xtrader one.
func mt5Status(state int) model.OrderStatusType {
	switch state {
	case mt5StatePartial:
		return model.OrderStatusTypePartiallyFilled
	case mt5StateFilled:
		return model.OrderStatusTypeFilled
	case mt5StateCanceled:
		return model.OrderStatusTypeCanceled
	case mt5StateRejected:
		return model.OrderStatusTypeRejected
	case mt5StateExpired:
		return model.OrderStatusTypeExpired
	}
	return model.OrderStatusTypeNew
}

func mt5Opposite(side model.SideType) model.SideType {
	if side == model.SideTypeBuy {
		return model.SideTypeSell
	}
	return model.SideTypeBuy
}

// mt5Time converts milliseconds since the epoch into a UTC time, zero for 0.
func mt5Time(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
