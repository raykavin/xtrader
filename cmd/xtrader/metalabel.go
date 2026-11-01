package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"

	"github.com/raykavin/xtrader/agents/metalabel"
)

// The meta-labeling model of an agent is trained here from the samples its
// entry filter recorded (-meta-record, one JSON line per attempted entry with
// its features and its triple-barrier label, see agents/metalabel), then
// given to the agent with -meta-model: it only lets an entry through when the
// probability of its target before its stop reaches the threshold.
//
// The samples read are first rid of the ones recorded twice (the same entry of
// an agent, strategy, pair and side at the same time and price, eg: a backtest
// run again over the same period appending to the same file), the last read
// kept, then kept by the filters (the agent, the strategy, the pair, where
// they were recorded and the segment of the backtest), and counted by where
// they were recorded.
//
// In production the agents run in the container of the controller as the
// user trader, and the model is written readable by its owner and its group
// only (Model.Save): written by root, eg: by a plain `docker exec`, the
// agents can't read it. The command is run as that user there (`docker exec
// -u trader ...`), and tells so when it runs as root.

// metalabelProduction is how the command is run in production: as the user of
// the agents, so that they read the model it writes (see above).
const metalabelProduction = "docker exec -u trader <container of the controller> xtrader metalabel ..."

// geteuid returns the effective user of the process, a variable for the tests.
var geteuid = os.Geteuid

var metalabelCommand = &cli.Command{
	Name:     "metalabel",
	HelpName: "metalabel",
	Usage: "Train the meta-labeling model of an agent from the samples of -meta-record: a logistic " +
		"regression, tested on the newest samples, written with -out for -meta-model (in production, run it " +
		"as the user of the agents: " + metalabelProduction + ")",
	Description: "The samples recorded twice (the same agent, strategy, pair, side, time and entry) are " +
		"dropped first, the last read kept; the filters then keep the samples trained on, counted by where " +
		"they were recorded (live, paper, backtest).\n\n" +
		"In production the agents run as the user trader and the model is written readable by its owner and " +
		"group only: run the command as that user, eg:\n\n   " + metalabelProduction + "\n\n" +
		"a model written by root (a plain docker exec) can't be read by the agents.",
	Flags: []cli.Flag{
		&cli.StringSliceFlag{Name: "samples", Usage: "JSONL file(s) of -meta-record", Required: true},
		&cli.StringFlag{Name: "out", Usage: "the model file to write, eg data/meta/matrend.json (none: report only)"},
		&cli.StringFlag{Name: "agent", Usage: "only the samples of this agent"},
		&cli.StringFlag{Name: "strategy", Usage: "only the samples of this strategy"},
		&cli.StringFlag{Name: "pair", Usage: "only the samples of this pair"},
		&cli.StringFlag{Name: "source", Usage: "only the samples recorded there, separated by commas: live, " +
			"paper, backtest (the samples recorded before their source was written have none: left out)"},
		&cli.StringFlag{Name: "segment", Usage: "only the samples of the backtests of the controller of this " +
			"segment of their period: fit or validation"},
		&cli.Float64Flag{Name: "train-share", Usage: "the oldest share of the samples trained on, the rest tested",
			Value: metalabel.DefaultTrainShare},
		&cli.Float64Flag{Name: "l2", Usage: "the L2 penalty of the weights", Value: metalabel.DefaultL2},
		&cli.Float64Flag{Name: "threshold", Usage: "the threshold kept in the model",
			Value: metalabel.DefaultThreshold},
		&cli.BoolFlag{Name: "json", Usage: "print the report as JSON"},
	},
	Action: func(c *cli.Context) error {
		sources, err := parseSources(c.String("source"))
		if err != nil {
			return err
		}
		segment := strings.TrimSpace(c.String("segment"))
		if segment != "" && segment != metalabel.SegmentFit && segment != metalabel.SegmentValidation {
			return fmt.Errorf("--segment: %q, %s or %s", segment, metalabel.SegmentFit, metalabel.SegmentValidation)
		}
		var samples []metalabel.Sample
		for _, file := range c.StringSlice("samples") {
			read, err := readSamples(file)
			if err != nil {
				return err
			}
			samples = append(samples, read...)
		}
		read := len(samples)
		samples, duplicates := dedupeSamples(samples)
		samples = filterSamples(samples, sampleFilter{agent: c.String("agent"), strategy: c.String("strategy"),
			pair: c.String("pair"), sources: sources, segment: segment})
		// the counts with the report, on the standard error under --json
		counts := c.App.Writer
		if c.Bool("json") {
			counts = c.App.ErrWriter
		}
		printSampleCounts(counts, read, duplicates, samples)
		if len(samples) == 0 {
			return errors.New("no sample left to train on")
		}
		model, report, err := metalabel.Train(samples, metalabel.TrainOptions{TrainShare: c.Float64("train-share"),
			L2: c.Float64("l2"), Threshold: c.Float64("threshold")})
		if err != nil {
			return err
		}
		if c.Bool("json") {
			encoder := json.NewEncoder(c.App.Writer)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(report); err != nil {
				return err
			}
		} else {
			printMetalabel(c.App.Writer, report)
		}
		if out := c.String("out"); out != "" {
			if err := model.Save(out); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(c.App.ErrWriter, "model written to %s: give it to the agent with -meta-model=%s\n",
				out, out)
			if geteuid() == 0 {
				_, _ = fmt.Fprintf(c.App.ErrWriter, "run as root: %s belongs to root, readable by its owner and "+
					"group only, which the agents can't read where they run as the user trader (production): run "+
					"the command as that user, %s, or give the file to it (chown trader:trader %s)\n", out,
					metalabelProduction, out)
			}
		}
		return nil
	},
}

// readSamples reads the samples of a JSONL file of -meta-record.
func readSamples(file string) ([]metalabel.Sample, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	samples, err := metalabel.ReadSamples(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return samples, nil
}

// parseSources reads the list of --source, separated by commas: the sources
// of metalabel.Sources, none for any.
func parseSources(value string) ([]string, error) {
	var sources []string
	for _, source := range strings.Split(value, ",") {
		source = strings.ToLower(strings.TrimSpace(source))
		switch {
		case source == "":
			continue
		case !slices.Contains(metalabel.Sources, source):
			return nil, fmt.Errorf("--source: %q, among %s", source, strings.Join(metalabel.Sources, ", "))
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// sampleKey is what tells an entry recorded twice apart from another one: its
// agent, strategy, pair, side, time and entry price.
type sampleKey struct {
	agent, strategy, pair, side string
	time                        int64
	entry                       float64
}

// dedupeSamples drops the samples of an entry recorded more than once (see
// sampleKey), eg: a backtest run again over the same period appending to the
// same file, keeping the last one read, in the order they were read; and
// returns how many were dropped.
func dedupeSamples(samples []metalabel.Sample) ([]metalabel.Sample, int) {
	last := make(map[sampleKey]int, len(samples))
	keyOf := func(s metalabel.Sample) sampleKey {
		return sampleKey{agent: s.Agent, strategy: s.Strategy, pair: s.Pair, side: s.Side, time: s.Time,
			entry: s.Entry}
	}
	for i, s := range samples {
		last[keyOf(s)] = i
	}
	kept := make([]metalabel.Sample, 0, len(last))
	for i, s := range samples {
		if last[keyOf(s)] == i {
			kept = append(kept, s)
		}
	}
	return kept, len(samples) - len(kept)
}

// sampleFilter keeps the samples of an agent, a strategy, a pair (each "" for
// any, compared without regard to case), of sources (none for any) and of a
// segment of the backtests of the controller ("" for any).
type sampleFilter struct {
	agent, strategy, pair string
	sources               []string
	segment               string
}

// filterSamples keeps the samples of samples filter keeps.
func filterSamples(samples []metalabel.Sample, filter sampleFilter) []metalabel.Sample {
	var kept []metalabel.Sample
	for _, s := range samples {
		if filter.agent != "" && !strings.EqualFold(s.Agent, filter.agent) ||
			filter.strategy != "" && !strings.EqualFold(s.Strategy, filter.strategy) ||
			filter.pair != "" && !strings.EqualFold(s.Pair, filter.pair) ||
			len(filter.sources) > 0 && !slices.Contains(filter.sources, s.Source) ||
			filter.segment != "" && s.Segment != filter.segment {
			continue
		}
		kept = append(kept, s)
	}
	return kept
}

// printSampleCounts prints the samples read, the duplicates dropped, and the
// samples kept by the filters by where they were recorded ("none" for the
// ones recorded before their source was written).
func printSampleCounts(w io.Writer, read, duplicates int, kept []metalabel.Sample) {
	bySource := make(map[string]int)
	for _, s := range kept {
		source := s.Source
		if source == "" {
			source = "none"
		}
		bySource[source]++
	}
	counts := make([]string, 0, len(bySource))
	for _, source := range slices.Sorted(maps.Keys(bySource)) {
		counts = append(counts, fmt.Sprintf("%s %d", source, bySource[source]))
	}
	_, _ = fmt.Fprintf(w, "samples read %d, %d duplicates dropped, %d kept by the filters", read, duplicates,
		len(kept))
	if len(counts) > 0 {
		_, _ = fmt.Fprintf(w, ": %s", strings.Join(counts, ", "))
	}
	_, _ = fmt.Fprintln(w)
}

func printMetalabel(w io.Writer, r metalabel.Report) {
	_, _ = fmt.Fprintf(w, "samples %d (skipped %d): trained on %d, %d purged, tested on %d\n", r.Samples, r.Skipped,
		r.Train, r.Purged, r.Test)
	_, _ = fmt.Fprintf(w, "targets first: %.1f%% of the training, %.1f%% of all\n", r.TrainBaseRate*100,
		r.BaseRate*100)
	_, _ = fmt.Fprintf(w, "AUC: training %.3f, test %.3f (0.5 is chance); %d Newton steps, converged %t\n\n",
		r.TrainAUC, r.TestAUC, r.Iterations, r.Converged)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "threshold\ttaken\tcoverage\tprecision")
	for _, t := range r.Thresholds {
		_, _ = fmt.Fprintf(tw, "%.2f\t%d\t%.0f%%\t%.1f%%\n", t.Threshold, t.Taken, t.Coverage*100, t.Precision*100)
	}
	_ = tw.Flush()
}
