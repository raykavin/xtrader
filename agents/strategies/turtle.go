package strategies

import (
	"github.com/raykavin/xtrader"
	"github.com/raykavin/xtrader/indicator"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/strategy"
	"github.com/raykavin/xtrader/tools/log"
)

// Turtle buys half the balance on a close at the highest of 40 candles and
// sells the position on one at the lowest of 20, see
// https://www.investopedia.com/articles/trading/08/turtle-trading.asp
//
// Deprecated: registered in no agent (breakoutagent trades the channel
// breakouts with the Signals, eg: donchianatr). Kept until its removal is
// decided.
type Turtle struct{}

func (e Turtle) Timeframe() string {
	return "4h"
}

func (e Turtle) WarmupPeriod() int {
	return 40
}

func (e Turtle) Indicators(df *xtrader.Dataframe) []strategy.ChartIndicator {
	df.Metadata["max40"] = indicator.Max(df.Close, 40)
	df.Metadata["low20"] = indicator.Min(df.Close, 20)

	return nil
}

func (e *Turtle) OnCandle(df *xtrader.Dataframe, broker service.Broker) {
	closePrice := df.Close.Last(0)
	highest := df.Metadata["max40"].Last(0)
	lowest := df.Metadata["low20"].Last(0)

	assetPosition, quotePosition, err := broker.Position(df.Pair)
	if err != nil {
		log.Error(err)
		return
	}

	// If position already open wait till it will be closed
	if assetPosition == 0 && closePrice >= highest {
		_, err := broker.CreateOrderMarketQuote(xtrader.SideTypeBuy, df.Pair, quotePosition/2)
		if err != nil {
			log.Error(err)
		}
		return
	}

	if assetPosition > 0 && closePrice <= lowest {
		_, err := broker.CreateOrderMarket(xtrader.SideTypeSell, df.Pair, assetPosition)
		if err != nil {
			log.Error(err)
		}
	}
}
