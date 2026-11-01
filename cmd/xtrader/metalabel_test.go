package main

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/raykavin/xtrader/agents/metalabel"
)

// writeSamples writes n samples of agent to a JSONL file, labeled by the sign
// of one feature (and some noise), every feature set.
func writeSamples(t *testing.T, file, agent string, n int, rng *rand.Rand) {
	t.Helper()
	writeSamplesOf(t, file, metalabel.Sample{Agent: agent}, 0, n, rng)
}

// writeSamplesOf writes the samples first to first+n-1 of the agent, source,
// run and segment of like to a JSONL file, labeled as writeSamples does, each
// at its own hour.
func writeSamplesOf(t *testing.T, file string, like metalabel.Sample, first, n int, rng *rand.Rand) {
	t.Helper()
	var b bytes.Buffer
	for i := first; i < first+n; i++ {
		features := make(map[string]float64)
		for _, name := range metalabel.FeatureNames() {
			features[name] = rng.NormFloat64()
		}
		label := 0
		if features["rsi14_side"]+0.3*rng.NormFloat64() > 0 {
			label = 1
		}
		line, err := metalabel.Sample{Agent: like.Agent, Strategy: "matrend", Source: like.Source, Run: like.Run,
			Segment: like.Segment, Pair: "XAUUSDm", Side: "BUY", Time: int64(1_760_000_000 + i*3600), Entry: 100,
			Stop: 99, Target: 102, Features: features, Label: label, Outcome: metalabel.OutcomeTarget,
			ExitTime: int64(1_760_000_000 + i*3600 + 1800)}.MarshalLine()
		require.NoError(t, err)
		b.Write(line)
	}
	require.NoError(t, os.WriteFile(file, b.Bytes(), 0o600))
}

// The model is trained from the samples of the agent asked for, reported, and
// written where -meta-model reads it.
func TestMetalabelCommand(t *testing.T) {
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(3, 3))
	gold, other := filepath.Join(dir, "gold.jsonl"), filepath.Join(dir, "other.jsonl")
	writeSamples(t, gold, "gold", 400, rng)
	writeSamples(t, other, "other", 50, rng)
	out := filepath.Join(dir, "meta", "gold.json")

	var stdout, stderr bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{metalabelCommand}, Writer: &stdout, ErrWriter: &stderr}
	require.NoError(t, app.Run([]string{"xtrader", "metalabel", "--samples", gold, "--samples", other,
		"--agent", "gold", "--out", out}))
	assert.Contains(t, stdout.String(), "samples 400")
	assert.Contains(t, stdout.String(), "samples read 450, 0 duplicates dropped, 400 kept by the filters: none 400")
	assert.Contains(t, stdout.String(), "threshold")
	assert.Contains(t, stderr.String(), "-meta-model="+out)

	model, err := metalabel.Load(out)
	require.NoError(t, err)
	assert.Equal(t, 280, model.Samples, "the oldest 70% trained on")
	assert.Greater(t, model.TestAUC, 0.85, "the separable feature learned")
	assert.Equal(t, int64(1_760_000_000+279*3600+1800), model.TrainedThrough, "the exit of the newest trained on")

	// no sample of the agent: refused, nothing written
	require.Error(t, app.Run([]string{"xtrader", "metalabel", "--samples", gold, "--agent", "nobody"}))

	stdout.Reset()
	stderr.Reset()
	require.NoError(t, app.Run([]string{"xtrader", "metalabel", "--samples", gold, "--json"}))
	assert.Contains(t, stdout.String(), `"test_auc"`)
	assert.NotContains(t, stdout.String(), "samples read", "the JSON alone on the standard output")
	assert.Contains(t, stderr.String(), "samples read 400")
}

// The samples recorded twice (a backtest run again over the same period, a
// file given twice) are trained on once, the last read kept; --source and
// --segment keep the samples recorded there, counted by source.
func TestMetalabelCommand_DuplicatesSourcesAndSegments(t *testing.T) {
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(4, 4))
	fit, again := filepath.Join(dir, "fit.jsonl"), filepath.Join(dir, "again.jsonl")
	validation, paper := filepath.Join(dir, "validation.jsonl"), filepath.Join(dir, "paper.jsonl")
	backtest := metalabel.Sample{Agent: "gold", Source: metalabel.SourceBacktest, Run: 7,
		Segment: metalabel.SegmentFit}
	writeSamplesOf(t, fit, backtest, 0, 200, rng)
	backtest.Run = 9 // the same period run again: the same entries
	writeSamplesOf(t, again, backtest, 0, 200, rng)
	backtest.Run, backtest.Segment = 10, metalabel.SegmentValidation
	writeSamplesOf(t, validation, backtest, 200, 100, rng)
	writeSamplesOf(t, paper, metalabel.Sample{Agent: "gold", Source: metalabel.SourcePaper}, 300, 60, rng)

	var stdout, stderr bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{metalabelCommand}, Writer: &stdout, ErrWriter: &stderr}
	all := []string{"xtrader", "metalabel", "--samples", fit, "--samples", again, "--samples", validation,
		"--samples", paper}
	require.NoError(t, app.Run(all))
	assert.Contains(t, stdout.String(), "samples read 560, 200 duplicates dropped, 360 kept by the filters: "+
		"backtest 300, paper 60")
	assert.Contains(t, stdout.String(), "samples 360 ")

	stdout.Reset()
	require.NoError(t, app.Run(append(all, "--source", "backtest", "--segment", "fit")))
	assert.Contains(t, stdout.String(), "200 kept by the filters: backtest 200")
	stdout.Reset()
	require.NoError(t, app.Run(append(all, "--source", "paper, live")))
	assert.Contains(t, stdout.String(), "60 kept by the filters: paper 60")

	require.ErrorContains(t, app.Run(append(all, "--source", "demo")), `--source: "demo"`)
	require.ErrorContains(t, app.Run(append(all, "--segment", "test")), `--segment: "test"`)

	samples := []metalabel.Sample{{Agent: "a", Time: 1, Entry: 100, Run: 1}, {Agent: "a", Time: 1, Entry: 101},
		{Agent: "a", Time: 1, Entry: 100, Run: 2}, {Agent: "a", Time: 2, Entry: 100}}
	kept, dropped := dedupeSamples(samples)
	assert.Equal(t, 1, dropped)
	assert.Equal(t, []int64{0, 2, 0}, []int64{kept[0].Run, kept[1].Run, kept[2].Run}, "the last read kept, in order")
}

// Run as root, eg: by a plain docker exec, the command tells that the agents
// can't read the model it wrote where they run as the user trader, and how to
// run it; as another user, nothing.
func TestMetalabelCommand_RootHint(t *testing.T) {
	dir := t.TempDir()
	samples := filepath.Join(dir, "gold.jsonl")
	writeSamples(t, samples, "gold", 100, rand.New(rand.NewPCG(5, 5)))
	out := filepath.Join(dir, "gold.json")
	euid := geteuid
	t.Cleanup(func() { geteuid = euid })

	var stdout, stderr bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{metalabelCommand}, Writer: &stdout, ErrWriter: &stderr}
	geteuid = func() int { return 0 }
	require.NoError(t, app.Run([]string{"xtrader", "metalabel", "--samples", samples, "--out", out}))
	assert.Contains(t, stderr.String(), "run as root")
	assert.Contains(t, stderr.String(), "docker exec -u trader")

	stderr.Reset()
	geteuid = func() int { return 1000 }
	require.NoError(t, app.Run([]string{"xtrader", "metalabel", "--samples", samples, "--out", out}))
	assert.NotContains(t, stderr.String(), "run as root")
	assert.Contains(t, metalabelCommand.Usage, "docker exec -u trader", "told in the help")
}

func TestFilterSamples(t *testing.T) {
	samples := []metalabel.Sample{{Agent: "Gold", Strategy: "matrend", Pair: "XAUUSDm", Source: "backtest",
		Segment: "fit"}, {Agent: "gold", Strategy: "rsi2", Pair: "BTCUSDm", Source: "live"},
		{Agent: "other", Strategy: "matrend", Pair: "XAUUSDm"}}
	assert.Len(t, filterSamples(samples, sampleFilter{agent: "gold"}), 2)
	assert.Len(t, filterSamples(samples, sampleFilter{strategy: "matrend", pair: "xauusdm"}), 2)
	assert.Len(t, filterSamples(samples, sampleFilter{}), 3)
	assert.Len(t, filterSamples(samples, sampleFilter{sources: []string{"backtest", "live"}}), 2)
	assert.Len(t, filterSamples(samples, sampleFilter{segment: "fit"}), 1)
}
