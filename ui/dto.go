package ui

// This file holds every type exposed by the HTTP API. It is the single source
// of truth for the TypeScript types consumed by the web dashboard:
// `make generate` runs tygo on this file and writes web/src/api/types.ts.
//
// All timestamps are Unix seconds (UTC), which is the native time format of
// lightweight-charts.

//go:generate go run github.com/gzuidhof/tygo@v0.2.21 generate

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
	// the one of the core, in the quote. Absent when unknown.
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

	// AccountCurrency is the currency of the account, see WithAccountReporter:
	// the profits of the orders are shown in it (their AccountProfit).
	AccountCurrency string `json:"account_currency,omitempty"`

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
// of the strategy on a pair, see AnalysisSource: its Pair, the decision it
// comes from (DecisionID, its Timeframe, the one of the strategy whatever the
// timeframe the chart shows, the open time of the candle of its context,
// when it was decided, its Action and its Outcome), its Drawings, and its
// Intent, the entry it proposed without placing an order, if any. The orders
// it placed are drawn as any order. An analysis of the model that answered
// replaces the one before, whatever its outcome; one that didn't (a request
// failed, or skipped) leaves it.
type Analysis struct {
	Pair       string    `json:"pair"`
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
// language model on each pair, see Analysis: ChartAnalysis returns it, false
// when there is none yet. Called from the handlers of the chart holding its
// lock, it must be safe for concurrent use, and must not block.
type AnalysisSource interface {
	ChartAnalysis(pair string) (Analysis, bool)
}

// PairsResponse lists the pairs known by the dashboard.
type PairsResponse struct {
	Pairs []string `json:"pairs"`
}

// AgentStatus is an agent of the dashboard, a bot serving its API at Address.
// Online tells whether the API answers. Period is its accounting period, the
// cards of the dashboard counting the trades closed since its start: absent
// before the first reset of its results, all of them counted.
type AgentStatus struct {
	Name    string       `json:"name"`
	Address string       `json:"address"`
	Online  bool         `json:"online"`
	Period  *AgentPeriod `json:"period,omitempty"`
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
// and of the logout.
type SessionResponse struct {
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
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
// orders, their moves and cancels, and the reanalysis, which the trade panel
// or the Telegram controls turn on; and whether the bot may be started and
// stopped there, BotControl, with the controller status ("running" or
// "stopped"): on the web dashboard whatever its trade panel, on Telegram with
// its controls on. It is also the payload of the SSE "controls" event, as the
// web dashboard sees it, sent when the bot is started or stopped.
type ControlsResponse struct {
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`

	// BotControl tells that the bot may be started and stopped on the
	// channel, its Status set.
	BotControl bool `json:"bot_control,omitempty"`

	// Reanalysis tells that the language model of the strategy may be asked
	// again on the channel, see Reanalyzer: the control panel is enabled on
	// it, and the strategy is one of a language model.
	Reanalysis bool `json:"reanalysis,omitempty"`
}

// ControlOrderRequest is the payload of POST /api/controls/order. Amount is
// in quote currency; with Percent it is a percentage of the available balance
// (quote balance for buys, asset balance for sells), and with Lot the size of
// the order, in the units the broker trades the market in, mirroring the
// Telegram /buy and /sell commands.
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
// the pairs of the bot, then their quotes, as the Telegram /balance command.
// Total sums their values.
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
// LLM agent only.
type AgentSettings struct {
	Name            string                `json:"name"`
	Kind            string                `json:"kind"`
	Mode            string                `json:"mode"`
	APIAddress      string                `json:"api_address"`
	Enabled         bool                  `json:"enabled"`
	Launched        bool                  `json:"launched"`
	Restart         bool                  `json:"restart"`
	TradePanel      bool                  `json:"trade_panel"`
	TelegramControl bool                  `json:"telegram_control"`
	Running         bool                  `json:"running"`
	RestartPending  bool                  `json:"restart_pending"`
	Restarts        int                   `json:"restarts,omitempty"`
	Restarting      bool                  `json:"restarting,omitempty"`
	Exited          bool                  `json:"exited,omitempty"`
	LastExit        string                `json:"last_exit,omitempty"`
	LaunchError     string                `json:"launch_error,omitempty"`
	Outbox          *AgentOutbox          `json:"outbox,omitempty"`
	Params          map[string]string     `json:"params"`
	Account         *AgentAccountSettings `json:"account,omitempty"`
	LLM             *AgentLLMSettings     `json:"llm,omitempty"`
	Model           string                `json:"model,omitempty"`
	PromptID        *int64                `json:"prompt_id,omitempty"`
	PromptStatus    *AgentPromptStatus    `json:"prompt_status,omitempty"`
	Secrets         AgentSecretsPresent   `json:"secrets"`
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
// its API, its reasoning effort (minimal, low, medium or high), the tokens of
// an answer at most, and the URL of the MCP server of the economic calendar.
type AgentLLMSettings struct {
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	BaseURL         *string `json:"base_url,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	MaxTokens       *int    `json:"max_tokens,omitempty"`
	CalendarURL     *string `json:"calendar_url,omitempty"`
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

// AgentSecretsPresent tells which secrets of an agent are set.
type AgentSecretsPresent struct {
	LLMAPIKey     bool `json:"llm_api_key"`
	BridgeToken   bool `json:"bridge_token"`
	CalendarToken bool `json:"calendar_token"`
}

// AgentSettingsResponse is the payload of GET /api/settings/agents: every
// agent of the controller, the disabled ones included.
type AgentSettingsResponse struct {
	Agents []AgentSettings `json:"agents"`
}

// AgentSettingsRequest is the payload of PATCH /api/settings/agents/{name}:
// the changes, of the fields set, the name and the kind never changing.
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
	CalendarToken   *string              `json:"calendar_token,omitempty"`
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
// fields set, see AgentLLMSettings: a BaseURL, a ReasoningEffort or a
// CalendarURL of "" and a MaxTokens of 0 remove them, the defaults of the
// agent applying. An LLM agent without a language model gets one, its
// Provider and its Model required then.
type AgentLLMRequest struct {
	Provider        *string `json:"provider,omitempty"`
	Model           *string `json:"model,omitempty"`
	BaseURL         *string `json:"base_url,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	MaxTokens       *int    `json:"max_tokens,omitempty"`
	CalendarURL     *string `json:"calendar_url,omitempty"`
}

// AgentCreateRequest is the payload of POST /api/settings/agents, answered
// with the AgentSettings of the agent created (201). Its Name (letters, digits
// and . _ -, 64 at most) and its Kind (llm, trend, momentum, breakout or
// intraday) never change, and its APIAddress, host:port, is the one of no
// other agent. Launched and Restart are true when absent. Params are the
// flags of its settings that no field holds, see AgentSettings. LLM is the
// language model of an agent of kind llm, required, and PromptID its prompt,
// the one embedded in the agent when absent. The secrets are written, never
// returned. An agent created enabled is launched at once.
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
	Params          map[string]string    `json:"params,omitempty"`
	Account         AgentAccountSettings `json:"account"`
	LLM             *AgentLLMSettings    `json:"llm,omitempty"`
	PromptID        *int64               `json:"prompt_id,omitempty"`
	LLMAPIKey       *string              `json:"llm_api_key,omitempty"`
	BridgeToken     *string              `json:"bridge_token,omitempty"`
	CalendarToken   *string              `json:"calendar_token,omitempty"`
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

// PromptsResponse is the payload of GET /api/prompts, by name.
type PromptsResponse struct {
	Prompts []PromptInfo `json:"prompts"`
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

// TelegramUserRequest is the payload of POST /api/settings/telegram/users.
type TelegramUserRequest struct {
	TelegramID int64  `json:"telegram_id"`
	Name       string `json:"name"`
}

// UserInfo is a user of the dashboard, its times in Unix seconds.
type UserInfo struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Enabled     bool   `json:"enabled"`
	LastLoginAt *int64 `json:"last_login_at,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

// UsersResponse is the payload of GET /api/users, by username.
type UsersResponse struct {
	Users []UserInfo `json:"users"`
}

// UserRequest is the payload of POST /api/users (Username and Password) and
// of PATCH /api/users/{id}, of the fields set.
type UserRequest struct {
	Username string  `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
}
