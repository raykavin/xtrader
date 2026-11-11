package ui

// This file holds every type exposed by the HTTP API. It is the single source
// of truth for the TypeScript types consumed by the web dashboard:
// `make generate` runs tygo on this file and writes web/src/api/types.ts.
//
// All timestamps are Unix seconds (UTC), which is the native time format of
// lightweight-charts.

//go:generate go run github.com/gzuidhof/tygo@v0.2.21 generate

// PageInfo is the page of a list the dashboard asked for, with ?page= (from
// 1), ?per_page= (DefPerPage by default, MaxPerPage at most) and ?q= (its
// search, every word in a field of a row, the case and the accents ignored):
// the rows of the list matching the search (Total), the page answered (the
// last one for a page past it), its size, the pages and whether others come
// before and after it, as the result of github.com/raykavin/gobox/pagination.
// Without ?page= the whole list is one page, as before the pages.
type PageInfo struct {
	Total      int  `json:"total"`
	Page       int  `json:"page"`
	PerPage    int  `json:"per_page"`
	TotalPages int  `json:"total_pages"`
	HasNext    bool `json:"has_next"`
	HasPrev    bool `json:"has_prev"`
}

// The size of a page of a list, by default and at most, and the longest
// search, in characters, see PageInfo.
const (
	DefPerPage = 20
	MaxPerPage = 100
	MaxSearch  = 100
)

// Candle is an OHLCV bar.
type Candle struct {
	Time   int64   `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// Point is a single time/value sample of a series.
type Point struct {
	Time  int64   `json:"time"`
	Value float64 `json:"value"`
}

// Metric is one plotted series of an indicator (e.g. the signal line of MACD).
// Style is one of: line, scatter, bar, histogram, waterfall.
type Metric struct {
	Name   string  `json:"name"`
	Color  string  `json:"color"`
	Style  string  `json:"style"`
	Points []Point `json:"points"`
}

// Indicator groups the metrics of one indicator. Overlay indicators are drawn
// on top of the price chart; the others get their own pane.
type Indicator struct {
	Name    string   `json:"name"`
	Overlay bool     `json:"overlay"`
	Metrics []Metric `json:"metrics"`
}

// Order is the API projection of model.Order. CandleTime is the open time of
// the candle that contains the order, so the UI can anchor markers to a bar.
type Order struct {
	ID          int64    `json:"id"`
	ExchangeID  int64    `json:"exchange_id"`
	Pair        string   `json:"pair"`
	Side        string   `json:"side"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	Price       float64  `json:"price"`
	Quantity    float64  `json:"quantity"`
	Fee         float64  `json:"fee"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	CandleTime  int64    `json:"candle_time"`
	Stop        *float64 `json:"stop"`
	GroupID     *int64   `json:"group_id"`
	RefPrice    float64  `json:"ref_price"`
	Profit      float64  `json:"profit"`
	ProfitValue float64  `json:"profit_value"`

	// AccountProfit is the profit the account booked for the order, a close,
	// in AccountCurrency, see WithAccountReporter: the one shown, ProfitValue
	// the one of the core, in the quote, shown only in a simulation. Absent
	// when unknown, eg: awaited from the history of the broker.
	AccountProfit   *float64 `json:"account_profit,omitempty"`
	AccountCurrency string   `json:"account_currency,omitempty"`

	// DecisionID is the decision of the language model of the strategy that
	// placed the order, see DecisionSource: its justification. Absent for the
	// other orders and strategies.
	DecisionID string `json:"decision_id,omitempty"`
}

// Drawdown is the maximum drawdown window of the paper wallet equity.
type Drawdown struct {
	Value float64 `json:"value"`
	Start int64   `json:"start"`
	End   int64   `json:"end"`
}

// Snapshot is everything the dashboard needs to render one pair.
type Snapshot struct {
	Pair         string      `json:"pair"`
	Asset        string      `json:"asset"`
	Quote        string      `json:"quote"`
	Candles      []Candle    `json:"candles"`
	Indicators   []Indicator `json:"indicators"`
	Orders       []Order     `json:"orders"`
	EquityValues []Point     `json:"equity_values"`
	AssetValues  []Point     `json:"asset_values"`
	MaxDrawdown  *Drawdown   `json:"max_drawdown"`

	// Levels are the horizontal lines of the strategy, see LevelsSource:
	// null when it draws none.
	Levels []Level `json:"levels" tstype:"Level[] | null,optional"`

	// Position is the open position of the pair as the broker reports it, its
	// profit included, see WithBrokerPositions: absent without it.
	Position *BrokerPosition `json:"position,omitempty"`
	// Protection is how the protection of the open position of the pair
	// stands (breakeven, trailing stop), see Chart.SetProtection: absent
	// without a position or without the protection.
	Protection *PositionProtection `json:"protection,omitempty"`

	// AccountCurrency is the currency of the account, see WithAccountReporter:
	// the money is shown in it, the profits of the orders (their
	// AccountProfit) and of the position, never converted into another.
	AccountCurrency string `json:"account_currency,omitempty"`
	// PointValue is the value in AccountCurrency of a move of 1 of the price
	// of the pair for 1 unit of its asset, as the broker tells it (its tick
	// value), see service.PointValuer: the risk and the return of an order are
	// valued with it. Absent when unknown, eg: without an account.
	PointValue float64 `json:"point_value,omitempty"`
	// Simulated tells a bot trading a paper wallet: its money is simulated, in
	// the quote of the pair, without any account.
	Simulated bool `json:"simulated,omitempty"`

	// LLM tells that a language model makes the decisions of the strategy,
	// see strategy.LLMStrategy: the dashboard offers its justifications and
	// its reanalysis. Absent for the other strategies.
	LLM *LLMInfo `json:"llm,omitempty"`

	// Analysis is the last analysis of the language model of the strategy on
	// the pair, drawn on the chart, see AnalysisSource: absent without one.
	Analysis *Analysis `json:"analysis,omitempty"`

	// Timeframe is the one of the strategy, of its Candles, the one it trades
	// and its language model analyzes on; Timeframes are the ones the chart of
	// the pair may be shown on, see GET /api/{pair}/candles: showing another
	// one changes what is drawn only, never the trading.
	Timeframe  string          `json:"timeframe,omitempty"`
	Timeframes []ViewTimeframe `json:"timeframes,omitempty"`
}

// ViewTimeframe is a timeframe the chart of a pair may be shown on: Execution
// tells the one of the strategy; Available that its candles can be read, eg:
// not one below the timeframe of the strategy in a backtest.
type ViewTimeframe struct {
	Timeframe string `json:"timeframe"`
	Execution bool   `json:"execution,omitempty"`
	Available bool   `json:"available"`
}

// CandlesResponse is the payload of GET /api/{pair}/candles?timeframe=&limit=:
// the candles of Pair on Timeframe, oldest first, the last one in progress,
// limit at most (1000 by default, 5000 at most). ExecutionTimeframe is the one
// of the strategy, which trades on its own candles whatever the timeframe
// shown. Source tells where they come from: strategy, its own candles; broker,
// the complete ones read from the broker, the ones in progress since built by
// the chart (above the timeframe of the strategy, from its candles; below it,
// from the prices it received, without their volume); resampled, built from the
// candles of the strategy, a timeframe above its own.
type CandlesResponse struct {
	Pair               string   `json:"pair"`
	Timeframe          string   `json:"timeframe"`
	ExecutionTimeframe string   `json:"execution_timeframe"`
	Source             string   `json:"source"`
	Candles            []Candle `json:"candles"`
}

// LLMInfo is the language model of the strategy of a bot, see
// strategy.LLMStrategy: Model names it, eg: openai gpt-5-mini, and Decisions
// tells whether its decisions are kept, see DecisionSource.
type LLMInfo struct {
	Model     string `json:"model"`
	Decisions bool   `json:"decisions"`
}

// AccountSummary is the account as the broker reports it, in its currency.
type AccountSummary struct {
	Currency   string  `json:"currency"`
	Balance    float64 `json:"balance"`
	Equity     float64 `json:"equity"`
	Profit     float64 `json:"profit"` // of the open positions
	Margin     float64 `json:"margin"`
	FreeMargin float64 `json:"free_margin"`
}

// BrokerPosition is the open position of a pair as the broker reports it: Open
// false when flat, else its side, its size, its average entry and its profit
// in Currency (the account's), as the terminal shows it.
type BrokerPosition struct {
	Open     bool    `json:"open"`
	Side     string  `json:"side,omitempty"`
	Quantity float64 `json:"quantity,omitempty"`
	Entry    float64 `json:"entry,omitempty"`
	Profit   float64 `json:"profit"`
	Currency string  `json:"currency,omitempty"`
}

// Level is a horizontal line of the chart at Price, eg: a support or a
// resistance the strategy relies on; Kind names it, eg: support.
type Level struct {
	Kind  string  `json:"kind"`
	Price float64 `json:"price"`
}

// LevelsSource is a strategy drawing horizontal lines on the chart, eg: the
// supports and resistances of its last analysis. Called from the handlers of
// the chart, ChartLevels must be safe for concurrent use; it returns an empty
// slice, not nil, when there are none.
type LevelsSource interface {
	ChartLevels(pair string) []Level
}

// The kinds of a Drawing: a zone of the structure, eg: a fair value gap; a
// supply zone, where sellers pushed the price down from, and a demand zone,
// where buyers pushed it up from; a level; a marker.
const (
	DrawingZone   = "zone"
	DrawingSupply = "supply"
	DrawingDemand = "demand"
	DrawingLevel  = "level"
	DrawingMarker = "marker"
)

// The states of a supply or demand zone, see Drawing: fresh, the price never
// back into it since it formed; tested, the price came back into it and it
// held; broken, a close beyond its far edge (above a supply zone, below a
// demand one) invalidated it, at To.
const (
	ZoneFresh  = "fresh"
	ZoneTested = "tested"
	ZoneBroken = "broken"
)

// The biases of a Drawing.
const (
	BiasBullish = "bullish"
	BiasBearish = "bearish"
	BiasNeutral = "neutral"
)

// Drawing is a mark of the analysis of a language model on the chart: Kind
// zone, a range of prices from Low to High, eg: a fair value gap or an order
// block; supply or demand, a zone of supply or of demand from Low to High,
// its State telling whether it holds (fresh, tested) or broke (broken);
// level, a price, eg: a pool of liquidity; marker, a point at Price, eg: a
// break of structure. It starts at From, the open time of a candle, and ends
// at To, absent while it holds: drawn up to the last candle. Label names it on
// the chart, eg: FVG; Bias colors it, bullish, bearish or neutral; Timeframe is
// the one it was read on, eg: 4h for a zone of a higher timeframe. ID is the
// one the model keeps for the same structure from an analysis to the next.
type Drawing struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label"`
	Bias      string   `json:"bias"`
	Timeframe string   `json:"timeframe"`
	From      int64    `json:"from"`
	To        *int64   `json:"to,omitempty"`
	Price     *float64 `json:"price,omitempty"`
	Low       *float64 `json:"low,omitempty"`
	High      *float64 `json:"high,omitempty"`
	State     string   `json:"state,omitempty"`
}

// The order types of an Intent.
const (
	IntentMarket = "market"
	IntentLimit  = "limit"
	IntentStop   = "stop"
)

// Intent is the entry a decision proposed without placing it: Action is the
// one of the decision, eg: BUY_LIMIT, of Side BUY or SELL, its OrderType
// market, limit or stop, at Entry with its StopLoss and TakeProfit, and
// Outcome what came of it (ignored under the min confidence, refused by the
// rules, or applied but failed at the broker), Error why.
type Intent struct {
	Action     string   `json:"action"`
	Side       string   `json:"side"`
	OrderType  string   `json:"order_type"`
	Entry      *float64 `json:"entry,omitempty"`
	StopLoss   *float64 `json:"stop_loss,omitempty"`
	TakeProfit *float64 `json:"take_profit,omitempty"`
	Outcome    string   `json:"outcome"`
	Error      string   `json:"error,omitempty"`
}

// Analysis is what the chart draws of the last analysis of the language model
// of the strategy on a pair, or of the levels of a strategy of rules (Source,
// eg: Fimathe), see AnalysisSource: its Pair, the decision it
// comes from (DecisionID, its Timeframe, the one of the strategy whatever the
// timeframe the chart shows, the open time of the candle of its context,
// when it was decided, its Action and its Outcome), its Drawings, and its
// Intent, the entry it proposed without placing an order, if any. The orders
// it placed are drawn as any order. An analysis of the model that answered
// replaces the one before, whatever its outcome; one that didn't (a request
// failed, or skipped) leaves it.
type Analysis struct {
	Pair string `json:"pair"`
	// Source names what drew it, eg: Fimathe for the levels of a strategy of
	// rules; empty for the language model.
	Source     string    `json:"source,omitempty"`
	DecisionID string    `json:"decision_id"`
	Timeframe  string    `json:"timeframe"`
	Candle     int64     `json:"candle"`
	DecidedAt  int64     `json:"decided_at"`
	Action     string    `json:"action"`
	Outcome    string    `json:"outcome"`
	Drawings   []Drawing `json:"drawings"`
	Intent     *Intent   `json:"intent,omitempty"`
}

// AnalysisSource is a strategy drawing on the chart the last analysis of its
// language model on each pair, or the levels of its rules, see Analysis:
// ChartAnalysis returns it, false
// when there is none yet. Called from the handlers of the chart holding its
// lock, it must be safe for concurrent use, and must not block.
type AnalysisSource interface {
	ChartAnalysis(pair string) (Analysis, bool)
}

// PairsResponse lists the pairs known by the dashboard.
type PairsResponse struct {
	Pairs []string `json:"pairs"`
}

// AgentStatus is an agent of the dashboard, a bot serving its API at Address,
// of the Kind of its settings (llm, trend, momentum, breakout or intraday):
// the dashboard lays out the cards of an LLM agent as it loads. Online tells
// whether the API answers. Period is its accounting period, the cards of the
// dashboard counting the trades closed since its start: absent before the
// first reset of its results, all of them counted.
type AgentStatus struct {
	Name        string                `json:"name"`
	Kind        string                `json:"kind"`
	Address     string                `json:"address"`
	Online      bool                  `json:"online"`
	Period      *AgentPeriod          `json:"period,omitempty"`
	Supervision *AgentSupervisionInfo `json:"supervision,omitempty"` // absent for an agent never supervised
	// Pairs are the pairs it trades, as its settings tell them; none without
	// the settings of the controller.
	Pairs []string `json:"pairs,omitempty"`
}

// AgentsResponse lists the agents of the dashboard, in the order of its
// configuration. The API of each one is under /api/agents/{name}.
type AgentsResponse struct {
	Agents []AgentStatus `json:"agents"`
}

// LoginRequest is the payload of POST /api/auth/login of the dashboard: the
// credentials of a user of the controller.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// SessionResponse tells whether the request carries a session of the
// dashboard, and its user: the answer of GET /api/auth/session, of the login
// and of the logout. Role is the role of the user, Permissions what it may
// see and do, the implied ones included (see Effective).
type SessionResponse struct {
	Authenticated bool     `json:"authenticated"`
	Username      string   `json:"username,omitempty"`
	Role          *RoleRef `json:"role,omitempty"`
	Permissions   []string `json:"permissions,omitempty"`
}

// The permissions of the users of the dashboard, "area:action", granted by
// their role (see RoleInfo) and checked by the controller on every route of
// its API: an area seen (view), changed (manage) or operated. The catalog,
// with the labels the dashboard shows, is PermissionAreas; Effective adds the
// implied ones: manage implies view, trading:operate and supervisor:chat the
// panel (dashboard:view).
const (
	PermDashboardView     = "dashboard:view"      // the panel: the agents, their charts, decisions, positions and the guard of the day
	PermTradingOperate    = "trading:operate"     // the bots started and stopped, the orders, the reanalyses, the closes, the entries blocked, the actions of XTrader AI confirmed
	PermSupervisorChat    = "supervisor:chat"     // the chat of XTrader AI and its analysis on demand
	PermPerformanceView   = "performance:view"    // the page of the performance
	PermBacktestsView     = "backtests:view"      // the backtests run
	PermBacktestsManage   = "backtests:manage"    // the backtests started, canceled and deleted
	PermAgentsView        = "agents:view"         // the settings of the agents and their results
	PermAgentsManage      = "agents:manage"       // the agents created, changed, restarted, renamed and deleted
	PermMT5AccountsView   = "mt5-accounts:view"   // the accounts of MT5
	PermMT5AccountsManage = "mt5-accounts:manage" // the accounts of MT5 changed, switched and tested
	PermLimitsView        = "limits:view"         // the daily limits, the trading hours and the exposure
	PermLimitsManage      = "limits:manage"       // the limits changed, of each agent too
	PermNewsView          = "news:view"           // the settings of the news
	PermNewsManage        = "news:manage"         // the settings of the news changed and tested
	PermPromptsView       = "prompts:view"        // the prompts of the LLM agents
	PermPromptsManage     = "prompts:manage"      // the prompts created, changed and deleted
	PermSupervisorView    = "supervisor:view"     // the settings of XTrader AI, its actions and protections
	PermSupervisorManage  = "supervisor:manage"   // the settings of XTrader AI changed
	PermTelegramView      = "telegram:view"       // the settings of the Telegram bot
	PermTelegramManage    = "telegram:manage"     // the Telegram bot changed, its users authorized
	PermUsersView         = "users:view"          // the users of the dashboard and their roles
	PermUsersManage       = "users:manage"        // the users and the roles created, changed and deleted
)

// PermissionArea is an area of the dashboard in the catalog of the
// permissions (see PermissionAreas): its permission to see it (View) and the
// one to change or operate it (Manage), "" when it has none, with the labels
// the dashboard shows of each.
type PermissionArea struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Hint        string `json:"hint"`
	View        string `json:"view,omitempty"`
	ViewLabel   string `json:"view_label,omitempty"`
	Manage      string `json:"manage,omitempty"`
	ManageLabel string `json:"manage_label,omitempty"`
}

// RoleRef names a role of the users of the dashboard.
type RoleRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RoleInfo is a role of the users of the dashboard: the permissions it grants
// as set (every one for the fixed role, Fixed, which can't be changed nor
// deleted), Users the number of users holding it; its time in Unix seconds.
type RoleInfo struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Fixed       bool     `json:"fixed"`
	Permissions []string `json:"permissions"`
	Users       int64    `json:"users"`
	UpdatedBy   string   `json:"updated_by,omitempty"`
	UpdatedAt   int64    `json:"updated_at"`
}

// RolesResponse is the payload of GET /api/roles: the roles, by name (the page
// asked for, see PageInfo), and the catalog of the permissions, in the order of
// the dashboard.
type RolesResponse struct {
	PageInfo `tstype:",extends"`
	Roles    []RoleInfo       `json:"roles"`
	Areas    []PermissionArea `json:"areas"`
}

// RoleRequest is the payload of POST /api/roles (Name and Permissions) and of
// PATCH /api/roles/{id}, of the fields set.
type RoleRequest struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Permissions *[]string `json:"permissions,omitempty"`
}

// MetricUpdate carries the latest point of one indicator metric.
type MetricUpdate struct {
	Name  string `json:"name"`
	Point Point  `json:"point"`
}

// IndicatorUpdate carries the latest point of each metric of an indicator.
type IndicatorUpdate struct {
	Name    string         `json:"name"`
	Metrics []MetricUpdate `json:"metrics"`
}

// CandleEvent is sent on the SSE stream (event: candle) when a candle closes,
// and as the candle in progress changes, Partial: the same candle again until
// its close, without the indicators.
type CandleEvent struct {
	Pair       string            `json:"pair"`
	Candle     Candle            `json:"candle"`
	Indicators []IndicatorUpdate `json:"indicators"`
	Equity     *Point            `json:"equity"`
	Asset      *Point            `json:"asset"`
	Partial    bool              `json:"partial,omitempty"`
	Levels     []Level           `json:"levels" tstype:"Level[] | null,optional"` // of the strategy, see LevelsSource: null when it draws none
	Position   *BrokerPosition   `json:"position,omitempty"`                      // see WithBrokerPositions: absent without it
	// Protection is the protection of the open position of the pair, see
	// Snapshot.Protection: absent without one
	Protection *PositionProtection `json:"protection,omitempty"`
}

// The units of the distances of the protection of the positions, see
// ProtectionSettings.
const (
	// ProtectionUnitATR is a multiple of the ATR(14) Wilder of the closed
	// candles of the timeframe of the agent, in the price of each pair.
	ProtectionUnitATR = "atr"
)

// The features of the protection of the positions.
const (
	ProtectionBreakeven = "breakeven"
	ProtectionTrailing  = "trailing"
)

// ProtectionSettings are the protection of the open positions of an agent,
// off unless enabled, each distance in Unit (see ProtectionUnitATR): the
// breakeven and the trailing stop, applied by the agent on every price, and
// whether the XTrader AI may arm each one on a position (Supervisor), its
// trigger kept.
type ProtectionSettings struct {
	Unit      string            `json:"unit"`
	Breakeven BreakevenSettings `json:"breakeven"`
	Trailing  TrailingSettings  `json:"trailing"`
}

// BreakevenSettings move the stop loss of a position to its entry, beyond it
// by Margin and the costs known, once the price went Trigger in its favour
// from the entry.
type BreakevenSettings struct {
	Enabled    bool    `json:"enabled"`
	Supervisor bool    `json:"supervisor"`
	Trigger    float64 `json:"trigger"`
	Margin     float64 `json:"margin"`
}

// TrailingSettings move the stop loss of a position Distance behind the
// price, once the price went Trigger in its favour from the entry, every
// time it would tighten it by Step at least.
type TrailingSettings struct {
	Enabled    bool    `json:"enabled"`
	Supervisor bool    `json:"supervisor"`
	Trigger    float64 `json:"trigger"`
	Distance   float64 `json:"distance"`
	Step       float64 `json:"step"`
}

// Active reports whether the protection runs, or may be armed by the
// XTrader AI: the stop guard of the agent on (see order.Controller).
func (s ProtectionSettings) Active() bool {
	return s.Breakeven.Enabled || s.Breakeven.Supervisor || s.Trailing.Enabled || s.Trailing.Supervisor
}

// ProtectionArm is a protection the XTrader AI armed on the open position
// PositionRef of Pair: the agent applies Feature (breakeven or trailing) once
// its trigger is reached, for the reason Reason; ID its record.
type ProtectionArm struct {
	ID          int64  `json:"id"`
	Pair        string `json:"pair"`
	PositionRef string `json:"position_ref"`
	Feature     string `json:"feature"`
	Reason      string `json:"reason"`
	ArmedAt     int64  `json:"armed_at"`
}

// The states of a feature of the protection of a position, see
// ProtectionFeature.
const (
	ProtectionOff         = "off"         // disabled, not armed
	ProtectionWaiting     = "waiting"     // its trigger not reached
	ProtectionActive      = "active"      // reached: the stop loss at its level, or moved to it
	ProtectionUnavailable = "unavailable" // can't run, eg: no ATR yet, see Detail
)

// The statuses of a move of the protection, see ProtectionMove.
const (
	MoveRequested = "requested" // sent to the broker, not answered yet
	MoveConfirmed = "confirmed" // the broker took it
	MoveRejected  = "rejected"  // the broker refused it: the stop loss kept
	MoveUnknown   = "unknown"   // the answer lost: the stop loss read again
)

// PositionProtection is how the protection of the open position of a pair
// stands, as the agent follows it: its side, entry (the average fill), the
// price it would exit at now (the bid of a long, the ask of a short), its
// stop loss at the broker (0 for none), the ATR in price, the spread, the
// best move in its favour since its entry in ATR, the costs known as a
// distance of the price, and its two features.
type PositionProtection struct {
	Pair        string            `json:"pair"`
	PositionRef string            `json:"position_ref"`
	Side        string            `json:"side"`
	Entry       float64           `json:"entry"`
	Price       float64           `json:"price"`
	StopLoss    float64           `json:"stop_loss"`
	ATR         float64           `json:"atr"`
	Spread      float64           `json:"spread,omitempty"`
	Favorable   float64           `json:"favorable"`
	Costs       float64           `json:"costs"`
	CostsKnown  bool              `json:"costs_known"`
	Breakeven   ProtectionFeature `json:"breakeven"`
	Trailing    ProtectionFeature `json:"trailing"`
}

// ProtectionFeature is a feature of the protection of a position: its State
// (see ProtectionOff), armed by the XTrader AI (ArmID, ArmReason), what is
// left to its trigger in ATR, the stop loss it asks for (Level), why it
// can't run (Detail) and its last move.
type ProtectionFeature struct {
	State     string          `json:"state"`
	Enabled   bool            `json:"enabled,omitempty"`
	Armed     bool            `json:"armed,omitempty"`
	ArmID     int64           `json:"arm_id,omitempty"`
	ArmReason string          `json:"arm_reason,omitempty"`
	Remaining float64         `json:"remaining,omitempty"`
	Level     float64         `json:"level,omitempty"`
	Detail    string          `json:"detail,omitempty"`
	Last      *ProtectionMove `json:"last,omitempty"`
}

// ProtectionMove is a move of the stop loss a protection asked for: its
// status (see MoveRequested), the level it was at, the one asked, the one
// the broker confirmed, why it failed, when (Unix seconds).
type ProtectionMove struct {
	Status    string  `json:"status"`
	Previous  float64 `json:"previous"`
	Requested float64 `json:"requested"`
	Confirmed float64 `json:"confirmed,omitempty"`
	Detail    string  `json:"detail,omitempty"`
	At        int64   `json:"at"`
}

// OrderEvent is sent on the SSE stream (event: order) when an order is
// created or updated.
type OrderEvent struct {
	Pair  string `json:"pair"`
	Order Order  `json:"order"`
}

// NotificationEvent is sent on the SSE stream (event: notification) with a
// message of the bot, such as the result of a closed trade, or with an error of
// the bot, Error set.
type NotificationEvent struct {
	Message string `json:"message"`
	Error   bool   `json:"error"`
}

// ControlsResponse reports, on the channel of the request, the web dashboard or
// Telegram, whether the bot control panel is enabled, Enabled: the manual
// orders, their moves and cancels, which the trade panel or the Telegram
// controls turn on; and whether the bot may be started and stopped there,
// BotControl, with the controller status ("running" or "stopped"): on the web
// dashboard whatever its trade panel, on Telegram with its controls on. It is
// also the payload of the SSE "controls" event, as the web dashboard sees it,
// sent when the bot is started or stopped, and when its reanalysis is
// switched.
type ControlsResponse struct {
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`

	// BotControl tells that the bot may be started and stopped on the
	// channel, its Status set.
	BotControl bool `json:"bot_control,omitempty"`

	// Reanalysis tells that the language model of the strategy may be asked
	// again on the channel, see Reanalyzer: the strategy is one of a language
	// model, its reanalysis allowed (see Chart.SetReanalysis), and the bot
	// started and stopped from the channel (BotControl), the web dashboard
	// whatever its trade panel.
	Reanalysis bool `json:"reanalysis,omitempty"`

	// ReanalysisDisabled tells that the strategy is one of a language model
	// whose reanalysis is forbidden, see Chart.SetReanalysis: switched off in
	// the settings of the agent, "Permitir reanálise", the bot started and
	// stopped from the channel.
	ReanalysisDisabled bool `json:"reanalysis_disabled,omitempty"`
}

// ControlOrderRequest is the payload of POST /api/controls/order. Amount is
// in the currency of the cash, the account's (the quote in a paper wallet);
// with Percent it is a percentage of the available balance (the cash for
// buys, the asset for sells), and with Lot the size of the order, in the units
// the broker trades the market in, mirroring the Telegram /buy and /sell
// commands.
//
// Type is "market", the default, or "limit" at Price. StopLoss and
// TakeProfit, the ones above 0, are placed at the broker with the order.
type ControlOrderRequest struct {
	Pair       string  `json:"pair"`
	Side       string  `json:"side"`
	Amount     float64 `json:"amount"`
	Percent    bool    `json:"percent"`
	Lot        bool    `json:"lot"`
	Type       string  `json:"type"`
	Price      float64 `json:"price"`
	StopLoss   float64 `json:"stop_loss"`
	TakeProfit float64 `json:"take_profit"`
}

// ControlOrderResponse is the answer of POST /api/controls/order: the order
// created, the entry of a protected one. Warning tells of an order executed
// short of what was asked, never answered as an error, which would have it sent
// again: eg: the entry filled, but its stop loss or its take profit failing,
// the position left without them.
type ControlOrderResponse struct {
	Order   `tstype:",extends"`
	Warning string `json:"warning,omitempty"`
}

// ControlMoveRequest is the payload of POST /api/controls/orders/{id}/move: the
// new price of a pending limit order, or the new level of a stop loss or take
// profit.
type ControlMoveRequest struct {
	Price float64 `json:"price"`
}

// AssetBalance is the balance of an asset of the account of the bot, free plus
// locked, negative when short. Value is its worth in Quote, the quote of its
// pair, at the last close; a quote is worth its quantity.
type AssetBalance struct {
	Asset    string  `json:"asset"`
	Quantity float64 `json:"quantity"`
	Value    float64 `json:"value"`
	Quote    string  `json:"quote"`
}

// BalanceResponse is the payload of GET /api/controls/balance: the assets of
// the pairs of the bot, then the cash, as the Telegram /balance command, their
// values in the currency of the cash (Quote): the account's on an account the
// broker reports, the quotes of the pairs in the paper wallet. Total sums
// their values.
type BalanceResponse struct {
	Balances []AssetBalance `json:"balances"`
	Total    float64        `json:"total"`
	// Account is the account as the broker reports it, in its currency, see
	// WithAccountReporter.
	Account *AccountSummary `json:"account,omitempty"`
}

// Decision is a decision of the language model of a strategy, see
// DecisionSource, kept with its justification whether or not it traded: the
// candle of its context and its price, when and why it was asked (Trigger:
// candle, at the close of a candle; start, on the first candle after the
// start; reanalysis, asked from the dashboard or Telegram between the closes;
// RequestedBy, the channel of a reanalysis it answers, and RequestNote, the
// note the trader sent with it, if any), the model, the prompt and its
// latency, the position held then, what the model answered
// with its justification (Reason), its reading of the market (Sentiment, from
// 0, extremely bearish, to 100, extremely bullish, 50 neutral: absent from the
// decisions without one, eg: failed, skipped or taken before it was asked
// for), its levels, its drawings (see Drawing: absent for a strategy that
// draws none), its notes and its attachments, and
// what came of it (Outcome: applied; ignored, its confidence under the
// minimum; refused, breaking the rules of the context; failed, no answer;
// skipped, an analysis that didn't ask the model, eg: no entry to open while
// flat, Reason then the code of why and Error its detail), with the orders it
// placed, moved or canceled.
type Decision struct {
	ID          string               `json:"id"`
	Pair        string               `json:"pair"`
	Timeframe   string               `json:"timeframe"`
	Candle      int64                `json:"candle"`
	DecidedAt   int64                `json:"decided_at"`
	Trigger     string               `json:"trigger"`
	RequestedBy string               `json:"requested_by,omitempty"`
	RequestNote string               `json:"request_note,omitempty"`
	Model       string               `json:"model"`
	Prompt      *DecisionPrompt      `json:"prompt,omitempty"`
	LatencyMS   int64                `json:"latency_ms"`
	Price       float64              `json:"price"`
	Position    string               `json:"position,omitempty"`
	Action      string               `json:"action,omitempty"`
	Confidence  float64              `json:"confidence"`
	Sentiment   *float64             `json:"sentiment,omitempty"`
	Reason      string               `json:"reason,omitempty"`
	Entry       *float64             `json:"entry,omitempty"`
	StopLoss    *float64             `json:"stop_loss,omitempty"`
	TakeProfit  *float64             `json:"take_profit,omitempty"`
	Levels      []Level              `json:"levels"`
	Drawings    []Drawing            `json:"drawings,omitempty"`
	Notes       []DecisionNote       `json:"notes"`
	Attachments []DecisionAttachment `json:"attachments"`
	Outcome     string               `json:"outcome"`
	Error       string               `json:"error,omitempty"`
	OrderIDs    []int64              `json:"order_ids"`
}

// DecisionPrompt is the prompt a decision was taken with, the strategy the
// model traded: its ID and version in the database of the controller, none
// for a prompt out of it, eg: the file of -prompt-path.
type DecisionPrompt struct {
	ID      int64  `json:"id,omitempty"`
	Name    string `json:"name"`
	Version int    `json:"version,omitempty"`
}

// DecisionNote is a note the model returned with a decision beside its
// justification: Kind text, words of the model beside the decision, or
// citation, an annotation of the API, its Title and URL.
type DecisionNote struct {
	Kind  string `json:"kind"`
	Text  string `json:"text,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// DecisionAttachment is a file the model returned with a decision, eg: an
// image: its media type and its size in bytes, its content served at
// /api/decisions/{id}/attachments/{index}.
type DecisionAttachment struct {
	Index     int    `json:"index"`
	MediaType string `json:"media_type"`
	Size      int    `json:"size"`
}

// DecisionsResponse is the payload of GET /api/decisions?pair=&limit=: the
// last decisions of the pair, newest first.
type DecisionsResponse struct {
	Decisions []Decision `json:"decisions"`
}

// DecisionEvent is sent on the SSE stream (event: decision) with a decision of
// the language model of the strategy as it is kept, and again as the orders it
// places are linked to it. Analysis is the analysis of the pair drawn on the
// chart once it is kept, see AnalysisSource: absent without one.
type DecisionEvent struct {
	Pair     string    `json:"pair"`
	Decision Decision  `json:"decision"`
	Analysis *Analysis `json:"analysis,omitempty"`
}

// ReanalysisRequest is the payload of POST /api/controls/reanalyze: the pair
// whose language model is asked again now, rather than at the next close, and
// optionally a note of the trader for the model, eg: their view of the
// position, MaxReanalysisNote characters at most.
type ReanalysisRequest struct {
	Pair string `json:"pair"`
	Note string `json:"note,omitempty"`
}

// MaxReanalysisNote is the length of the note of a reanalysis at most, in
// characters.
const MaxReanalysisNote = 500

// ReanalysisResponse answers a reanalysis, see Reanalyzer: Status queued, the
// model asked on the next tick of the candle in progress; running, the
// analysis in progress answers it. Message tells it.
type ReanalysisResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ErrorResponse is the JSON body of API error replies.
type ErrorResponse struct {
	Error string `json:"error"`
}

// AgentSettings is an agent in the settings of the dashboard: its settings,
// its process, its language model with its prompt, and which of its secrets
// are set, never their values. Its Name and its Kind never change. Its API is
// served at APIAddress, host:port. Launched tells that the controller runs it,
// and Restart that it runs it again when it exits, with the settings of its
// launch and the secrets of now: Restarts counts the times since then, and
// Restarting tells that it exited and waits for that, Running still. Exited
// tells that it exited without Restart: launched again once restarted,
// enabled again, its settings changed after it exited, or the controller
// started again. LaunchError tells why the controller didn't launch it,
// eg: another agent running trades the same positions on MT5. Outbox tells
// what its outbox holds not written yet, see AgentOutbox. Params are the
// flags of its settings that no field holds, by name, the ones of its
// language model included, eg: {"pair": "XAUUSDm", "llm-min-stop-atr": "1"}.
// Model names its language model, eg: openai gpt-5, and LLM holds it, for an
// LLM agent only. Reanalysis tells that the language model of an LLM agent
// may be asked again between its closes, "Permitir reanálise": from the
// dashboard, whatever its trade panel, and from Telegram, its controls on
// there; false for the other kinds.
type AgentSettings struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Mode            string `json:"mode"`
	APIAddress      string `json:"api_address"`
	Enabled         bool   `json:"enabled"`
	Launched        bool   `json:"launched"`
	Restart         bool   `json:"restart"`
	TradePanel      bool   `json:"trade_panel"`
	TelegramControl bool   `json:"telegram_control"`
	Reanalysis      bool   `json:"reanalysis"`
	Running         bool   `json:"running"`
	RestartPending  bool   `json:"restart_pending"`
	Restarts        int    `json:"restarts,omitempty"`
	Restarting      bool   `json:"restarting,omitempty"`
	Exited          bool   `json:"exited,omitempty"`
	LastExit        string `json:"last_exit,omitempty"`
	LaunchError     string `json:"launch_error,omitempty"`
	// BotStopped tells that a user stopped its bot (Parar bot, /stop), by
	// BotStoppedBy at BotStoppedAt (Unix seconds): it starts stopped, whatever
	// restarts it, until a user starts it
	BotStopped   bool                  `json:"bot_stopped,omitempty"`
	BotStoppedBy string                `json:"bot_stopped_by,omitempty"`
	BotStoppedAt int64                 `json:"bot_stopped_at,omitempty"`
	Outbox       *AgentOutbox          `json:"outbox,omitempty"`
	Params       map[string]string     `json:"params"`
	Account      *AgentAccountSettings `json:"account,omitempty"`
	MT5AccountID int64                 `json:"mt5_account_id,omitempty"` // the account of MT5 it trades, see MT5AccountInfo
	LLM          *AgentLLMSettings     `json:"llm,omitempty"`
	Model        string                `json:"model,omitempty"`
	PromptID     *int64                `json:"prompt_id,omitempty"`
	PromptStatus *AgentPromptStatus    `json:"prompt_status,omitempty"`
	Secrets      AgentSecretsPresent   `json:"secrets"`
	Supervision  *AgentSupervisionInfo `json:"supervision,omitempty"` // absent for an agent never supervised
	// Protection is the protection of its open positions (breakeven, trailing
	// stop), all off unless set, applied live
	Protection ProtectionSettings `json:"protection"`
	// DailyLimit is its daily limits, in the currency of its account, off
	// unless set, see DailyLimitSettings
	DailyLimit DailyLimitSettings `json:"daily_limit"`
}

// AgentOutbox is what the outbox of an agent holds, as it told the
// controller with its last batch of records: the records not written to the
// database yet, Since in Unix seconds when the oldest of them was written by
// the agent, absent when unknown, Failing when that batch wasn't written, the
// records the controller refused for good, set apart in the outbox, and the
// ones it failed again and again, held apart there, sent again now and then.
// Absent for an outbox sent whole.
type AgentOutbox struct {
	Pending  int   `json:"pending"`
	Since    int64 `json:"since,omitempty"`
	Failing  bool  `json:"failing,omitempty"`
	SetApart int   `json:"set_apart,omitempty"`
	Held     int   `json:"held,omitempty"`
}

// AgentAccountSettings is the trading account of an agent: the URL of the
// bridge of MT5, eg: http://127.0.0.1:18812, the magic number and the comment
// of its orders there, their defaults when absent (a hash of the comment, and
// the one of its kind, eg: nb:llm), and whether a real account is allowed, a
// demo one only otherwise. The magic number is above 0, and the comment takes
// 1 to 31 printable ASCII characters, not starting with [.
type AgentAccountSettings struct {
	BridgeURL   string  `json:"bridge_url"`
	Magic       *int64  `json:"magic,omitempty"`
	Comment     *string `json:"comment,omitempty"`
	RealAllowed bool    `json:"real_allowed"`
}

// AgentLLMSettings is the language model of an LLM agent: its provider
// (openai or anthropic), its model, eg: gpt-5, and, when set, the base URL of
// its API, its reasoning effort (minimal, low, medium or high) and the tokens
// of an answer at most. The MCP server of the economic calendar is the one of
// the news of all the agents, see NewsSettingsResponse.
type AgentLLMSettings struct {
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	BaseURL         *string `json:"base_url,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	MaxTokens       *int    `json:"max_tokens,omitempty"`
}

// AgentPromptStatus is the last prompt an LLM agent was asked to switch to,
// and how it went: applied, or refused with Error, the previous one kept. At
// is in Unix seconds.
type AgentPromptStatus struct {
	PromptID int64  `json:"prompt_id,omitempty"`
	Name     string `json:"name"`
	Version  int    `json:"version"`
	Applied  bool   `json:"applied"`
	Error    string `json:"error,omitempty"`
	At       int64  `json:"at"`
}

// AgentSecretsPresent tells which secrets of an agent are set. The token of
// the MCP server of the calendar is the one of the news of all the agents, see
// NewsSettingsResponse.
type AgentSecretsPresent struct {
	LLMAPIKey   bool `json:"llm_api_key"`
	BridgeToken bool `json:"bridge_token"`
}

// AgentSettingsResponse is the payload of GET /api/settings/agents: every
// agent of the controller, the disabled ones included.
type AgentSettingsResponse struct {
	Agents []AgentSettings `json:"agents"`
}

// AgentRenameRequest is the payload of POST /api/settings/agents/{name}/rename:
// the new name of the agent, disabled and stopped, see store.RenameAgent.
type AgentRenameRequest struct {
	Name string `json:"name"`
}

// AgentSettingsRequest is the payload of PATCH /api/settings/agents/{name}:
// the changes, of the fields set, the kind never changing and the name only by
// AgentRenameRequest. Reanalysis switches the one of an LLM agent, see
// AgentSettings: on refused for another kind.
// Params, when set, replace them all, the ones of the language model included
// ({} removes them); Account and LLM change the fields they set. DefaultPrompt
// selects the prompt embedded in the agent, PromptID one of the database; a
// secret set to "" is removed. A change of the settings the agent runs with,
// all but its switches and its prompt, waits for its restart, see
// AgentSettings.RestartPending.
type AgentSettingsRequest struct {
	Enabled         *bool                `json:"enabled,omitempty"`
	TradePanel      *bool                `json:"trade_panel,omitempty"`
	TelegramControl *bool                `json:"telegram_control,omitempty"`
	Reanalysis      *bool                `json:"reanalysis,omitempty"`
	Mode            *string              `json:"mode,omitempty"`
	APIAddress      *string              `json:"api_address,omitempty"`
	Launched        *bool                `json:"launched,omitempty"`
	Restart         *bool                `json:"restart,omitempty"`
	Params          map[string]string    `json:"params,omitempty"`
	Account         *AgentAccountRequest `json:"account,omitempty"`
	LLM             *AgentLLMRequest     `json:"llm,omitempty"`
	PromptID        *int64               `json:"prompt_id,omitempty"`
	DefaultPrompt   bool                 `json:"default_prompt,omitempty"`
	LLMAPIKey       *string              `json:"llm_api_key,omitempty"`
	BridgeToken     *string              `json:"bridge_token,omitempty"`
	// Protection replaces the protection of the open positions, applied live
	Protection *ProtectionSettings `json:"protection,omitempty"`
	// MT5AccountID links the agent to an account of MT5 (0 unlinks it): only
	// with the agent stopped and flat on its former one
	MT5AccountID *int64 `json:"mt5_account_id,omitempty"`
}

// AgentParam is a param set by the global parametrizer: the name of a flag
// without its dash (eg: lot) and its value.
type AgentParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// MaxAgentsParams is the most params the global parametrizer sets at once.
const MaxAgentsParams = 20

// AgentsParamRequest is the body of POST /api/settings/agents/params, the
// global parametrizer of the agents: the params of Params (or the one of Key
// and Value, without Params), each set on every agent of the filter holding
// it, on the ones without it too when AddMissing, all at once on each agent
// (one refused, none set on it), each agent's other params and settings kept;
// then, when RestartActive, every agent of the filter enabled restarted, the
// disabled ones left stopped. The filter is every agent, the LLM ones only
// when LLMOnly, the enabled ones only when EnabledOnly (both: the LLM ones
// enabled). The params are checked as the params of an agent are, a key once,
// MaxAgentsParams at most.
type AgentsParamRequest struct {
	Key           string       `json:"key,omitempty"`
	Value         string       `json:"value,omitempty"`
	Params        []AgentParam `json:"params,omitempty"`
	AddMissing    bool         `json:"add_missing,omitempty"`
	RestartActive bool         `json:"restart_active,omitempty"`
	LLMOnly       bool         `json:"llm_only,omitempty"`
	EnabledOnly   bool         `json:"enabled_only,omitempty"`
}

// The outcomes of the global parametrizer on an agent, see AgentsParamResult.
const (
	ParamUpdated   = "updated"   // its value changed
	ParamAdded     = "added"     // added, the agent without it
	ParamUnchanged = "unchanged" // the value it had already
	ParamSkipped   = "skipped"   // left as it was, Detail telling why
	ParamFailed    = "failed"    // refused on this agent, Detail telling why
	ParamExcluded  = "excluded"  // out of the filter: untouched, never restarted
)

// The restarts of the global parametrizer, see AgentsParamResult.
const (
	ParamRestarted     = "restarted" // stopped and launched again
	ParamRestartFailed = "failed"    // RestartDetail telling why
	ParamRestartLeft   = "skipped"   // not restarted, RestartDetail telling why
)

// AgentParamResult is what the global parametrizer did with a param on an
// agent: Status ParamUpdated (Previous the value it had), ParamAdded,
// ParamUnchanged or ParamSkipped (Detail telling why).
type AgentParamResult struct {
	Key      string `json:"key"`
	Status   string `json:"status"`
	Previous string `json:"previous,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// AgentsParamResult is what POST /api/settings/agents/params did on an agent:
// Params what it did with each param, in their order, and Status all of them
// told at once: ParamFailed when they were refused on it (Detail telling why,
// none set, Params empty), ParamExcluded out of the filter, else ParamUpdated
// when a value changed, ParamAdded when a param was added only, ParamUnchanged
// when it had them all already, ParamSkipped (Detail telling why with a
// single param). With a single param, Previous is the value it had. And, the
// restart asked, Restart ParamRestarted, ParamRestartFailed or
// ParamRestartLeft (RestartDetail telling why), "" for an agent disabled,
// left stopped, or out of the filter. Enabled tells whether the agent is
// enabled.
type AgentsParamResult struct {
	Agent         string             `json:"agent"`
	Enabled       bool               `json:"enabled"`
	Status        string             `json:"status"`
	Previous      string             `json:"previous,omitempty"`
	Detail        string             `json:"detail,omitempty"`
	Params        []AgentParamResult `json:"params,omitempty"`
	Restart       string             `json:"restart,omitempty"`
	RestartDetail string             `json:"restart_detail,omitempty"`
}

// AgentsParamResponse answers POST /api/settings/agents/params: the params
// set, trimmed (Key and Value the first one), and what was done on each agent,
// in the order of the dashboard.
type AgentsParamResponse struct {
	Key     string              `json:"key"`
	Value   string              `json:"value"`
	Params  []AgentParam        `json:"params"`
	Results []AgentsParamResult `json:"results"`
}

// OpenPositionsResponse is the payload of GET /api/positions/open: the open
// profit of the agents, the profit and the swap of the positions they hold
// open now on their MT5 accounts, as the terminals report them (the closed
// trades and the pending orders left out), the accounts read from their
// bridges: of every agent trading live, running or not (stopped, exited,
// disabled), each position once by its ticket on its account, whatever the
// agents sharing it. Totals sum them by the currency of their account, none
// converted, in the order of the accounts: one for each account read, 0
// without a position. Balances sum the balances of the accounts read, each
// account once by its login, by their currency in the same order; Accounts
// counts them. PendingOrders counts the pending orders of the agents on the
// accounts read, each once by its ticket, the ones by hand left out.
// Complete tells that every account was read: else Missing tells the agents
// whose account wasn't, left out of Totals and Balances. At is when the
// accounts were read, in Unix seconds.
type OpenPositionsResponse struct {
	Totals        []CurrencyAmount   `json:"totals"`
	Balances      []CurrencyAmount   `json:"balances"`
	Accounts      int                `json:"accounts"`
	Complete      bool               `json:"complete"`
	Positions     []OpenPosition     `json:"positions"`
	PendingOrders int                `json:"pending_orders"`
	Missing       []OpenPositionsGap `json:"missing,omitempty"`
	At            int64              `json:"at"`
}

// OpenPosition is a position open on an MT5 account, of the agent of its
// magic number: its side (BUY or SELL), its volume in lots, its open and
// current prices, and its profit with its swap in Currency, the one of the
// account. Running tells that the agent runs, else stopped or disabled.
type OpenPosition struct {
	Agent      string  `json:"agent"`
	Pair       string  `json:"pair"`
	Side       string  `json:"side"`
	Volume     float64 `json:"volume"`
	Entry      float64 `json:"entry"`
	Price      float64 `json:"price"`
	StopLoss   float64 `json:"stop_loss,omitempty"`   // at the broker, absent for none
	TakeProfit float64 `json:"take_profit,omitempty"` // at the broker, absent for none
	Profit     float64 `json:"profit"`
	Currency   string  `json:"currency"`
	Ticket     int64   `json:"ticket"`
	Running    bool    `json:"running"`
}

// OpenPositionsGap is an account of the agents not read, see
// OpenPositionsResponse: the agents on it, and why: unreachable (the bridge
// didn't answer), refused (the bridge refused their tokens) or failed (the
// bridge answered an error, eg: its terminal disconnected).
type OpenPositionsGap struct {
	Agents []string `json:"agents"`
	Reason string   `json:"reason"`
}

// ClosedProfitResponse is the payload of GET /api/positions/closed: the
// closed profit of the agents from From until before To (Unix seconds), what
// their accounts booked for the positions they opened and closed then, read
// from the deals of their MT5 accounts: the profit, swap, commission and fee
// of each exit of a position, as the net profit of an agent counts it, the
// position of the agent of the magic number of its entry, closed by the bot or
// outside it (by hand, by a stop out); of every agent trading live, running or
// not, each deal once by its ticket on its account. Totals sum them by the
// currency of their account, none converted, as OpenPositionsResponse does,
// and Agents by agent, the ones that closed positions then; Series the same
// exits summed one after another in the order of their times, a series for
// each currency of Totals. Complete and Missing as OpenPositionsResponse; At is
// when the deals were read.
type ClosedProfitResponse struct {
	From     int64                `json:"from"`
	To       int64                `json:"to"`
	Totals   []CurrencyAmount     `json:"totals"`
	Agents   []AgentClosedProfit  `json:"agents"`
	Series   []ClosedProfitSeries `json:"series"`
	Complete bool                 `json:"complete"`
	Missing  []OpenPositionsGap   `json:"missing,omitempty"`
	At       int64                `json:"at"`
}

// BacktestRequest is the body of POST /api/backtests: a batch of backtests
// of Agents (by name) on their pairs (Pairs a subset, empty for all of each)
// from From until before To (Unix seconds), its last ValidationShare (0 for
// none, else up to 0.5) run apart as the validation of the rest; Timeframe
// replacing the one of the agents ("" for theirs); the money (Balance, in the
// quote of the pairs: a simulation) and the costs (Fee, a share of the
// notional of each fill; SpreadPoints, for the candles without their spread;
// SlippagePoints); DailyLoss and DailyGain, the daily limits of every run in
// the quote currency (0 for none: the ones of the agents are in the currency
// of their account, never converted); the ceilings of the runs of a language
// model (0 for the defaults of the controller).
type BacktestRequest struct {
	Name            string         `json:"name"`
	Agents          []string       `json:"agents"`
	Pairs           []string       `json:"pairs,omitempty"`
	From            int64          `json:"from"`
	To              int64          `json:"to"`
	ValidationShare float64        `json:"validation_share"`
	Timeframe       string         `json:"timeframe,omitempty"`
	Balance         float64        `json:"balance"`
	Fee             float64        `json:"fee"`
	SpreadPoints    float64        `json:"spread_points"`
	SlippagePoints  float64        `json:"slippage_points"`
	DailyLoss       float64        `json:"daily_loss,omitempty"`
	DailyGain       float64        `json:"daily_gain,omitempty"`
	Hours           *BacktestHours `json:"hours,omitempty"`
	LLMMaxCalls     int            `json:"llm_max_calls,omitempty"`
	LLMMaxCost      float64        `json:"llm_max_cost,omitempty"`
	// Params are params added to every agent of the batch, by name, eg: the
	// entry filter, regime=trend: a param of the agent of the same name is
	// replaced; for the agents of a strategy only (not the LLM ones), never a
	// param a backtest sets itself (the pairs, the timeframe, the mode...).
	Params map[string]string `json:"params,omitempty"`
}

// BacktestHours are the trading hours of the runs of a batch, as the ones of
// the agents (see TradingHoursSettings) on the clock of the candles: no entry
// outside its Windows (HH:MM of America/Belem); at the end of each, after it
// was open, the positions closed and the pending entries canceled, as it
// checks. Without Windows (the batches of before), Start to End is its only
// one (crossing midnight when Start is after End), with Flatten and
// CancelPending.
type BacktestHours struct {
	Windows       []TradingWindow `json:"windows,omitempty"`
	Start         string          `json:"start,omitempty"`
	End           string          `json:"end,omitempty"`
	Flatten       bool            `json:"flatten"`
	CancelPending bool            `json:"cancel_pending"`
}

// BacktestAssumptions are the assumptions of the runs of a batch, the same
// for all of them: the money and the costs asked (see BacktestRequest), and
// the execution, the same for all the backtests: the fills of the candles of
// ExecutionTimeframe (the finest the period allows), the stops filled at the
// open of a candle opening beyond them (GapFills), the worst assumed of a
// candle reaching both the stop and the target or the entry and the stop
// (Conservative), the positions open at the end valued at the market apart
// from the realized result (EndPolicy).
type BacktestAssumptions struct {
	Balance        float64 `json:"balance"`
	Currency       string  `json:"currency"`
	Fee            float64 `json:"fee"`
	SpreadPoints   float64 `json:"spread_points"`
	SlippagePoints float64 `json:"slippage_points"`
	DailyLoss      float64 `json:"daily_loss,omitempty"`
	DailyGain      float64 `json:"daily_gain,omitempty"`
	// Hours are the trading hours of the runs, nil for none
	Hours              *BacktestHours `json:"hours,omitempty"`
	Timeframe          string         `json:"timeframe,omitempty"`
	ExecutionTimeframe string         `json:"execution_timeframe"`
	GapFills           bool           `json:"gap_fills"`
	Conservative       bool           `json:"conservative"`
	EndPolicy          string         `json:"end_policy"`
	LLMMaxCalls        int            `json:"llm_max_calls"`
	LLMMaxCost         float64        `json:"llm_max_cost"`
	// Params are the params added to every agent of the batch, see
	// BacktestRequest.
	Params map[string]string `json:"params,omitempty"`
}

// BacktestBatchInfo is a batch of backtests and its runs, see
// GET /api/backtests.
type BacktestBatchInfo struct {
	ID              int64               `json:"id"`
	Name            string              `json:"name"`
	From            int64               `json:"from"`
	To              int64               `json:"to"`
	ValidationShare float64             `json:"validation_share"`
	Assumptions     BacktestAssumptions `json:"assumptions"`
	CreatedBy       string              `json:"created_by"`
	CreatedAt       int64               `json:"created_at"`
	Runs            []BacktestRunInfo   `json:"runs"`
}

// BacktestRunInfo is a run of a batch: its agent, its segment (fit, or
// validation), its period, its status (queued, running, succeeded, failed,
// canceled, interrupted, budget_exhausted), its progress (candles processed
// of Total), its error, when it ran, what it traded, whether a language model
// decided (LLM: not deterministic), and its summary once ended.
type BacktestRunInfo struct {
	ID         int64            `json:"id"`
	Agent      string           `json:"agent"`
	Kind       string           `json:"kind"`
	Strategy   string           `json:"strategy,omitempty"`
	Segment    string           `json:"segment"`
	From       int64            `json:"from"`
	To         int64            `json:"to"`
	Status     string           `json:"status"`
	Processed  int64            `json:"processed"`
	Total      int64            `json:"total"`
	Error      string           `json:"error,omitempty"`
	StartedAt  int64            `json:"started_at,omitempty"`
	FinishedAt int64            `json:"finished_at,omitempty"`
	Pairs      []string         `json:"pairs"`
	Timeframe  string           `json:"timeframe,omitempty"`
	LLM        bool             `json:"llm,omitempty"`
	Summary    *BacktestSummary `json:"summary,omitempty"`
}

// BacktestSummary is the summary of a run ended, the metrics of the
// comparison, in Currency: the result of the trades closed (Net, after their
// fees, and its share of the balance, Return), the positions open at the end
// (OpenProfit, apart), the trades, won and lost, the win rate, the profit
// factor and the payoff (nil when undefined: no loss, no win), the average
// win and loss, the fees, the largest fall of the equity (and its share),
// Net over it (nil without a fall), the months with trades and the positive
// ones, the warnings and gaps of the data, the orders refused, the calls and
// the cost of a language model, and the timeframe of the candles it was
// filled on (ExecutionTimeframe: the one of the batch, or a coarser one when
// the broker lacked the finer candles of the period).
type BacktestSummary struct {
	Currency           string   `json:"currency"`
	InitialBalance     float64  `json:"initial_balance"`
	FinalEquity        float64  `json:"final_equity"`
	Net                float64  `json:"net"`
	Return             float64  `json:"return"`
	OpenProfit         float64  `json:"open_profit"`
	OpenPositions      int      `json:"open_positions"`
	Trades             int      `json:"trades"`
	Wins               int      `json:"wins"`
	Losses             int      `json:"losses"`
	WinRate            float64  `json:"win_rate"`
	ProfitFactor       *float64 `json:"profit_factor,omitempty"`
	Payoff             *float64 `json:"payoff,omitempty"`
	AverageWin         float64  `json:"average_win"`
	AverageLoss        float64  `json:"average_loss"`
	Fees               float64  `json:"fees"`
	MaxDrawdown        float64  `json:"max_drawdown"`
	MaxDrawdownPct     float64  `json:"max_drawdown_pct"`
	ReturnOverDrawdown *float64 `json:"return_over_drawdown,omitempty"`
	Months             int      `json:"months"`
	PositiveMonths     int      `json:"positive_months"`
	Warnings           int      `json:"warnings"`
	Gaps               int      `json:"gaps"`
	Refused            int      `json:"refused"`
	LLMCalls           int      `json:"llm_calls,omitempty"`
	LLMCost            *float64 `json:"llm_cost,omitempty"`
	ExecutionTimeframe string   `json:"execution_timeframe,omitempty"`
}

// BacktestCompatibility tells whether an agent can be backtested, why not
// (Reason), and what it trades.
type BacktestCompatibility struct {
	Agent      string   `json:"agent"`
	Kind       string   `json:"kind"`
	Mode       string   `json:"mode"`
	Enabled    bool     `json:"enabled"`
	Strategy   string   `json:"strategy,omitempty"`
	Pairs      []string `json:"pairs"`
	Timeframe  string   `json:"timeframe,omitempty"`
	LLM        bool     `json:"llm,omitempty"`
	Compatible bool     `json:"compatible"`
	Reason     string   `json:"reason,omitempty"`
}

// BacktestsResponse is the payload of GET /api/backtests: the batches, the
// newest first, the last ones or the page asked for (see PageInfo), the runs
// queued or running of every batch (Running), every batch as the choice of
// its results (Choices), the agents with their compatibility, and the limits
// of a batch.
type BacktestsResponse struct {
	PageInfo      `tstype:",extends"`
	Batches       []BacktestBatchInfo     `json:"batches"`
	Running       int                     `json:"running"`
	Choices       []BacktestBatchChoice   `json:"choices"`
	Agents        []BacktestCompatibility `json:"agents"`
	MaxPeriodDays int                     `json:"max_period_days"`
	MaxRuns       int                     `json:"max_runs"`
	LLMMaxCalls   int                     `json:"llm_max_calls"`
	LLMMaxCost    float64                 `json:"llm_max_cost"`
}

// BacktestBatchChoice is a batch as the choice of its results: its ID, its
// name and its period.
type BacktestBatchChoice struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	From int64  `json:"from"`
	To   int64  `json:"to"`
}

// The columns the comparison of a batch sorts by, see
// BacktestComparisonResponse: the agent, by name; the net, the return, the
// drawdown (the shallowest first), the return over the drawdown, the profit
// factor, the win rate, the trades and the positive months of the fit; the
// return over the drawdown of the validation.
const (
	CompareAgent      = "agent"
	CompareNet        = "net"
	CompareReturn     = "return"
	CompareDrawdown   = "drawdown"
	CompareRatio      = "ratio"
	CompareFactor     = "factor"
	CompareWinRate    = "winrate"
	CompareTrades     = "trades"
	CompareMonths     = "months"
	CompareValidation = "validation"
)

// BacktestComparisonRow is an agent of a batch compared: what it trades, its
// run of the fit and of the validation (when asked), and whether the
// validation degraded from the fit (a positive fit with a negative
// validation, or the return over the drawdown of the validation under half of
// the fit's).
type BacktestComparisonRow struct {
	Agent      string           `json:"agent"`
	Kind       string           `json:"kind"`
	Strategy   string           `json:"strategy,omitempty"`
	Pairs      []string         `json:"pairs"`
	LLM        bool             `json:"llm"`
	Fit        *BacktestRunInfo `json:"fit,omitempty"`
	Validation *BacktestRunInfo `json:"validation,omitempty"`
	Degraded   bool             `json:"degraded"`
}

// BacktestComparisonResponse is the payload of GET
// /api/backtests/{id}/comparison: the batch (without its runs), the rows of
// its agents kept by the filters (?min_trades=, ?llm=), sorted (?sort=, one
// of the Compare columns, ?order=, asc or desc), the page asked for (see
// PageInfo); the rows the filters hide, what differs between all of them
// (eg: the assets) and the runs of the batch queued or running.
type BacktestComparisonResponse struct {
	PageInfo    `tstype:",extends"`
	Batch       BacktestBatchInfo       `json:"batch"`
	Rows        []BacktestComparisonRow `json:"rows"`
	Hidden      int                     `json:"hidden"`
	Differences []string                `json:"differences"`
	Active      int                     `json:"active"`
}

// PerformanceResponse is the payload of GET /api/performance: the trades of
// the agents trading live closed from From until before To (Unix seconds),
// read from the deals of their MT5 accounts as the accounts booked them, in
// the currency of each account, none converted, see PerformanceTrade; the
// accounts read, see PerformanceAccount. Complete and Missing as
// OpenPositionsResponse; At is when the deals were read.
type PerformanceResponse struct {
	From     int64                `json:"from"`
	To       int64                `json:"to"`
	Trades   []PerformanceTrade   `json:"trades"`
	Accounts []PerformanceAccount `json:"accounts"`
	Complete bool                 `json:"complete"`
	Missing  []OpenPositionsGap   `json:"missing,omitempty"`
	At       int64                `json:"at"`
}

// PerformanceTrade is a position of an agent closed during the range of a
// PerformanceResponse (its last exit then, nothing of it left open), on the
// account Account: its side, its volume in lots, its opening (Unix seconds,
// absent when its entry is older than the deals read, Entry then 0) and its
// closing, its entry price and the average price of its exits; Gross the
// profit of its exits, Costs the commission, fee and swap of its entry and
// exits (negative for a cost), Net both, in Currency.
type PerformanceTrade struct {
	Position int64   `json:"position"`
	Account  string  `json:"account"`
	Agent    string  `json:"agent"`
	Symbol   string  `json:"symbol"`
	Side     string  `json:"side"`
	Volume   float64 `json:"volume"`
	OpenedAt int64   `json:"opened_at,omitempty"`
	ClosedAt int64   `json:"closed_at"`
	Entry    float64 `json:"entry"`
	Exit     float64 `json:"exit"`
	Gross    float64 `json:"gross"`
	Costs    float64 `json:"costs"`
	Net      float64 `json:"net"`
	Currency string  `json:"currency"`
}

// PerformanceExcursionsResponse is the payload of GET
// /api/performance/excursions: how far the price went for and against each
// trade of the range of a PerformanceResponse while it was open, see
// PerformanceExcursion; Complete and Missing as OpenPositionsResponse; At is
// when they were read.
type PerformanceExcursionsResponse struct {
	From       int64                  `json:"from"`
	To         int64                  `json:"to"`
	Excursions []PerformanceExcursion `json:"excursions"`
	Complete   bool                   `json:"complete"`
	Missing    []OpenPositionsGap     `json:"missing,omitempty"`
	At         int64                  `json:"at"`
}

// PerformanceExcursion is the path of the price of a trade (its Position on
// its Account) from its opening to its closing, read from the candles of
// Timeframe of its symbol at the broker: High and Low the extremes of the
// price it could close at (the bid, and the ask of a short at its high),
// widened to its entry and exit; MFE and MAE the most it was up and down,
// positive, in the currency of its account, valued as the trade itself
// (its gross over its move). Error tells why a trade has none, eg: its
// opening unknown, its candles unread.
type PerformanceExcursion struct {
	Account   string  `json:"account"`
	Position  int64   `json:"position"`
	Timeframe string  `json:"timeframe,omitempty"`
	High      float64 `json:"high,omitempty"`
	Low       float64 `json:"low,omitempty"`
	MFE       float64 `json:"mfe"`
	MAE       float64 `json:"mae"`
	Error     string  `json:"error,omitempty"`
}

// PerformanceAccount is an account of the agents read for a
// PerformanceResponse: its key (its login), its currency, the agents trading
// on it, its balance now and at From (the balance less every deal booked
// since, deposits included): the capital of the return of the period.
type PerformanceAccount struct {
	Account      string   `json:"account"`
	Currency     string   `json:"currency"`
	Agents       []string `json:"agents"`
	Balance      float64  `json:"balance"`
	StartBalance float64  `json:"start_balance"`
}

// ClosedProfitSeries is the evolution of the closed profit of the agents in
// Currency over the range of a ClosedProfitResponse: Points the running total
// after the exits of each second with one, in the order of their times, the
// last one the total of the currency; none without an exit.
type ClosedProfitSeries struct {
	Currency string              `json:"currency"`
	Points   []ClosedProfitPoint `json:"points"`
}

// ClosedProfitPoint is the closed profit summed from the start of the range
// until the exits of At (Unix seconds) included, in the currency of its
// series.
type ClosedProfitPoint struct {
	At     int64   `json:"at"`
	Amount float64 `json:"amount"`
}

// AgentClosedProfit is the closed profit of an agent, see
// ClosedProfitResponse: Amount in Currency, the one of its account, of the
// Positions it closed (a part of). Running tells that the agent runs, else
// stopped or disabled.
type AgentClosedProfit struct {
	Agent     string  `json:"agent"`
	Amount    float64 `json:"amount"`
	Currency  string  `json:"currency"`
	Positions int     `json:"positions"`
	Running   bool    `json:"running"`
	// the positions won and lost, by the profit of all their exits, and the
	// sum of the profits of the ones won and of the losses of the ones lost
	Wins        int     `json:"wins,omitempty"`
	Losses      int     `json:"losses,omitempty"`
	GrossProfit float64 `json:"gross_profit,omitempty"`
	GrossLoss   float64 `json:"gross_loss,omitempty"`
}

// CurrencyAmount is an amount in Currency.
type CurrencyAmount struct {
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"`
}

// AgentPeriod is an accounting period of an agent: the cards of the dashboard
// (trades, win rate, net profit, profit factor) count the trades closed from
// StartedAt, the close of their order, until EndedAt, the start of the next
// period, absent for the current one; Deposit, in Currency, is the balance it
// started with, and CreatedBy the user who started it. The orders, the open
// positions and the pending orders are untouched: a period only tells which
// results count. Times in Unix seconds.
type AgentPeriod struct {
	ID        int64   `json:"id"`
	StartedAt int64   `json:"started_at"`
	EndedAt   *int64  `json:"ended_at,omitempty"`
	Deposit   float64 `json:"deposit"`
	Currency  string  `json:"currency"`
	CreatedBy string  `json:"created_by,omitempty"`
}

// AgentPeriodsResponse is the payload of GET
// /api/settings/agents/{name}/periods: the periods of the agent, the current
// one first, none before the first reset of its results.
type AgentPeriodsResponse struct {
	Periods []AgentPeriod `json:"periods"`
}

// AgentPeriodRequest is the payload of POST
// /api/settings/agents/{name}/periods, the reset of the results of every pair
// of the agent: a period starting now, answered with it (201), Deposit (from 0,
// rounded to cents) in Currency (a code of 3 letters, eg: BRL) the balance it
// starts with.
type AgentPeriodRequest struct {
	Deposit  float64 `json:"deposit"`
	Currency string  `json:"currency"`
}

// AgentAccountRequest is a change of the account of an agent, of the fields
// set, see AgentAccountSettings: a Magic of 0 and a Comment of "" remove them,
// their defaults applying. An agent without an account gets one, its
// BridgeURL required then.
type AgentAccountRequest struct {
	BridgeURL   *string `json:"bridge_url,omitempty"`
	Magic       *int64  `json:"magic,omitempty"`
	Comment     *string `json:"comment,omitempty"`
	RealAllowed *bool   `json:"real_allowed,omitempty"`
}

// AgentLLMRequest is a change of the language model of an LLM agent, of the
// fields set, see AgentLLMSettings: a BaseURL or a ReasoningEffort of "" and
// a MaxTokens of 0 remove them, the defaults of the
// agent applying. An LLM agent without a language model gets one, its
// Provider and its Model required then.
type AgentLLMRequest struct {
	Provider        *string `json:"provider,omitempty"`
	Model           *string `json:"model,omitempty"`
	BaseURL         *string `json:"base_url,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	MaxTokens       *int    `json:"max_tokens,omitempty"`
}

// AgentCreateRequest is the payload of POST /api/settings/agents, answered
// with the AgentSettings of the agent created (201). Its Name (letters, digits
// and . _ -, 64 at most) and its Kind (llm, trend, momentum, breakout or
// intraday) never change, and its APIAddress, host:port, is the one of no
// other agent. Launched and Restart are true when absent. Params are the
// flags of its settings that no field holds, see AgentSettings. LLM is the
// language model of an agent of kind llm, required, and PromptID its prompt,
// the one embedded in the agent when absent; Reanalysis allows its
// reanalysis, see AgentSettings, refused for another kind. The secrets are
// written, never returned. An agent created enabled is launched at once.
type AgentCreateRequest struct {
	Name            string               `json:"name"`
	Kind            string               `json:"kind"`
	Mode            string               `json:"mode"`
	APIAddress      string               `json:"api_address"`
	Enabled         bool                 `json:"enabled,omitempty"`
	Launched        *bool                `json:"launched,omitempty"`
	Restart         *bool                `json:"restart,omitempty"`
	TradePanel      bool                 `json:"trade_panel,omitempty"`
	TelegramControl bool                 `json:"telegram_control,omitempty"`
	Reanalysis      bool                 `json:"reanalysis,omitempty"`
	Params          map[string]string    `json:"params,omitempty"`
	Account         AgentAccountSettings `json:"account"`
	MT5AccountID    *int64               `json:"mt5_account_id,omitempty"` // its account of MT5, its bridge and token then
	LLM             *AgentLLMSettings    `json:"llm,omitempty"`
	PromptID        *int64               `json:"prompt_id,omitempty"`
	LLMAPIKey       *string              `json:"llm_api_key,omitempty"`
	BridgeToken     *string              `json:"bridge_token,omitempty"`
}

// PromptInfo is a prompt of the LLM agents without its content, with the
// agents using it; UpdatedAt in Unix seconds.
type PromptInfo struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     int      `json:"version"`
	UpdatedAt   int64    `json:"updated_at"`
	UsedBy      []string `json:"used_by"`
}

// PromptsResponse is the payload of GET /api/prompts, by name: the page asked
// for, see PageInfo.
type PromptsResponse struct {
	PageInfo `tstype:",extends"`
	Prompts  []PromptInfo `json:"prompts"`
}

// PromptDetail is a prompt with its content, the answer of GET, POST and PUT
// /api/prompts/{id}.
type PromptDetail struct {
	PromptInfo `tstype:",extends"`
	Content    string `json:"content"`
}

// PromptRequest is the payload of POST /api/prompts (Name, Description and
// Content) and of PUT /api/prompts/{id}, of the fields set: Version is the one
// the edit was made on, refused (409) when the prompt was edited since.
type PromptRequest struct {
	Name        string  `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Content     *string `json:"content,omitempty"`
	Version     int     `json:"version,omitempty"`
}

// TelegramSettingsResponse is the payload of GET /api/settings/telegram: the
// bot, whether it has a token, never the token, whether it runs, and the users
// it answers.
type TelegramSettingsResponse struct {
	Enabled  bool           `json:"enabled"`
	HasToken bool           `json:"has_token"`
	Running  bool           `json:"running"`
	Users    []TelegramUser `json:"users"`
}

// TelegramUser is a Telegram user the bot answers: ID is the one of the
// record, TelegramID the one of the user on Telegram.
type TelegramUser struct {
	ID         int64  `json:"id"`
	TelegramID int64  `json:"telegram_id"`
	Name       string `json:"name"`
}

// TelegramSettingsRequest is the payload of PATCH /api/settings/telegram, of
// the fields set: a token set to "" is removed.
type TelegramSettingsRequest struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Token   *string `json:"token,omitempty"`
}

// TelegramUsersResponse is the payload of GET /api/settings/telegram/users: the
// Telegram users the bot answers, by their ID, the page asked for (see
// PageInfo).
type TelegramUsersResponse struct {
	PageInfo `tstype:",extends"`
	Users    []TelegramUser `json:"users"`
}

// TelegramUserRequest is the payload of POST /api/settings/telegram/users.
type TelegramUserRequest struct {
	TelegramID int64  `json:"telegram_id"`
	Name       string `json:"name"`
}

// UserInfo is a user of the dashboard, its times in Unix seconds: Role its
// role, nil for none (it can't log in).
type UserInfo struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	Enabled     bool     `json:"enabled"`
	Role        *RoleRef `json:"role,omitempty"`
	LastLoginAt *int64   `json:"last_login_at,omitempty"`
	CreatedAt   int64    `json:"created_at"`
}

// UsersResponse is the payload of GET /api/users, by username: the page asked
// for, see PageInfo.
type UsersResponse struct {
	PageInfo `tstype:",extends"`
	Users    []UserInfo `json:"users"`
}

// UserRequest is the payload of POST /api/users (Username, Password and
// RoleID) and of PATCH /api/users/{id}, of the fields set: a RoleID 0 takes
// its role away, the user then refused at the login.
type UserRequest struct {
	Username string  `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
	RoleID   *int64  `json:"role_id,omitempty"`
}

// SupervisionState is the answer of GET /api/supervision of an agent, to the
// supervisor of the controller only (the channel supervisor): whether its
// entries are refused, why, and the revision of their last block or
// allowance (see order.Controller.BlockEntries), the status of its order
// controller (running or stopped), its positions as its exchange holds them,
// and its pending orders, the entries apart from the legs of the positions.
type SupervisionState struct {
	// Protections are the protection of the open positions, one per pair
	// held, see PositionProtection: absent without the protection.
	Protections     []PositionProtection `json:"protections,omitempty"`
	EntriesBlocked  bool                 `json:"entries_blocked"`
	EntriesReason   string               `json:"entries_reason,omitempty"`
	EntriesRevision int64                `json:"entries_revision"`
	Status          string               `json:"status"`
	Positions       []SupervisedPosition `json:"positions"`
	Pending         []SupervisedOrder    `json:"pending"`
}

// SupervisedPosition is the position an agent holds on a pair, BUY or SELL,
// its size in units of the base asset.
type SupervisedPosition struct {
	Pair     string  `json:"pair"`
	Side     string  `json:"side"`
	Quantity float64 `json:"quantity"`
}

// SupervisedOrder is a pending order of an agent: an Entry, which would open a
// position, or a leg protecting one.
type SupervisedOrder struct {
	ID       int64   `json:"id"`
	Pair     string  `json:"pair"`
	Side     string  `json:"side"`
	Type     string  `json:"type"`
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
	Entry    bool    `json:"entry"`
}

// SupervisionEntriesRequest is the payload of POST /api/supervision/entries:
// the entries of the agent blocked, for Reason, or allowed again, at
// Revision, ignored when older than the last change.
type SupervisionEntriesRequest struct {
	Blocked  bool   `json:"blocked"`
	Revision int64  `json:"revision"`
	Reason   string `json:"reason,omitempty"`
	// Drain, with Blocked, waits for the orders being sent to end (a few
	// seconds at most) before answering, eg: before the pending entries are
	// canceled, so that none sent just before the block is left
	Drain bool `json:"drain,omitempty"`
}

// SupervisionCloseRequest is the payload of POST /api/supervision/close: the
// positions of Pairs closed, all of them without any, never opening the
// other side; Block blocks the entries first, at Revision, for Reason, and
// they are held anyway as the positions are closed, then HoldSeconds more.
// CancelPending cancels the pending entries of the pairs first, under the
// same hold, so an entry filled as it is canceled is closed with the rest.
type SupervisionCloseRequest struct {
	Pairs         []string `json:"pairs,omitempty"`
	Block         bool     `json:"block"`
	Revision      int64    `json:"revision,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	HoldSeconds   int      `json:"hold_seconds,omitempty"`
	CancelPending bool     `json:"cancel_pending,omitempty"`
}

// SupervisionCloseResponse answers POST /api/supervision/close: a result by
// pair, and the cancels of the pending entries when asked (null when the
// agent doesn't cancel them, an earlier version).
type SupervisionCloseResponse struct {
	Results  []SupervisionCloseResult  `json:"results"`
	Canceled []SupervisionCancelResult `json:"canceled"`
}

// The statuses of a SupervisionCloseResult and a SupervisionCancelResult.
const (
	SupervisionClosed   = "closed"   // the position closed
	SupervisionFlat     = "flat"     // nothing held, nothing done
	SupervisionCanceled = "canceled" // the order canceled
	SupervisionGone     = "gone"     // the order ended before its cancel
	SupervisionFailed   = "failed"   // refused, nothing done
	SupervisionUnknown  = "unknown"  // maybe done: to check on the account, never to send again
)

// SupervisionCloseResult is how the close of the position of a pair went: the
// order closing it, its side and size, when closed, and the error else.
type SupervisionCloseResult struct {
	Pair     string  `json:"pair"`
	Status   string  `json:"status"`
	Side     string  `json:"side,omitempty"`
	Quantity float64 `json:"quantity,omitempty"`
	Error    string  `json:"error,omitempty"`
}

// SupervisionCancelRequest is the payload of POST /api/supervision/cancel: the
// pending entries of Pairs canceled, all of them without any; the legs of
// the positions stay.
type SupervisionCancelRequest struct {
	Pairs []string `json:"pairs,omitempty"`
}

// SupervisionCancelResponse answers POST /api/supervision/cancel: a result by
// pending entry.
type SupervisionCancelResponse struct {
	Results []SupervisionCancelResult `json:"results"`
}

// SupervisionProtectRequest is the payload of POST /api/supervision/protect
// of an agent, from the supervisor only: arm Feature (breakeven or trailing)
// on the open position PositionRef of Pair, as the arm ArmID of the
// controller recorded it with Reason; the agent applies it once its trigger
// is reached, at once when it is.
type SupervisionProtectRequest struct {
	ArmID       int64  `json:"arm_id"`
	Pair        string `json:"pair"`
	PositionRef string `json:"position_ref"`
	Feature     string `json:"feature"`
	Reason      string `json:"reason"`
}

// The statuses of an arm of the protection, see SupervisionProtectResponse.
const (
	ProtectApplied = "applied" // the stop loss moved, confirmed by the broker
	ProtectArmed   = "armed"   // armed: applied once its trigger is reached
	ProtectRefused = "refused" // not armed, see Detail: eg: not allowed, another position
	ProtectFailed  = "failed"  // armed, the move refused by the broker: the stop loss kept
	ProtectUnknown = "unknown" // armed, the answer of the broker lost: the stop loss read again
)

// SupervisionProtectResponse is how the arm went: its status (see
// ProtectApplied), the stop loss before, the one asked, the one the broker
// confirmed, why (Detail), and the protection of the position after.
type SupervisionProtectResponse struct {
	Status     string              `json:"status"`
	Previous   float64             `json:"previous"`
	Requested  float64             `json:"requested,omitempty"`
	Confirmed  float64             `json:"confirmed,omitempty"`
	Detail     string              `json:"detail,omitempty"`
	Protection *PositionProtection `json:"protection,omitempty"`
}

// SupervisionCancelResult is how the cancel of a pending entry went.
type SupervisionCancelResult struct {
	ID     int64  `json:"id"`
	Pair   string `json:"pair"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// AgentSupervisionInfo is the supervision of an agent on the dashboard: its
// entries blocked, why, by whom and when (Unix seconds), and its stop by the
// supervisor: pending_flat while it waits for the agent to hold nothing,
// stopped once disabled, until a user enables it again; none otherwise.
type AgentSupervisionInfo struct {
	EntriesBlocked bool   `json:"entries_blocked"`
	EntriesReason  string `json:"entries_reason,omitempty"`
	EntriesBy      string `json:"entries_by,omitempty"`
	EntriesAt      *int64 `json:"entries_at,omitempty"`
	StopState      string `json:"stop_state"`
	StopReason     string `json:"stop_reason,omitempty"`
	StopCategory   string `json:"stop_category,omitempty"`
	StopBy         string `json:"stop_by,omitempty"`
	StopAt         *int64 `json:"stop_at,omitempty"`
	// DailyBlocked tells its entries blocked by a daily guard, its own or the
	// one of all the agents, until the next day, DailyReason why
	DailyBlocked bool   `json:"daily_blocked,omitempty"`
	DailyReason  string `json:"daily_reason,omitempty"`
}

// SupervisorAutonomy is what the supervisor does alone in its analyses, see
// store.Autonomy: closing positions is never one of them.
type SupervisorAutonomy struct {
	BlockEntries     bool `json:"block_entries"`
	StopWhenFlat     bool `json:"stop_when_flat"`
	CancelPending    bool `json:"cancel_pending"`
	MaxActionsPerRun int  `json:"max_actions_per_run"`
}

// SupervisorSettingsResponse is the payload of GET /api/settings/supervisor:
// its settings, whether its key is set, never its value, the bounds of the
// interval and of the timeout, in seconds, and its state: an analysis
// running, the next one (Unix seconds) and the failed ones in a row.
type SupervisorSettingsResponse struct {
	Configured      bool               `json:"configured"` // false before the seeds: run the migrator
	Enabled         bool               `json:"enabled"`
	IntervalSeconds int                `json:"interval_seconds"`
	Provider        string             `json:"provider,omitempty"`
	Model           string             `json:"model,omitempty"`
	BaseURL         string             `json:"base_url,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
	MaxTokens       int                `json:"max_tokens,omitempty"`
	TimeoutSeconds  int                `json:"timeout_seconds"`
	Instructions    string             `json:"instructions"`
	Autonomy        SupervisorAutonomy `json:"autonomy"`
	APIKeySet       bool               `json:"api_key_set"`
	MinInterval     int                `json:"min_interval"`
	MaxInterval     int                `json:"max_interval"`
	MinTimeout      int                `json:"min_timeout"`
	MaxTimeout      int                `json:"max_timeout"`
	MaxActions      int                `json:"max_actions"`
	// the max tokens of an answer without MaxTokens, a model that reasons
	// spending them thinking too
	DefaultMaxTokens int    `json:"default_max_tokens"`
	Analyzing        bool   `json:"analyzing"`
	NextRunAt        *int64 `json:"next_run_at,omitempty"`
	FailuresInRow    int    `json:"failures_in_row"`
	UpdatedAt        int64  `json:"updated_at"`
}

// SupervisorSettingsRequest is the payload of PATCH /api/settings/supervisor,
// of the fields set: an empty BaseURL or ReasoningEffort and a MaxTokens of 0
// remove them; APIKey sets the key, "" removes it.
type SupervisorSettingsRequest struct {
	Enabled         *bool               `json:"enabled,omitempty"`
	IntervalSeconds *int                `json:"interval_seconds,omitempty"`
	Provider        *string             `json:"provider,omitempty"`
	Model           *string             `json:"model,omitempty"`
	BaseURL         *string             `json:"base_url,omitempty"`
	ReasoningEffort *string             `json:"reasoning_effort,omitempty"`
	MaxTokens       *int                `json:"max_tokens,omitempty"`
	TimeoutSeconds  *int                `json:"timeout_seconds,omitempty"`
	Instructions    *string             `json:"instructions,omitempty"`
	Autonomy        *SupervisorAutonomy `json:"autonomy,omitempty"`
	APIKey          *string             `json:"api_key,omitempty"`
}

// SupervisorSnapshot is what the supervisor knows of the agents, sent to its
// model as is, never a secret: when it was made, in the zone of the
// controller, the windows of the realized results, the accounts by an alias
// with their currency, the exposure of the agents on each account and pair,
// the agents, and what couldn't be read. Money comes with its currency,
// never converted nor summed across currencies; the volumes in lots.
type SupervisorSnapshot struct {
	GeneratedAt string               `json:"generated_at"`
	Timezone    string               `json:"timezone"`
	Windows     []SupervisorWindow   `json:"windows"`
	Accounts    []SupervisorAccount  `json:"accounts"`
	Exposure    []SupervisorExposure `json:"exposure"`
	Agents      []SupervisorAgent    `json:"agents"`
	Gaps        []string             `json:"gaps,omitempty"`
	// DailyGuard is how the daily limits stand: the result of the day
	// considered, the limits and the guards, see DailyLimitsStatus
	DailyGuard *DailyLimitsStatus `json:"daily_guard,omitempty"`
}

// SupervisorWindow is a window of the realized results, its start and end.
type SupervisorWindow struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// SupervisorAccount is an account of the agents trading live: its alias, its
// currency and balance, when it was read, why not (Gap), and its agents.
type SupervisorAccount struct {
	Account  string   `json:"account"`
	Currency string   `json:"currency,omitempty"`
	Balance  *float64 `json:"balance,omitempty"`
	ReadAt   string   `json:"read_at,omitempty"`
	Gap      string   `json:"gap,omitempty"`
	Agents   []string `json:"agents"`
}

// SupervisorExposure is the exposure of the agents on a pair of an account:
// the lots and positions bought and sold, the agents on each side, and the
// open profit of them, in the currency of the account.
type SupervisorExposure struct {
	Account       string   `json:"account"`
	Currency      string   `json:"currency"`
	Pair          string   `json:"pair"`
	BuyLots       float64  `json:"buy_lots"`
	SellLots      float64  `json:"sell_lots"`
	BuyPositions  int      `json:"buy_positions"`
	SellPositions int      `json:"sell_positions"`
	BuyAgents     []string `json:"buy_agents,omitempty"`
	SellAgents    []string `json:"sell_agents,omitempty"`
	OpenProfit    float64  `json:"open_profit"`
}

// SupervisorAgent is an agent as the supervisor knows it: its settings, the
// state of its process and of its outbox, its supervision, its limits of risk
// (the ones of its settings, by the name of their flag, and the daily ones of
// its prompt), its positions and pending orders, its results and its last
// decisions.
type SupervisorAgent struct {
	Name           string                 `json:"name"`
	Kind           string                 `json:"kind"`
	Strategy       string                 `json:"strategy,omitempty"`
	Mode           string                 `json:"mode"`
	Enabled        bool                   `json:"enabled"`
	State          string                 `json:"state"`
	Reachable      bool                   `json:"reachable"`
	Restarts       int                    `json:"restarts,omitempty"`
	LastExit       string                 `json:"last_exit,omitempty"`
	LaunchError    string                 `json:"launch_error,omitempty"`
	RestartPending bool                   `json:"restart_pending,omitempty"`
	Outbox         *AgentOutbox           `json:"outbox,omitempty"`
	EntriesBlocked bool                   `json:"entries_blocked"`
	EntriesReason  string                 `json:"entries_reason,omitempty"`
	StopState      string                 `json:"stop_state,omitempty"`
	StopReason     string                 `json:"stop_reason,omitempty"`
	Account        string                 `json:"account,omitempty"`
	Pairs          []string               `json:"pairs"`
	Timeframe      string                 `json:"timeframe,omitempty"`
	Risk           map[string]string      `json:"risk,omitempty"`
	DailyLimits    *SupervisorDailyLimits `json:"daily_limits,omitempty"`
	Model          string                 `json:"model,omitempty"`
	Prompt         string                 `json:"prompt,omitempty"`
	PromptStatus   string                 `json:"prompt_status,omitempty"`
	PeriodStart    string                 `json:"period_start,omitempty"`
	Positions      []SupervisorPosition   `json:"positions"`
	Pending        []SupervisorPending    `json:"pending"`
	OpenProfit     []CurrencyAmount       `json:"open_profit"`
	Realized       []SupervisorRealized   `json:"realized"`
	Decisions      []SupervisorDecision   `json:"decisions,omitempty"`
	// Protection is the protection of its positions, when it runs or the
	// XTrader AI may arm it, and Protections how it stands on each position,
	// as the agent tells it: the reference of the position protect_position
	// takes, its entry, price, stop loss, ATR in price, best move in ATR and
	// the state of its breakeven and trailing stop
	Protection  *ProtectionSettings  `json:"protection,omitempty"`
	Protections []PositionProtection `json:"protections,omitempty"`
}

// SupervisorProtectionInfo is a protection the XTrader AI armed on a position
// of an agent (breakeven or trailing stop): who asked (the analysis RunID,
// the user of a conversation), why, its status (requested, armed, active,
// refused, ended) and its last move of the stop loss, Move confirmed,
// rejected or unknown: the stop loss before, asked and confirmed by the
// broker, 0 for none; times in Unix seconds.
type SupervisorProtectionInfo struct {
	ID          int64    `json:"id"`
	Agent       string   `json:"agent"`
	RunID       *int64   `json:"run_id,omitempty"`
	Pair        string   `json:"pair"`
	PositionRef string   `json:"position_ref"`
	Ticket      int64    `json:"ticket,omitempty"`
	Side        string   `json:"side"`
	Feature     string   `json:"feature"`
	Reason      string   `json:"reason"`
	Category    string   `json:"category,omitempty"`
	Evidence    []string `json:"evidence"`
	RequestedBy string   `json:"requested_by"`
	Status      string   `json:"status"`
	Move        string   `json:"move,omitempty"`
	Previous    float64  `json:"previous"`
	Requested   float64  `json:"requested"`
	Confirmed   float64  `json:"confirmed"`
	Detail      string   `json:"detail,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	EndedAt     *int64   `json:"ended_at,omitempty"`
}

// SupervisorProtectionsResponse is the payload of GET
// /api/supervisor/protections: the arms of the XTrader AI, the newest first,
// the last ones or the page asked for (see PageInfo).
type SupervisorProtectionsResponse struct {
	PageInfo    `tstype:",extends"`
	Protections []SupervisorProtectionInfo `json:"protections"`
}

// SupervisorDailyLimits are the daily limits of the prompt of an LLM agent:
// the gain and the loss after which it takes no entry on a pair that day.
type SupervisorDailyLimits struct {
	Gain     float64 `json:"gain,omitempty"`
	Loss     float64 `json:"loss,omitempty"`
	Currency string  `json:"currency"`
}

// SupervisorPosition is a position of an agent: on its account (its ticket,
// its levels at the broker, 0 for none, its profit in the currency of the
// account, when it opened), or simulated, in paper (Paper, its size in units
// of the base asset, without ticket nor levels).
type SupervisorPosition struct {
	Ticket     int64   `json:"ticket,omitempty"`
	Pair       string  `json:"pair"`
	Side       string  `json:"side"`
	Volume     float64 `json:"volume"`
	Entry      float64 `json:"entry,omitempty"`
	Price      float64 `json:"price,omitempty"`
	StopLoss   float64 `json:"stop_loss"`
	TakeProfit float64 `json:"take_profit"`
	Profit     float64 `json:"profit"`
	Currency   string  `json:"currency,omitempty"`
	OpenedAt   string  `json:"opened_at,omitempty"`
	Paper      bool    `json:"paper,omitempty"`
}

// SupervisorPending is a pending order of an agent: its ticket on the account,
// or its ID in paper, and whether it is an entry or a leg of a position.
type SupervisorPending struct {
	Ticket     int64   `json:"ticket"`
	Pair       string  `json:"pair"`
	Side       string  `json:"side"`
	Type       string  `json:"type"`
	Price      float64 `json:"price"`
	Volume     float64 `json:"volume"`
	StopLoss   float64 `json:"stop_loss,omitempty"`
	TakeProfit float64 `json:"take_profit,omitempty"`
	Entry      bool    `json:"entry"`
	Paper      bool    `json:"paper,omitempty"`
}

// SupervisorRealized is the result an agent realized in a window, as its
// account booked it, in its currency: the positions closed, won and lost,
// the sum of the profits of the ones won and of the losses of the ones lost.
type SupervisorRealized struct {
	Window      string  `json:"window"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
	Positions   int     `json:"positions"`
	Wins        int     `json:"wins"`
	Losses      int     `json:"losses"`
	GrossProfit float64 `json:"gross_profit"`
	GrossLoss   float64 `json:"gross_loss"`
}

// SupervisorDecision is a recent decision of an LLM agent: when, on which
// pair, its action, confidence, outcome and the start of its reason.
type SupervisorDecision struct {
	At         string  `json:"at"`
	Pair       string  `json:"pair"`
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	Outcome    string  `json:"outcome"`
	Reason     string  `json:"reason,omitempty"`
}

// SupervisorFinding is a finding of an analysis of the supervisor about an
// agent, or the agents as a whole without one: its category (operational,
// risk or performance), severity (info, warning or critical), what it rests
// on, and what it recommends, eg: stop.
type SupervisorFinding struct {
	Agent          string `json:"agent,omitempty"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`
	Summary        string `json:"summary"`
	Evidence       string `json:"evidence,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
}

// SupervisorRunInfo is an analysis of the supervisor, its times in Unix
// seconds: schedule or manual, running, ok, failed or skipped.
type SupervisorRunInfo struct {
	ID          int64               `json:"id"`
	Origin      string              `json:"origin"`
	Status      string              `json:"status"`
	RequestedBy string              `json:"requested_by,omitempty"`
	Model       string              `json:"model,omitempty"`
	StartedAt   int64               `json:"started_at"`
	FinishedAt  *int64              `json:"finished_at,omitempty"`
	Summary     string              `json:"summary,omitempty"`
	Findings    []SupervisorFinding `json:"findings"`
	Error       string              `json:"error,omitempty"`
	ActionIDs   []int64             `json:"action_ids,omitempty"`
	// ProtectionIDs are the protections of positions the analysis armed, see
	// SupervisorProtectionInfo
	ProtectionIDs []int64 `json:"protection_ids,omitempty"`
}

// SupervisorRunsResponse is the payload of GET /api/supervisor/runs, the
// newest first: the last ones or the page asked for (see PageInfo).
type SupervisorRunsResponse struct {
	PageInfo `tstype:",extends"`
	Runs     []SupervisorRunInfo `json:"runs"`
}

// SupervisorRunResponse is the payload of GET /api/supervisor/runs/{id}: the
// analysis and the data sent to the model.
type SupervisorRunResponse struct {
	Run      SupervisorRunInfo   `json:"run"`
	Snapshot *SupervisorSnapshot `json:"snapshot,omitempty"`
}

// SupervisorScope is what an action of the supervision acts on: the agents,
// the pairs (all of them without any), and, confirmed, the positions and the
// pending orders it was proposed with, the ones of agents not running apart
// (closed on their bridge).
type SupervisorScope struct {
	Agents    []string                  `json:"agents"`
	Pairs     []string                  `json:"pairs,omitempty"`
	Positions []SupervisorScopePosition `json:"positions,omitempty"`
	Orders    []SupervisorScopeOrder    `json:"orders,omitempty"`
}

// SupervisorScopePosition is a position an action closes: of an agent, on an
// account, as it was read when proposed.
type SupervisorScopePosition struct {
	Agent              string `json:"agent"`
	SupervisorPosition `tstype:",extends"`
	Account            string `json:"account,omitempty"`
	Running            bool   `json:"running"`
}

// SupervisorScopeOrder is a pending entry an action cancels: of an agent.
type SupervisorScopeOrder struct {
	Agent             string `json:"agent"`
	SupervisorPending `tstype:",extends"`
	Running           bool `json:"running"`
}

// SupervisorOptions is what goes with an action: the pending entries of the
// agents canceled, their entries kept blocked once their positions closed.
type SupervisorOptions struct {
	CancelPending bool `json:"cancel_pending"`
	KeepBlocked   bool `json:"keep_blocked"`
	// Whole closes every position of the agents read when executed, the ones
	// opened after the action was built included (eg: by a daily limit)
	Whole bool `json:"whole,omitempty"`
}

// SupervisorResultItem is how an action went on an agent, a position or an
// order, as checked: done, already (nothing to do), pending (applied once the
// agent runs, or once it is flat), failed (nothing done) or unknown (maybe
// done: to check on the account, never sent again).
type SupervisorResultItem struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Pair   string `json:"pair,omitempty"`
	Ticket int64  `json:"ticket,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// SupervisorResult is the result of an action, checked at CheckedAt (Unix
// seconds).
type SupervisorResult struct {
	Items     []SupervisorResultItem `json:"items"`
	Error     string                 `json:"error,omitempty"`
	CheckedAt int64                  `json:"checked_at,omitempty"`
}

// SupervisorActionInfo is an action of the supervision on the agents, its
// times in Unix seconds: asked by the supervisor (alone, or proposed) or by a
// user, or taken by a rule of the controller (Origin rule: the daily limit or
// the trading hours, RequestedBy naming it); block_entries, allow_entries, stop_agent (when_flat or now),
// cancel_pending or close_positions; why, of which category (operational,
// risk or performance); proposed until confirmed, rejected or expired, then
// executing, succeeded, partial, failed or unknown.
type SupervisorActionInfo struct {
	ID          int64             `json:"id"`
	Source      string            `json:"source"`
	Origin      string            `json:"origin"` // supervisor (XTrader AI), user or rule
	RunID       *int64            `json:"run_id,omitempty"`
	Action      string            `json:"action"`
	Mode        string            `json:"mode,omitempty"`
	Scope       SupervisorScope   `json:"scope"`
	Options     SupervisorOptions `json:"options"`
	Reason      string            `json:"reason"`
	Category    string            `json:"category,omitempty"`
	Evidence    []string          `json:"evidence"`
	Status      string            `json:"status"`
	Result      *SupervisorResult `json:"result,omitempty"`
	RequestedBy string            `json:"requested_by,omitempty"`
	ConfirmedBy string            `json:"confirmed_by,omitempty"`
	CreatedAt   int64             `json:"created_at"`
	ExpiresAt   *int64            `json:"expires_at,omitempty"`
	DecidedAt   *int64            `json:"decided_at,omitempty"`
	FinishedAt  *int64            `json:"finished_at,omitempty"`
}

// SupervisorActionsResponse is the payload of GET /api/supervisor/actions,
// the newest first: the last ones or the page asked for (see PageInfo).
type SupervisorActionsResponse struct {
	PageInfo `tstype:",extends"`
	Actions  []SupervisorActionInfo `json:"actions"`
}

// SupervisorMessageInfo is a message of the conversation of the user with the
// supervisor, its times in Unix seconds: a question of the user, an answer of
// the supervisor (analytical), a proposal of an action, its result once
// confirmed and checked, or an error, eg: the model unavailable; DataAt is
// when the data the answer rests on was read.
type SupervisorMessageInfo struct {
	ID        int64                 `json:"id"`
	Role      string                `json:"role"`
	Kind      string                `json:"kind"`
	Content   string                `json:"content"`
	Action    *SupervisorActionInfo `json:"action,omitempty"`
	DataAt    *int64                `json:"data_at,omitempty"`
	CreatedAt int64                 `json:"created_at"`
}

// SupervisorMessagesResponse is the payload of GET /api/supervisor/messages,
// the oldest first, and of POST /api/supervisor/messages, the messages it
// added; More tells there are older ones.
type SupervisorMessagesResponse struct {
	Messages []SupervisorMessageInfo `json:"messages"`
	More     bool                    `json:"more,omitempty"`
}

// SupervisorMessageRequest is the payload of POST /api/supervisor/messages
// and of POST /api/supervisor/messages/stream: a question or a request of the
// user to the supervisor.
type SupervisorMessageRequest struct {
	Text string `json:"text"`
}

// The events of POST /api/supervisor/messages/stream, server-sent: "message"
// a SupervisorMessageInfo kept (the question, then each proposal as the model
// makes it), "stage" a SupervisorStreamStage, "text" a SupervisorStreamText,
// "error" a SupervisorStreamError, and last "done", a
// SupervisorMessagesResponse with every message the answer added, as POST
// /api/supervisor/messages answers them.
const (
	SupervisorEventMessage = "message"
	SupervisorEventStage   = "stage"
	SupervisorEventText    = "text"
	SupervisorEventError   = "error"
	SupervisorEventDone    = "done"
)

// SupervisorStreamStage is where an answer of the supervisor is: reading (the
// agents read for the model), thinking (the model writing its answer) or tool
// (a tool the model called runs, Tool, eg: get_agent or propose_action). The
// text of a stage is dropped at the next one: the text the model writes
// before calling a tool isn't its answer.
type SupervisorStreamStage struct {
	Stage string `json:"stage"`
	Tool  string `json:"tool,omitempty"`
}

// SupervisorStreamText is a piece of the text the model of the supervisor
// writes, in Markdown.
type SupervisorStreamText struct {
	Text string `json:"text"`
}

// SupervisorStreamError is an error that ended the stream of an answer before
// its end; what the server kept is read with GET /api/supervisor/messages.
type SupervisorStreamError struct {
	Error string `json:"error"`
}

// SupervisorEntriesRequest is the payload of POST
// /api/supervisor/agents/{name}/entries: the entries of the agent blocked,
// for Reason, or allowed again, by the user.
type SupervisorEntriesRequest struct {
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason,omitempty"`
}

// SupervisorCloseRequest is the payload of POST
// /api/supervisor/agents/{name}/close: every open position of the agent
// closed by the user, for Reason (a default naming the user without one); and
// of POST /api/supervisor/close-all: every position of the agents closed and
// their pending entries canceled.
type SupervisorCloseRequest struct {
	Reason string `json:"reason,omitempty"`
}

// DailyLimitSettings are daily limits: of all the agents together (their
// amounts applying to the result in each currency of the accounts apart, none
// converted) or of an agent (in the currency of its account); Currency is
// always empty, refused when set. Enabled, at MaxLoss lost or MaxGain won in the
// result of the day (realized plus open, see DailyResult) the application
// closes their positions, cancels their pending entries and blocks their
// entries until the next day; absent, no limit of that side.
type DailyLimitSettings struct {
	Enabled   bool     `json:"enabled"`
	Currency  string   `json:"currency,omitempty"`
	MaxLoss   *float64 `json:"max_loss,omitempty"`
	MaxGain   *float64 `json:"max_gain,omitempty"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
}

// DailyResult is the result of the day of agents in Currency: Realized, the
// exits of the day with their swap, commission and fee and the commission and
// fee of the entries of the day; Open, the profit and swap of their positions
// open now; Total, both. Complete false when an account wasn't read, Missing
// telling which: never counted as 0.
type DailyResult struct {
	Currency string   `json:"currency"`
	Realized float64  `json:"realized"`
	Open     float64  `json:"open"`
	Total    float64  `json:"total"`
	Complete bool     `json:"complete"`
	Missing  []string `json:"missing,omitempty"`
}

// The states of a scope of the daily limits: off (no limit), free (under its
// limits), blocked (a limit reached: its positions closed, its entries
// blocked until the next day) or incomplete (its result unknown: its entries
// blocked until it is read again).
const (
	DailyOff        = "off"
	DailyFree       = "free"
	DailyBlocked    = "blocked"
	DailyIncomplete = "incomplete"
)

// DailyGuardInfo is a daily guard tripped: Kind limit (Trigger loss or gain,
// Result and Limit in Currency) or incomplete; how its positions were closed
// (FlattenStatus running, done, partial or failed, FlattenActionID the
// action); times in Unix seconds.
type DailyGuardInfo struct {
	ID              int64   `json:"id"`
	Kind            string  `json:"kind"`
	Trigger         string  `json:"trigger,omitempty"`
	Result          float64 `json:"result"`
	Limit           float64 `json:"limit"`
	Currency        string  `json:"currency"`
	Reason          string  `json:"reason"`
	Trips           int     `json:"trips"`
	TrippedAt       int64   `json:"tripped_at"`
	FlattenStatus   string  `json:"flatten_status,omitempty"`
	FlattenActionID *int64  `json:"flatten_action_id,omitempty"`
}

// DailyScopeStatus is a scope of the daily limits now: all the agents (Agent
// empty) or one; its limits, its result of the day (of all the agents, the
// one of Results its limits weigh: reaching one, else the closest to one),
// its results by currency of the accounts (of all the agents only), its state
// (see DailyOff) and the guard blocking it, if any.
type DailyScopeStatus struct {
	Agent   string             `json:"agent,omitempty"`
	Limits  DailyLimitSettings `json:"limits"`
	Result  *DailyResult       `json:"result,omitempty"`
	Results []DailyResult      `json:"results,omitempty"`
	State   string             `json:"state"`
	Guard   *DailyGuardInfo    `json:"guard,omitempty"`
}

// DailyLimitsStatus is the payload of GET /api/daily-guard: the day (eg:
// 2026-10-29) in Zone, from its midnight, when it was evaluated (Unix
// seconds, 0 not yet), all the agents together, the agents with their own
// limits or guards, and the agents in paper, never counted (no money of an
// account) though blocked and closed with all the agents.
type DailyLimitsStatus struct {
	Day         string             `json:"day"`
	Zone        string             `json:"zone"`
	EvaluatedAt int64              `json:"evaluated_at"`
	Global      DailyScopeStatus   `json:"global"`
	Agents      []DailyScopeStatus `json:"agents"`
	Paper       []string           `json:"paper,omitempty"`
	Hours       TradingHoursStatus `json:"hours"`
}

// TradingWindow is a window of the trading hours of a day: from Start to End
// (HH:MM, of the zone of the controller, America/Belem; Start before End, a
// window never crossing midnight; the end itself outside), the agents allowed
// to open positions in it (Agents, by name; none for all of them) and the
// assets they may open them on (Pairs, eg: XAUUSDm; none for all of them),
// and what is done at its end: every position of the agents closed (Flatten)
// and their pending orders canceled (CancelPending), as checked, whatever its
// Agents and Pairs.
type TradingWindow struct {
	Start         string   `json:"start"`
	End           string   `json:"end"`
	Agents        []string `json:"agents,omitempty"`
	Pairs         []string `json:"pairs,omitempty"`
	Flatten       bool     `json:"flatten"`
	CancelPending bool     `json:"cancel_pending"`
}

// TradingHoursSettings are the trading hours of all the agents: on, no agent
// opens a position outside its Windows (in the order of the day, none
// overlapping nor touching another), and at the end of each every position of
// the agents is closed and their pending orders canceled, as it checks.
// Start, End, Flatten and CancelPending are its first window, for the clients
// of a single window: a request without Windows takes them as its only one.
// UpdatedBy and UpdatedAt (Unix seconds) tell the last change.
type TradingHoursSettings struct {
	Enabled       bool            `json:"enabled"`
	Windows       []TradingWindow `json:"windows"`
	Start         string          `json:"start"`
	End           string          `json:"end"`
	Flatten       bool            `json:"flatten"`
	CancelPending bool            `json:"cancel_pending"`
	UpdatedBy     string          `json:"updated_by,omitempty"`
	UpdatedAt     int64           `json:"updated_at,omitempty"`
}

// TradingHoursStatus is how the trading hours stand: on or off, their
// Windows (in Zone), Open while inside one (Window, the one open), Blocked
// while the entries of the agents are blocked by them (as recorded and sent
// to the agents), NextAt the next change (the end of the window open, else the
// start of the next one; Unix seconds, 0 while off), and the actions of the
// last end of a window, if any. Start and End are the window open, else the
// next one.
type TradingHoursStatus struct {
	Enabled bool             `json:"enabled"`
	Windows []TradingWindow  `json:"windows,omitempty"`
	Window  *TradingWindow   `json:"window,omitempty"`
	Start   string           `json:"start,omitempty"`
	End     string           `json:"end,omitempty"`
	Zone    string           `json:"zone"`
	Open    bool             `json:"open"`
	Blocked bool             `json:"blocked"`
	NextAt  int64            `json:"next_at,omitempty"`
	LastEnd *TradingHoursEnd `json:"last_end,omitempty"`
}

// TradingHoursEnd is how the actions of an end of a window went: when (Unix
// seconds), the window (Start, End; none for the ends of before), what was
// asked (Flatten, CancelPending), the status of the action (running while it
// runs, else the one of its result: succeeded, partial, failed or unknown) and
// the action of the XTrader AI recorded.
type TradingHoursEnd struct {
	At            int64  `json:"at"`
	Start         string `json:"start,omitempty"`
	End           string `json:"end,omitempty"`
	Flatten       bool   `json:"flatten"`
	CancelPending bool   `json:"cancel_pending"`
	Status        string `json:"status"`
	ActionID      *int64 `json:"action_id,omitempty"`
}

// TradingHoursResponse answers GET and PUT /api/settings/trading-hours: the
// hours and how they stand.
type TradingHoursResponse struct {
	Settings TradingHoursSettings `json:"settings"`
	Status   TradingHoursStatus   `json:"status"`
}

// TradingHoursRequest is the body of PUT /api/settings/trading-hours: the
// hours, and why (Reason, recorded).
type TradingHoursRequest struct {
	Settings TradingHoursSettings `json:"settings"`
	Reason   string               `json:"reason,omitempty"`
}

// ExposureSettings is the exposure limit of the agents, by rules: Pairs, at
// most so many agents holding a position or a pending entry on the asset in
// the same direction at once (eg: XAUUSDm: 2), from 1 to 100; an asset
// without a rule without a limit, none for none. An entry beyond it refused,
// the exits, the stops, the targets and the orders by hand never. UpdatedBy
// and UpdatedAt (Unix seconds) tell the last change.
type ExposureSettings struct {
	Pairs     map[string]int `json:"pairs,omitempty"`
	UpdatedBy string         `json:"updated_by,omitempty"`
	UpdatedAt int64          `json:"updated_at,omitempty"`
}

// ExposureRequest is the body of PUT /api/settings/exposure.
type ExposureRequest struct {
	Settings ExposureSettings `json:"settings"`
}

// ExposureResponse answers GET and PUT /api/settings/exposure.
type ExposureResponse struct {
	Settings ExposureSettings `json:"settings"`
}

// DailyLimitsRequest is the body of PUT /api/settings/daily-limits (the
// limits of all the agents together) and of PUT
// /api/settings/agents/{name}/daily-limit (of an agent): the limits, why
// (Reason, recorded), and Confirm to apply a change that trips or releases
// the guard at once (else refused, 409, a DailyLimitPreview).
type DailyLimitsRequest struct {
	Settings DailyLimitSettings `json:"settings"`
	Reason   string             `json:"reason,omitempty"`
	Confirm  bool               `json:"confirm,omitempty"`
}

// The effects of a change of daily limits on their guard now, see
// DailyLimitPreview.
const (
	DailyEffectNone    = "none"
	DailyEffectTrip    = "trip"
	DailyEffectRelease = "release"
)

// DailyLimitPreview is what a change of daily limits would do now (Effect,
// see DailyEffectNone), against Result (of all the agents, the one of Results
// weighed, see DailyScopeStatus); the answer (409) of a change of an effect not
// confirmed.
type DailyLimitPreview struct {
	Effect  string        `json:"effect"`
	Result  *DailyResult  `json:"result,omitempty"`
	Results []DailyResult `json:"results,omitempty"`
	Message string        `json:"message"`
}

// DailyLimitsResponse is the payload of GET and PUT
// /api/settings/daily-limits and of PUT /api/settings/agents/{name}/daily-limit:
// the limits of all the agents together, and how the daily limits stand.
type DailyLimitsResponse struct {
	Settings DailyLimitSettings `json:"settings"`
	Status   DailyLimitsStatus  `json:"status"`
	// Effect of the change just applied, see DailyEffectNone
	Effect string `json:"effect,omitempty"`
}

// DailyAgentsResponse is the payload of GET /api/settings/daily-limits/agents:
// the agents with daily limits or guards of their own as they stand now, in
// the order of DailyLimitsStatus.Agents, the page asked for (see PageInfo),
// and the zone of their day.
type DailyAgentsResponse struct {
	PageInfo `tstype:",extends"`
	Agents   []DailyScopeStatus `json:"agents"`
	Zone     string             `json:"zone"`
}

// NewsSettingsResponse is the payload of GET and PUT /api/settings/news: the
// news of all the agents, LLM or not: on or off (off, no agent reads the
// economic calendar nor blocks anything); the MCP server of the calendar (its
// address, whether its token is set: never the token); the window of the news
// block in minutes (BlockBefore before a release of at least BlockImportance
// to BlockAfter after it, both 0 for none: inside it, no agent opens a
// position, their pending entries canceled and their positions closed as it
// starts); the agents whose former tokens (Conflict) or servers (URLConflict)
// differed, none chosen, until set here; who changed them last and when (Unix
// seconds, 0 never); and the agents, whether enabled and running: a change
// applies to them while they run, without a restart.
type NewsSettingsResponse struct {
	Enabled         bool        `json:"enabled"`
	URL             string      `json:"url,omitempty"`
	TokenSet        bool        `json:"token_set"`
	BlockBefore     int         `json:"block_before"`
	BlockAfter      int         `json:"block_after"`
	BlockImportance string      `json:"block_importance"`
	Conflict        []string    `json:"conflict"`
	URLConflict     []string    `json:"url_conflict"`
	UpdatedBy       string      `json:"updated_by,omitempty"`
	UpdatedAt       int64       `json:"updated_at,omitempty"`
	Agents          []NewsAgent `json:"agents"`
}

// NewsAgent is an agent in the settings of the news: its kind, whether it is
// enabled and runs.
type NewsAgent struct {
	Agent   string `json:"agent"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
}

// NewsSettingsRequest is the body of PUT /api/settings/news, the fields set:
// Token replaces the token of the MCP server ("" removes it; absent keeps it),
// URL its address ("" removes it), the window in minutes (0 to 1440) and its
// importance (low, medium or high).
type NewsSettingsRequest struct {
	Enabled         *bool   `json:"enabled,omitempty"`
	Token           *string `json:"token,omitempty"`
	URL             *string `json:"url,omitempty"`
	BlockBefore     *int    `json:"block_before,omitempty"`
	BlockAfter      *int    `json:"block_after,omitempty"`
	BlockImportance *string `json:"block_importance,omitempty"`
}

// NewsTestResponse is the payload of POST /api/settings/news/test: the MCP
// server of the news read once with its token, asked by the user, the news on
// or off, with the agents reading it and its status: ok (it answered),
// missing_server or missing_token (nothing asked), unauthorized (the token
// refused) or unavailable (the server failing, Detail telling how, without
// the token).
type NewsTestResponse struct {
	Results []NewsTestResult `json:"results"`
	At      int64            `json:"at"`
}

// NewsTestResult is an MCP server of a NewsTestResponse.
type NewsTestResult struct {
	URL    string   `json:"url"`
	Agents []string `json:"agents"`
	Status string   `json:"status"`
	Detail string   `json:"detail,omitempty"`
}

// MT5AccountInfo is an account of MetaTrader 5 on the dashboard
// (Configurações > Contas MT5): never its password nor the token of its
// bridge, only whether they are set; its kind, currency and company as the
// bridge confirmed them; the agents linked to it; and how its terminal stands
// (Session), as the controller last read it.
type MT5AccountInfo struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Login       int64             `json:"login"`
	Server      string            `json:"server"`
	BridgeURL   string            `json:"bridge_url"`
	AllowReal   bool              `json:"allow_real"`
	ReadOnly    bool              `json:"read_only"`
	Active      bool              `json:"active"`
	Kind        string            `json:"kind,omitempty"`
	Currency    string            `json:"currency,omitempty"`
	Company     string            `json:"company,omitempty"`
	CheckedAt   int64             `json:"checked_at,omitempty"`
	CheckError  string            `json:"check_error,omitempty"`
	PasswordSet bool              `json:"password_set"`
	TokenSet    bool              `json:"token_set"`
	Agents      []string          `json:"agents"`
	Session     *MT5SessionStatus `json:"session,omitempty"`
	UpdatedBy   string            `json:"updated_by,omitempty"`
	UpdatedAt   int64             `json:"updated_at"`
	// Terminal is the host and the port of its bridge, Instance the one its
	// terminal tells (once read): together, the terminal of the account, many
	// on a host told apart, two URLs of one terminal told the same
	Terminal string `json:"terminal"`
	Instance string `json:"instance,omitempty"`
}

// MT5SessionStatus is how the terminal of an account active stands: its
// bridge reached, the account it is logged in (LoggedLogin) and whether it is
// this one (Current), what it waits for (session, password) and the last
// error, at At (Unix seconds).
type MT5SessionStatus struct {
	Reachable   bool   `json:"reachable"`
	Connected   bool   `json:"connected"`
	LoggedLogin int64  `json:"logged_login,omitempty"`
	Current     bool   `json:"current"`
	Waiting     string `json:"waiting,omitempty"`
	Error       string `json:"error,omitempty"`
	At          int64  `json:"at"`
}

// MT5SwitchPreview answers GET /api/settings/mt5-accounts/{id}/switch: what
// activating the account (Active) or deactivating it does on its terminal,
// as read now. Current names the accounts active on the terminal that leave
// it; From the accounts whose agents follow an activation: the ones of
// Current, or, with none active on the terminal (a switch failed before), its
// inactive accounts holding agents; Linked their agents, moved to the account
// activated when asked (see MT5SwitchRequest); Running the agents running on
// it, stopped first; Exposure the
// positions and the pending orders of its agents on the accounts that leave
// it, closed and canceled once confirmed, eg: "posição de matrend em
// XAUUSDm #123"; Error why it can't be read, nothing switched then.
type MT5SwitchPreview struct {
	Active   bool     `json:"active"`
	Current  []string `json:"current"`
	From     []string `json:"from"`
	Linked   []string `json:"linked"`
	Running  []string `json:"running"`
	Exposure []string `json:"exposure"`
	Error    string   `json:"error,omitempty"`
}

// MT5SwitchRequest is the body of POST .../activate and .../deactivate of an
// account of MT5: ClosePositions confirms closing the positions and canceling
// the pending orders of the agents on the account leaving the terminal (see
// MT5SwitchPreview), refused (409, Confirm) without it when there are some;
// MoveAgents moves the agents of the accounts leaving the terminal (of its
// inactive accounts, with none active on it, see MT5SwitchPreview.From) to the
// account activated, started there once it is logged in.
type MT5SwitchRequest struct {
	ClosePositions bool   `json:"close_positions,omitempty"`
	MoveAgents     bool   `json:"move_agents,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// MT5SwitchRefusal answers a switch of the account of a terminal refused:
// Error why, Confirm when it waits for ClosePositions (see
// MT5SwitchRequest), Pending what remains open on the account.
type MT5SwitchRefusal struct {
	Error   string   `json:"error"`
	Confirm bool     `json:"confirm,omitempty"`
	Pending []string `json:"pending,omitempty"`
}

// MT5AccountsResponse answers GET /api/settings/mt5-accounts, by name: the
// page asked for, see PageInfo.
type MT5AccountsResponse struct {
	PageInfo `tstype:",extends"`
	Accounts []MT5AccountInfo `json:"accounts"`
}

// MT5AccountRequest is the body of POST /api/settings/mt5-accounts (an
// account, its name, login, server and bridge required) and of PATCH
// /api/settings/mt5-accounts/{id} (the fields set): the password and the
// token of the bridge written only ("" removes them, absent keeps them);
// Active deactivates it (see POST .../activate to make it the account of its
// terminal); Reason recorded.
type MT5AccountRequest struct {
	Name        *string `json:"name,omitempty"`
	Login       *int64  `json:"login,omitempty"`
	Server      *string `json:"server,omitempty"`
	Password    *string `json:"password,omitempty"`
	BridgeURL   *string `json:"bridge_url,omitempty"`
	BridgeToken *string `json:"bridge_token,omitempty"`
	AllowReal   *bool   `json:"allow_real,omitempty"`
	ReadOnly    *bool   `json:"read_only,omitempty"`
	Active      *bool   `json:"active,omitempty"`
	Reason      string  `json:"reason,omitempty"`
}

// MT5AccountTest answers POST /api/settings/mt5-accounts/{id}/test: the
// bridge reached and its token taken, the account its terminal is logged in
// and whether it is this one (Current), then what the bridge confirmed of it
// (kind: demo, real or contest; currency, company, server), or why not
// (Error), never a credential; Detail what to do.
type MT5AccountTest struct {
	Reachable   bool   `json:"reachable"`
	TokenOK     bool   `json:"token_ok"`
	Connected   bool   `json:"connected"`
	LoggedLogin int64  `json:"logged_login,omitempty"`
	Current     bool   `json:"current"`
	Kind        string `json:"kind,omitempty"`
	Currency    string `json:"currency,omitempty"`
	Company     string `json:"company,omitempty"`
	Server      string `json:"server,omitempty"`
	Error       string `json:"error,omitempty"`
	Detail      string `json:"detail,omitempty"`
	At          int64  `json:"at"`
}

// MT5AccountEventInfo is an event of an account of MT5, never a secret.
type MT5AccountEventInfo struct {
	At       int64  `json:"at"`
	Kind     string `json:"kind"`
	By       string `json:"by"`
	Reason   string `json:"reason,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Previous any    `json:"previous,omitempty"`
	Next     any    `json:"next,omitempty"`
}

// MT5AccountEventsResponse answers GET /api/settings/mt5-accounts/{id}/events.
type MT5AccountEventsResponse struct {
	Events []MT5AccountEventInfo `json:"events"`
}
