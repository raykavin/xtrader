package strategies

import (
	"errors"
	"fmt"
	"time"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
)

// RSI2Config holds the inputs of RSI2, with its defaults in DefaultRSI2Config:
// the 2-period RSI strategy of Larry Connors and Cesar Alvarez, "Short Term
// Trading Strategies That Work" (TradingMarkets, 2008), its chapter "The
// 2-Period RSI: The Trader's Holy Grail of Indicators?", on the daily bars of
// the book. Periods are counted in candles of Timeframe.
//
// The rule, a long:
//
//   - the trend: the close above the simple moving average of TrendLen closes,
//     the 200-day SMA of the book, which buys only the pullbacks of a market
//     going up;
//   - the entry: the RSI of RSILen closes (2, Wilder's, the classic RSI on a
//     very short period) under Oversold, bought at the market on the close of
//     that candle;
//   - the exit: a close above the simple moving average of ExitLen closes, the
//     5-day SMA of the book, sold at the market on that close.
//
// A short is the mirror, as Connors tests both sides: the close under the SMA
// of TrendLen, the RSI over Overbought, covered on a close under the SMA of
// ExitLen. Every comparison is strict, on the candle that just closed and the
// ones before it only.
//
// Connors' tests place no stop loss (the book finds that stops hurt the
// results of these short-term systems): here the stop loss and the take
// profit of the bracket, see BracketConfig, are a safety, each entry placed
// with them at the broker, the exit at the SMA closing the position unless
// the broker reaches one of them first.
type RSI2Config struct {
	Timeframe string

	// The trend filter: the SMA of TrendLen closes.
	TrendLen int

	// The entry: the RSI of RSILen closes under Oversold for a long, over
	// Overbought for a short.
	RSILen     int
	Oversold   float64
	Overbought float64

	// The exit: the SMA of ExitLen closes, the position closed at the market
	// on a close beyond it from its side.
	ExitLen int

	// TrailingATR, above 0, trails the stop loss at the broker that many ATR
	// of ATRLen candles behind the best price since the entry, as
	// SignalConfig.TrailingATR does: not part of the rule, taken as by every
	// strategy of the momentum agent. 0, the default, leaves it where the
	// entry placed it.
	TrailingATR float64

	// The safety exits and the sizing, see BracketConfig: the stop loss
	// StopATR and the take profit TakeProfitATR away from the entry close, in
	// ATR of ATRLen candles, the defaults of DefaultBracketConfig, not of the
	// book. TakeProfitATR 0 places no take profit, as SignalConfig takes it:
	// the exit at the SMA alone, closer to the source.
	BracketConfig
}

// DefaultRSI2Config returns the defaults of RSI2, on the 1d timeframe of the
// source: the SMA 200 trend, the RSI 2 under 10 or over 90, the exit at the
// SMA 5, and the stop loss and take profit of DefaultBracketConfig as a
// safety (1 and 1.5 ATR of 14 candles, not from the source, see
// RSI2Config).
func DefaultRSI2Config() RSI2Config {
	return RSI2Config{
		Timeframe: "1d",

		TrendLen: 200,

		RSILen:     2,
		Oversold:   10,
		Overbought: 90,

		ExitLen: 5,

		BracketConfig: DefaultBracketConfig(),
	}
}

func (c RSI2Config) validate() error {
	exits := c.BracketConfig
	if exits.TakeProfitATR == 0 {
		exits.TakeProfitATR = 1 // none, the rest of the exits checked as for the other strategies
	}
	if err := exits.validate(); err != nil {
		return err
	}

	switch {
	case c.Timeframe == "":
		return errors.New("timeframe is required")
	case c.RSILen < 1:
		return errors.New("the RSI period must be at least 1")
	case c.TrendLen < 2 || c.ExitLen < 2:
		return errors.New("the periods of the SMAs of the trend and of the exit must be at least 2: no close is " +
			"ever beyond the SMA of 1 close, itself")
	case c.Oversold <= 0 || c.Overbought >= 100 || c.Oversold >= c.Overbought:
		return errors.New("RSI levels: the oversold level must be above 0 and below the overbought one, below 100")
	case c.TrailingATR < 0:
		return errors.New("trailing ATR multiple can't be negative")
	}
	_, _, err := timeframeCandles(c.Timeframe, time.Hour)
	return err
}

// RSI2 is the 2-period RSI strategy of Connors and Alvarez: a BUY on the RSI 2
// oversold while the close is above the SMA 200, closed on a close above the
// SMA 5; a SELL on the mirror. One position at a time per pair, placed at the
// market on the close of the candle of the signal, with the stop loss and the
// take profit of the bracket at the broker as a safety. See RSI2Config.
type RSI2 struct {
	cfg     RSI2Config
	warmup  int
	bracket *bracket
	trails  map[string]*signalTrail // the trailing of the positions, by pair, see RSI2Config.TrailingATR
}

// rsi2Margin is the candles of the warmup of RSI2 beyond the closes of its
// longer SMA (not from the source): a few, so the SMA of the trend isn't known
// on the last candle of the window only, eg: on the chart.
const rsi2Margin = 10

// NewRSI2 validates the configuration and creates the strategy.
func NewRSI2(cfg RSI2Config) (*RSI2, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("rsi2: %w", err)
	}

	// the SMAs need their closes, and a margin, the recursive series (the RSI
	// and the ATR) their seed washed out: overlapped rather than summed, the
	// decision reading the last candle only, whose RSI and ATR the window of
	// the SMA of the trend settles too when longer than their seed (200 closes
	// against the 154 of the ATR 14 by default); 210 daily candles then, which
	// the warm-up of a backtest of the controller, a year at most, holds on
	// gold too (about 250 daily candles a year), where the sum (354) didn't
	return &RSI2{
		cfg:     cfg,
		warmup:  max(max(cfg.TrendLen, cfg.ExitLen)+rsi2Margin, seedWarmup(max(cfg.RSILen, cfg.ATRLen))),
		bracket: newBracket("RSI2", cfg.Timeframe, cfg.BracketConfig),
		trails:  make(map[string]*signalTrail),
	}, nil
}

func (s *RSI2) Timeframe() string {
	return s.cfg.Timeframe
}

func (s *RSI2) WarmupPeriod() int {
	return s.warmup
}

// Indicators adds the SMAs of the trend and of the exit, the RSI and the ATR
// of the bracket to df, each value computed from its candle and the ones
// before it only.
func (s *RSI2) Indicators(df *model.Dataframe) []strategy.ChartIndicator {
	trend, exit := pineSMA(df.Close, s.cfg.TrendLen), pineSMA(df.Close, s.cfg.ExitLen)
	rsi := pineRSI(df.Close, s.cfg.RSILen)
	df.Metadata["r2_trend"], df.Metadata["r2_exit"], df.Metadata["r2_rsi"] = trend, exit, rsi
	df.Metadata["r2_atr"] = pineATR(df.High, df.Low, df.Close, s.cfg.ATRLen)

	return append(append(
		overlay(df, fmt.Sprintf("SMA %d", s.cfg.TrendLen), s.cfg.TrendLen,
			line(trend, fmt.Sprintf("SMA %d", s.cfg.TrendLen), colorSlow)),
		overlay(df, fmt.Sprintf("SMA %d", s.cfg.ExitLen), s.cfg.ExitLen,
			line(exit, fmt.Sprintf("SMA %d", s.cfg.ExitLen), colorLine))...),
		pane(df, fmt.Sprintf("RSI %d", s.cfg.RSILen), s.cfg.RSILen,
			line(rsi, fmt.Sprintf("RSI %d", s.cfg.RSILen), colorOther)))
}

// signal returns the entry of the last candle, if any, see RSI2Config: none
// while an indicator is missing, a comparison with NaN being false.
func (s *RSI2) signal(df *model.Dataframe) model.SideType {
	m, ok := meta(df, "r2_trend", "r2_rsi", "r2_atr")
	i := len(df.Close) - 1
	if !ok || i < 0 {
		return ""
	}
	closePrice, trend, rsi := df.Close[i], m["r2_trend"][i], m["r2_rsi"][i]
	switch {
	case closePrice > trend && rsi < s.cfg.Oversold:
		return model.SideTypeBuy
	case closePrice < trend && rsi > s.cfg.Overbought:
		return model.SideTypeSell
	}
	return ""
}

// exit closes the position on a close beyond the SMA of the exit from its
// side: above it for a long, under it for a short.
func (s *RSI2) exit(df *model.Dataframe, held model.SideType) string {
	sma := df.Metadata["r2_exit"]
	i := len(df.Close) - 1
	if i < 0 || len(sma) != len(df.Close) {
		return ""
	}
	if rkSign(held)*(df.Close[i]-sma[i]) > 0 {
		return "SMA"
	}
	return ""
}

func (s *RSI2) OnCandle(df *model.Dataframe, broker service.Broker) {
	entry := s.signal(df)
	if !s.bracket.manage(df, broker, s.exit) {
		s.bracket.trail(df, broker, s.trails, s.cfg.TrailingATR, df.Metadata["r2_atr"])
		return
	}
	if entry != "" && s.bracket.open(df, broker, entry, df.Metadata["r2_atr"].Last(0)) {
		s.trails[df.Pair] = &signalTrail{trade: s.bracket.trades[df.Pair], extreme: df.Close.Last(0)}
	}
}
