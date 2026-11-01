package metalabel

import (
	"bytes"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/model"
)

// frame returns n candles of 15m from a Monday, a wave with a drift.
func frame(n int) *model.Dataframe {
	df := &model.Dataframe{Pair: "XAUUSDm", Metadata: map[string]model.Series[float64]{}}
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	for i := range n {
		p := 2000 + 0.05*float64(i) + 15*math.Sin(float64(i)/17) + 4*math.Sin(float64(i)/3)
		df.Time = append(df.Time, start.Add(time.Duration(i)*15*time.Minute))
		df.Open = append(df.Open, p-0.3)
		df.Close = append(df.Close, p)
		df.High = append(df.High, p+1.2+0.4*math.Sin(float64(i)))
		df.Low = append(df.Low, p-1.1)
		df.Volume = append(df.Volume, 100+20*math.Sin(float64(i)/5))
	}
	return df
}

// tail returns the last n candles of df.
func tail(df *model.Dataframe, n int) *model.Dataframe {
	k := len(df.Close) - n
	return &model.Dataframe{Pair: df.Pair, Time: df.Time[k:], Open: df.Open[k:], Close: df.Close[k:],
		High: df.High[k:], Low: df.Low[k:], Volume: df.Volume[k:], Metadata: map[string]model.Series[float64]{}}
}

// The features are fixed and sorted, computed from the last Window candles
// only: the same on a longer history, none on a shorter one or for no side;
// the side ones mirrored for a sell.
func TestFeatures(t *testing.T) {
	names := FeatureNames()
	require.True(t, slices.IsSorted(names))

	df := frame(400)
	buy, ok := Features(df, model.SideTypeBuy)
	require.True(t, ok)
	keys := make([]string, 0, len(buy))
	for key := range buy {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	assert.Equal(t, names, keys, "exactly the features named")

	again, ok := Features(tail(df, Window), model.SideTypeBuy)
	require.True(t, ok)
	assert.Equal(t, buy, again, "the last Window candles only")

	sell, ok := Features(df, model.SideTypeSell)
	require.True(t, ok)
	assert.Equal(t, -1.0, sell["side"])
	assert.Equal(t, buy["ema50_dist_atr"], sell["ema50_dist_atr"])
	assert.Equal(t, -buy["ema50_dist_atr_side"], sell["ema50_dist_atr_side"])
	assert.InDelta(t, -buy["rsi14_side"], sell["rsi14_side"], 1e-12)
	assert.InDelta(t, 100*1.0, 100*buy["side"], 0)
	for name, value := range buy {
		assert.False(t, math.IsNaN(value) || math.IsInf(value, 0), name)
	}
	assert.GreaterOrEqual(t, buy["bb_width_pct"], 0.0)
	assert.LessOrEqual(t, buy["bb_width_pct"], 100.0)
	assert.InDelta(t, 1, buy["hour_sin"]*buy["hour_sin"]+buy["hour_cos"]*buy["hour_cos"], 1e-12)

	_, ok = Features(tail(df, Window-1), model.SideTypeBuy)
	assert.False(t, ok, "fewer candles than the window")
	_, ok = Features(df, "")
	assert.False(t, ok, "no side")

	novolume := tail(df, Window)
	novolume.Volume = make(model.Series[float64], Window)
	features, ok := Features(novolume, model.SideTypeBuy)
	require.True(t, ok)
	assert.Equal(t, 1.0, features["vol_ratio"], "no volume")

	// the last candle changed: only from it on, never an earlier feature
	moved := tail(df, Window)
	moved.Close = slices.Clone(moved.Close)
	moved.Close[Window-1] += 10
	changed, ok := Features(moved, model.SideTypeBuy)
	require.True(t, ok)
	assert.NotEqual(t, buy["ret10_atr"], changed["ret10_atr"])
}

// samples returns n samples of the tests, a day apart, labeled 1 mostly when
// er10 is high and the ret10_atr_side positive, with noise.
func samples(n int, seed uint64) []Sample {
	rng := rand.New(rand.NewPCG(seed, 7))
	list := make([]Sample, n)
	for i := range list {
		features := make(map[string]float64)
		for _, name := range FeatureNames() {
			features[name] = rng.NormFloat64()
		}
		score := 3*features["er10"] + 2*features["ret10_atr_side"] + 0.5*rng.NormFloat64()
		label := 0
		if score > 0 {
			label = 1
		}
		at := int64(1_700_000_000 + i*86400)
		list[i] = Sample{Agent: "a", Strategy: "s", Pair: "XAUUSDm", Side: "BUY", Time: at, Features: features,
			Label: label, ExitTime: at + 3600}
	}
	return list
}

// Train separates a set the features tell apart: a high area under the curve
// on the samples held out, the weights of the features telling it the
// largest; the same samples, the same model.
func TestTrain_SeparatesASeparableSet(t *testing.T) {
	set := samples(600, 1)
	m, report, err := Train(set, TrainOptions{})
	require.NoError(t, err)
	assert.True(t, report.Converged)
	assert.Equal(t, 600, report.Samples)
	assert.Equal(t, 420, report.Train)
	assert.Equal(t, 180, report.Test)
	assert.Greater(t, report.TestAUC, 0.95)
	assert.Greater(t, report.TrainAUC, 0.95)
	assert.Equal(t, report.TestAUC, m.TestAUC)
	assert.Len(t, report.Thresholds, len(ReportThresholds))
	for i := 1; i < len(report.Thresholds); i++ {
		assert.LessOrEqual(t, report.Thresholds[i].Taken, report.Thresholds[i-1].Taken, "fewer taken higher")
	}
	weight := func(name string) float64 { return m.Weights[slices.Index(m.Features, name)] }
	assert.Greater(t, weight("er10"), weight("ret10_atr_side"))
	assert.Greater(t, weight("ret10_atr_side"), math.Abs(weight("adx14")))
	assert.Equal(t, set[419].Time, m.TrainedAt, "the newest sample trained on")
	assert.Equal(t, set[419].ExitTime, m.TrainedThrough, "the newest outcome learned")
	assert.Equal(t, DefaultThreshold, m.Threshold)

	shuffled := slices.Clone(set)
	rand.New(rand.NewPCG(3, 3)).Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	again, againReport, err := Train(shuffled, TrainOptions{})
	require.NoError(t, err)
	assert.Equal(t, m, again, "ordered by time whatever the order read: deterministic")
	assert.Equal(t, report, againReport)
}

// The training samples whose outcome is known only after the first sample of
// the test are purged; the skipped ones and the errors told.
func TestTrain_PurgedSkippedAndRefused(t *testing.T) {
	set := samples(100, 2)
	set[69].ExitTime = set[70].Time // its outcome known as the test starts
	set[68].ExitTime = set[99].Time
	delete(set[0].Features, "er10")
	set[60].ExitTime = set[70].Time - 1 // an older sample of the latest outcome trained on, just kept
	m, report, err := Train(set, TrainOptions{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Skipped)
	assert.Equal(t, 2, report.Purged)
	assert.Equal(t, set[70].Time-1, m.TrainedThrough, "the latest exit of the samples trained on, the purged left "+
		"out")

	_, _, err = Train(samples(20, 3), TrainOptions{})
	assert.ErrorContains(t, err, "samples to train on")
	same := samples(100, 4)
	for i := range same {
		same[i].Label = 1
	}
	_, _, err = Train(same, TrainOptions{})
	assert.ErrorContains(t, err, "single label")
	for _, opts := range []TrainOptions{{TrainShare: 1}, {L2: -1}, {Threshold: 1}} {
		_, _, err = Train(samples(100, 5), opts)
		assert.Error(t, err, opts)
	}
}

// A model saved is read back the same; one that can't be used is refused.
func TestModel_SaveLoadAndProbability(t *testing.T) {
	m := &Model{Version: ModelVersion, Features: []string{"a", "b"}, Mean: []float64{1, 0}, Std: []float64{2, 1},
		Weights: []float64{1, -0.5}, Bias: 0.25, Threshold: 0.55, TrainedAt: 1, TrainedThrough: 2, Samples: 30,
		Positives: 12}
	p, err := m.Probability(map[string]float64{"a": 3, "b": 1, "c": 9})
	require.NoError(t, err)
	assert.InDelta(t, 1/(1+math.Exp(-(0.25+1*(3-1)/2.0-0.5*1))), p, 1e-12)
	_, err = m.Probability(map[string]float64{"a": 3})
	assert.ErrorContains(t, err, "the feature b is missing")

	path := filepath.Join(t.TempDir(), "models", "meta.json")
	require.NoError(t, m.Save(path))
	read, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, m, read)

	for name, change := range map[string]func(*Model){
		"version":   func(m *Model) { m.Version = 2 },
		"lengths":   func(m *Model) { m.Std = m.Std[:1] },
		"deviation": func(m *Model) { m.Std[1] = 0 },
		"threshold": func(m *Model) { m.Threshold = 1 },
		"twice":     func(m *Model) { m.Features[1] = "a" },
		"none":      func(m *Model) { m.Features, m.Mean, m.Std, m.Weights = nil, nil, nil, nil },
	} {
		bad := *m
		bad.Features, bad.Mean = slices.Clone(m.Features), slices.Clone(m.Mean)
		bad.Std, bad.Weights = slices.Clone(m.Std), slices.Clone(m.Weights)
		change(&bad)
		assert.Error(t, bad.Validate(), name)
		file := filepath.Join(t.TempDir(), name+".json")
		require.NoError(t, os.WriteFile(file, []byte(`{"version": 2}`), 0o600))
		_, err := Load(file)
		assert.Error(t, err, name)
	}
	_, err = Load(filepath.Join(t.TempDir(), "missing.json"))
	assert.Error(t, err)
}

// The samples are JSON lines, snake_case, read back the same.
func TestSamples_JSONLines(t *testing.T) {
	s := Sample{Agent: "gold", Strategy: "meanreversion", Source: SourceBacktest, Run: 12, Segment: SegmentFit,
		Pair: "XAUUSDm", Side: "BUY", Time: 10, Entry: 2000, Stop: 1990, Target: 2020,
		Features: map[string]float64{"side": 1}, Label: 1, R: 2, Outcome: OutcomeTarget, ExitTime: 20}
	line, err := s.MarshalLine()
	require.NoError(t, err)
	assert.Contains(t, string(line), `"exit_time":20`)
	assert.Contains(t, string(line), `"source":"backtest","run":12,"segment":"fit"`)
	assert.Equal(t, byte('\n'), line[len(line)-1])
	read, err := ReadSamples(bytes.NewReader(append(append(line, '\n'), line...)))
	require.NoError(t, err)
	assert.Equal(t, []Sample{s, s}, read)
	_, err = ReadSamples(bytes.NewReader([]byte("{\n")))
	assert.ErrorContains(t, err, "line 1")
}
