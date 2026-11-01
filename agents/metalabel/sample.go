package metalabel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// The outcomes of a sample: its target touched first, its stop touched first
// (both on one candle: the stop, the worst case), or neither by its vertical
// barrier or by the exit of the strategy (time).
const (
	OutcomeTarget = "target"
	OutcomeStop   = "stop"
	OutcomeTime   = "time"
)

// Where a sample was recorded (Sample.Source): an agent trading live, in paper,
// or a backtest.
const (
	SourceLive     = "live"
	SourcePaper    = "paper"
	SourceBacktest = "backtest"
)

// Sources are the sources of the samples, see SourceLive.
var Sources = []string{SourceLive, SourcePaper, SourceBacktest}

// The segments of a backtest of the controller a sample may come from
// (Sample.Segment): the fit, the older part of the period, or the validation,
// the newer one held out.
const (
	SegmentFit        = "fit"
	SegmentValidation = "validation"
)

// Sample is an entry a strategy attempted, as an agent records it with
// -meta-record, a JSON line each (snake_case): the agent, its strategy, where
// it was recorded (Source, see SourceLive; "" in the samples recorded before
// it was written), the backtest run of the controller (Run, 0 for none) and
// its segment (Segment, see SegmentFit; "" for none), the pair and the side
// (buy or sell); Time, the open of the candle the entry was
// decided at the close of (Unix seconds); the entry price (the close for an
// entry at the market, the level of a limit or stop entry), its stop loss and
// take profit (0 for none); its features (see Features), and its outcome:
// Label 1 when its target was touched first, or when it ended at its vertical
// barrier or by the exit of the strategy with a positive result, else 0 (AFML
// §3.4, the sign of the return at the vertical barrier); R its result in R
// (the risk the distance to the stop: -1 at the stop, the reward over the
// risk at the target), 0 without a stop; Outcome how it ended (see
// OutcomeTarget); ExitTime the open of the candle it ended on.
type Sample struct {
	Agent    string             `json:"agent"`
	Strategy string             `json:"strategy"`
	Source   string             `json:"source,omitempty"`
	Run      int64              `json:"run,omitempty"`
	Segment  string             `json:"segment,omitempty"`
	Pair     string             `json:"pair"`
	Side     string             `json:"side"`
	Time     int64              `json:"time"`
	Entry    float64            `json:"entry"`
	Stop     float64            `json:"stop"`
	Target   float64            `json:"target"`
	Features map[string]float64 `json:"features"`
	Label    int                `json:"label"`
	R        float64            `json:"r"`
	Outcome  string             `json:"outcome"`
	ExitTime int64              `json:"exit_time"`
}

// MarshalLine returns s as a line of JSON, its newline included: what a
// recorder appends in one write.
func (s Sample) MarshalLine() ([]byte, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ReadSamples reads the samples of r, a JSON line each, the blank lines
// skipped: an error tells the line that can't be read.
func ReadSamples(r io.Reader) ([]Sample, error) {
	var samples []Sample
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for line := 1; scanner.Scan(); line++ {
		text := bytes.TrimSpace(scanner.Bytes())
		if len(text) == 0 {
			continue
		}
		var s Sample
		if err := json.Unmarshal(text, &s); err != nil {
			return nil, fmt.Errorf("metalabel: the sample of line %d: %w", line, err)
		}
		samples = append(samples, s)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("metalabel: the samples: %w", err)
	}
	return samples, nil
}
