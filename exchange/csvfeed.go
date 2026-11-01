package exchange

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/samber/lo"
	"github.com/xhit/go-str2duration/v2"

	"github.com/raykavin/xtrader/model"
)

var ErrInsufficientData = errors.New("insufficient data")

type PairFeed struct {
	Pair       string
	File       string
	Timeframe  string
	HeikinAshi bool
}

type CSVFeed struct {
	Feeds               map[string]PairFeed
	CandlePairTimeFrame map[string][]model.Candle
}

func (c CSVFeed) AssetsInfo(pair string) model.AssetInfo {
	asset, quote := SplitAssetQuote(pair)
	return model.AssetInfo{
		BaseAsset:          asset,
		QuoteAsset:         quote,
		MaxPrice:           math.MaxFloat64,
		MaxQuantity:        math.MaxFloat64,
		StepSize:           0.00000001,
		TickSize:           0.00000001,
		QuotePrecision:     8,
		BaseAssetPrecision: 8,
	}
}

func parseHeaders(headers []string) (index map[string]int, additional []string, ok bool) {
	headerMap := map[string]int{
		"time": 0, "open": 1, "close": 2, "low": 3, "high": 4, "volume": 5,
	}

	_, err := strconv.Atoi(headers[0])
	if err == nil {
		return headerMap, additional, false
	}

	for index, h := range headers {
		if _, ok := headerMap[h]; !ok {
			additional = append(additional, h)
		}
		headerMap[h] = index
	}

	return headerMap, additional, true
}

// NewCSVFeed creates a new data feed from CSV files and resample
func NewCSVFeed(targetTimeframe string, feeds ...PairFeed) (*CSVFeed, error) {
	csvFeed := &CSVFeed{
		Feeds:               make(map[string]PairFeed),
		CandlePairTimeFrame: make(map[string][]model.Candle),
	}

	for _, feed := range feeds {
		csvFeed.Feeds[feed.Pair] = feed

		csvFile, err := os.Open(feed.File)
		if err != nil {
			return nil, err
		}

		csvLines, err := csv.NewReader(csvFile).ReadAll()
		if err != nil {
			return nil, err
		}

		var candles []model.Candle
		ha := model.NewHeikinAshi()

		// map each header label with its index
		headerMap, additionalHeaders, hasCustomHeaders := parseHeaders(csvLines[0])
		if hasCustomHeaders {
			csvLines = csvLines[1:]
		}

		for _, line := range csvLines {
			timestamp, err := strconv.Atoi(line[headerMap["time"]])
			if err != nil {
				return nil, err
			}

			candle := model.Candle{
				Time:      time.Unix(int64(timestamp), 0).UTC(),
				UpdatedAt: time.Unix(int64(timestamp), 0).UTC(),
				Pair:      feed.Pair,
				Complete:  true,
			}

			candle.Open, err = strconv.ParseFloat(line[headerMap["open"]], 64)
			if err != nil {
				return nil, err
			}

			candle.Close, err = strconv.ParseFloat(line[headerMap["close"]], 64)
			if err != nil {
				return nil, err
			}

			candle.Low, err = strconv.ParseFloat(line[headerMap["low"]], 64)
			if err != nil {
				return nil, err
			}

			candle.High, err = strconv.ParseFloat(line[headerMap["high"]], 64)
			if err != nil {
				return nil, err
			}

			candle.Volume, err = strconv.ParseFloat(line[headerMap["volume"]], 64)
			if err != nil {
				return nil, err
			}

			if hasCustomHeaders {
				candle.Metadata = make(map[string]float64)
				for _, header := range additionalHeaders {
					candle.Metadata[header], err = strconv.ParseFloat(line[headerMap[header]], 64)
					if err != nil {
						return nil, err
					}
				}
			}

			if feed.HeikinAshi {
				candle = candle.ToHeikinAshi(ha)
			}

			candles = append(candles, candle)
		}

		csvFeed.CandlePairTimeFrame[csvFeed.feedTimeframeKey(feed.Pair, feed.Timeframe)] = candles

		err = csvFeed.resample(feed.Pair, feed.Timeframe, targetTimeframe)
		if err != nil {
			return nil, err
		}
	}

	return csvFeed, nil
}

func (c CSVFeed) feedTimeframeKey(pair, timeframe string) string {
	return fmt.Sprintf("%s--%s", pair, timeframe)
}

func (c CSVFeed) LastQuote(_ context.Context, _ string) (float64, error) {
	return 0, errors.New("invalid operation")
}

func (c *CSVFeed) Limit(duration time.Duration) *CSVFeed {
	for pair, candles := range c.CandlePairTimeFrame {
		start := candles[len(candles)-1].Time.Add(-duration)
		c.CandlePairTimeFrame[pair] = lo.Filter(candles, func(candle model.Candle, _ int) bool {
			return candle.Time.After(start)
		})
	}
	return c
}

func isLastCandlePeriod(t time.Time, fromTimeframe, targetTimeframe string) (bool, error) {
	if fromTimeframe == targetTimeframe {
		return true, nil
	}

	fromDuration, err := str2duration.ParseDuration(fromTimeframe)
	if err != nil {
		return false, err
	}

	next := t.Add(fromDuration).UTC()

	switch targetTimeframe {
	case "1m":
		return next.Second()%60 == 0, nil
	case "5m":
		return next.Minute()%5 == 0, nil
	case "10m":
		return next.Minute()%10 == 0, nil
	case "15m":
		return next.Minute()%15 == 0, nil
	case "30m":
		return next.Minute()%30 == 0, nil
	case "1h":
		return next.Minute()%60 == 0, nil
	case "2h":
		return next.Minute() == 0 && next.Hour()%2 == 0, nil
	case "4h":
		return next.Minute() == 0 && next.Hour()%4 == 0, nil
	case "12h":
		return next.Minute() == 0 && next.Hour()%12 == 0, nil
	case "1d":
		return next.Minute() == 0 && next.Hour()%24 == 0, nil
	case "1w":
		return next.Minute() == 0 && next.Hour()%24 == 0 && next.Weekday() == time.Sunday, nil
	}

	return false, fmt.Errorf("invalid timeframe: %s", targetTimeframe)
}

// periodStart returns the open time of the candle of timeframe holding a
// candle opened at t, in UTC: the start of its minutes or hours, of its day,
// or of its week from Sunday (as isLastCandlePeriod ends the weeks on
// Saturday).
func periodStart(t time.Time, timeframe string) (time.Time, error) {
	t = t.UTC()
	switch timeframe {
	case "1d":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	case "1w":
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return day.AddDate(0, 0, -int(day.Weekday())), nil
	}
	length, err := str2duration.ParseDuration(timeframe)
	if err != nil {
		return time.Time{}, err
	}
	if length <= 0 {
		return time.Time{}, fmt.Errorf("invalid timeframe: %s", timeframe)
	}
	return t.Truncate(length), nil
}

// periodLength returns the length of a candle of timeframe.
func periodLength(timeframe string) (time.Duration, error) {
	switch timeframe {
	case "1d":
		return 24 * time.Hour, nil
	case "1w":
		return 7 * 24 * time.Hour, nil
	}
	return str2duration.ParseDuration(timeframe)
}

// resample builds the candles of targetTimeframe of pair from the ones of
// sourceTimeframe: each source candle goes to the target candle of its open
// time (see periodStart), the target candle growing one source candle at a
// time, every step kept (Complete false, the partial candles of the
// backtests) until its last source candle (see isLastCandlePeriod), Complete
// then. A target candle missing its last source candles (a gap in the data:
// a minute the broker didn't send, the market closed before the end of a
// day) completes with what it had once a later candle begins, as of its last
// source candle (its UpdatedAt the time that one would have had): it is never
// merged with the candles of the next period, whose prices would come into it
// under its own time, a look into the future. A target candle missing its
// first source candles keeps the open time of its period. The source candles
// before the first period beginning in them, and the last target candle if
// not complete, are left out.
func (c *CSVFeed) resample(pair, sourceTimeframe, targetTimeframe string) error {
	sourceKey := c.feedTimeframeKey(pair, sourceTimeframe)
	targetKey := c.feedTimeframeKey(pair, targetTimeframe)
	source := c.CandlePairTimeFrame[sourceKey]

	if _, err := isLastCandlePeriod(time.Time{}, sourceTimeframe, targetTimeframe); err != nil {
		return err
	}
	sourceLength, err := periodLength(sourceTimeframe)
	if err != nil {
		return err
	}
	targetLength, err := periodLength(targetTimeframe)
	if err != nil {
		return err
	}
	if sourceTimeframe == targetTimeframe {
		candles := make([]model.Candle, len(source))
		for i, candle := range source {
			candle.Complete = true
			candles[i] = candle
		}
		c.CandlePairTimeFrame[targetKey] = candles
		return nil
	}

	var i int
	for ; i < len(source); i++ {
		start, err := periodStart(source[i].Time, targetTimeframe)
		if err != nil {
			return err
		}
		if start.Equal(source[i].Time.UTC()) {
			break
		}
	}

	candles := make([]model.Candle, 0)
	var start time.Time // of the target candle growing
	for ; i < len(source); i++ {
		candle := source[i]
		period, err := periodStart(candle.Time, targetTimeframe)
		if err != nil {
			return err
		}
		last, err := isLastCandlePeriod(candle.Time, sourceTimeframe, targetTimeframe)
		if err != nil {
			return err
		}

		lastIndex := len(candles) - 1
		if lastIndex >= 0 && !candles[lastIndex].Complete && !period.Equal(start) {
			// its period over without its last source candle: complete as it was
			candles[lastIndex].Complete = true
			candles[lastIndex].UpdatedAt = start.Add(targetLength - sourceLength)
		}
		if lastIndex >= 0 && !candles[lastIndex].Complete {
			candle.Time = candles[lastIndex].Time
			candle.Open = candles[lastIndex].Open
			candle.High = math.Max(candles[lastIndex].High, candle.High)
			candle.Low = math.Min(candles[lastIndex].Low, candle.Low)
			candle.Volume += candles[lastIndex].Volume
		} else {
			start = period
			candle.Time = period
		}
		candle.Complete = last
		candles = append(candles, candle)
	}

	// remove last candle if not complete
	if len(candles) > 0 && !candles[len(candles)-1].Complete {
		candles = candles[:len(candles)-1]
	}

	c.CandlePairTimeFrame[targetKey] = candles

	return nil
}

func (c CSVFeed) CandlesByPeriod(_ context.Context, pair, timeframe string,
	start, end time.Time) ([]model.Candle, error) {

	key := c.feedTimeframeKey(pair, timeframe)
	candles := make([]model.Candle, 0)
	for _, candle := range c.CandlePairTimeFrame[key] {
		if candle.Time.Before(start) || candle.Time.After(end) {
			continue
		}
		candles = append(candles, candle)
	}
	return candles, nil
}

func (c *CSVFeed) CandlesByLimit(_ context.Context, pair, timeframe string, limit int) ([]model.Candle, error) {
	var result []model.Candle
	key := c.feedTimeframeKey(pair, timeframe)
	if len(c.CandlePairTimeFrame[key]) < limit {
		return nil, fmt.Errorf("%w: %s", ErrInsufficientData, pair)
	}
	result, c.CandlePairTimeFrame[key] = c.CandlePairTimeFrame[key][:limit], c.CandlePairTimeFrame[key][limit:]
	return result, nil
}

func (c CSVFeed) CandlesSubscription(_ context.Context, pair, timeframe string) (chan model.Candle, chan error) {
	ccandle := make(chan model.Candle)
	cerr := make(chan error)
	key := c.feedTimeframeKey(pair, timeframe)
	go func() {
		for _, candle := range c.CandlePairTimeFrame[key] {
			ccandle <- candle
		}
		close(ccandle)
		close(cerr)
	}()
	return ccandle, cerr
}
