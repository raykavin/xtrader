package metalabel

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// ModelVersion is the version of the models this package reads and writes.
const ModelVersion = 1

// DefaultThreshold is the threshold of a model trained without one: an entry
// as likely to reach its target first as not.
const DefaultThreshold = 0.5

// Model is a logistic regression telling the probability of an entry reaching
// its target first (its label 1, see Sample), as a JSON file (see Load and
// Save): its version; its features, in order, each standardized with its mean
// and standard deviation of the training samples, and weighted; the bias; the
// threshold of the entries taken (a probability at or above it); and how it
// was trained: when (Unix seconds), the newest outcome it learned
// (TrainedThrough, the latest exit time of the samples trained on, Unix
// seconds: a backtest of a period starting before it would test the model on
// what it learned, see the runner of the agents; 0 in the models written
// before it was), on how many samples and positives, its area under the ROC
// curve on the samples held out for the test and their count, and its L2
// penalty.
type Model struct {
	Version        int       `json:"version"`
	Features       []string  `json:"features"`
	Mean           []float64 `json:"mean"`
	Std            []float64 `json:"std"`
	Weights        []float64 `json:"weights"`
	Bias           float64   `json:"bias"`
	Threshold      float64   `json:"threshold"`
	TrainedAt      int64     `json:"trained_at"`
	TrainedThrough int64     `json:"trained_through,omitempty"`
	Samples        int       `json:"samples"`
	Positives      int       `json:"positives"`
	TestAUC        float64   `json:"test_auc"`
	TestSamples    int       `json:"test_samples"`
	L2             float64   `json:"l2"`
}

// Validate returns why m can't be used: another version, its features and
// coefficients of different lengths, a feature twice, a standard deviation
// not above 0, a coefficient not finite, a threshold out of [0, 1).
func (m *Model) Validate() error {
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	switch {
	case m.Version != ModelVersion:
		return fmt.Errorf("metalabel: the model is of version %d, not %d", m.Version, ModelVersion)
	case len(m.Features) == 0:
		return errors.New("metalabel: the model has no feature")
	case len(m.Mean) != len(m.Features) || len(m.Std) != len(m.Features) || len(m.Weights) != len(m.Features):
		return fmt.Errorf("metalabel: the model has %d features but %d means, %d deviations and %d weights",
			len(m.Features), len(m.Mean), len(m.Std), len(m.Weights))
	case !finite(m.Bias):
		return errors.New("metalabel: the bias of the model isn't finite")
	case !finite(m.Threshold) || m.Threshold < 0 || m.Threshold >= 1:
		return fmt.Errorf("metalabel: the threshold of the model, %g, out of [0, 1)", m.Threshold)
	}
	seen := make(map[string]bool, len(m.Features))
	for j, name := range m.Features {
		switch {
		case name == "" || seen[name]:
			return fmt.Errorf("metalabel: the feature %q of the model empty or twice", name)
		case !finite(m.Mean[j]) || !finite(m.Weights[j]):
			return fmt.Errorf("metalabel: the coefficients of the feature %s aren't finite", name)
		case !finite(m.Std[j]) || m.Std[j] <= 0:
			return fmt.Errorf("metalabel: the standard deviation of the feature %s must be above 0", name)
		}
		seen[name] = true
	}
	return nil
}

// Probability returns the probability of an entry of features reaching its
// target first: the logistic function of the bias plus the weights times the
// features standardized. Every feature of the model is required.
func (m *Model) Probability(features map[string]float64) (float64, error) {
	if len(m.Features) == 0 || len(m.Mean) != len(m.Features) || len(m.Std) != len(m.Features) ||
		len(m.Weights) != len(m.Features) {
		return 0, errors.New("metalabel: an invalid model")
	}
	score := m.Bias
	for j, name := range m.Features {
		value, ok := features[name]
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, fmt.Errorf("metalabel: the feature %s is missing", name)
		}
		score += m.Weights[j] * (value - m.Mean[j]) / m.Std[j]
	}
	return sigmoid(score), nil
}

// sigmoid is the logistic function.
func sigmoid(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}

// Load reads the model of path, validated.
func Load(path string) (*Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("metalabel: the model: %w", err)
	}
	var m Model
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("metalabel: the model %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%w (%s)", err, path)
	}
	return &m, nil
}

// Save writes m to path at once (a file written aside, then renamed over it),
// validated first.
func (m *Model) Save(path string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
