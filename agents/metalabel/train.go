package metalabel

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"

	"gonum.org/v1/gonum/mat"
)

// The defaults of TrainOptions.
const (
	DefaultTrainShare = 0.7
	DefaultL2         = 1.0
	defaultIterations = 100
	defaultTolerance  = 1e-8
	// minTrainSamples is the least samples a model is trained on, after the
	// purge, both labels among them.
	minTrainSamples = 20
)

// ReportThresholds are the thresholds Report tells the precision and the
// coverage of, on the samples held out for the test.
var ReportThresholds = []float64{0.40, 0.45, 0.50, 0.55, 0.60, 0.65, 0.70}

// TrainOptions are how Train fits a model, each field taking its default at
// its zero value: TrainShare the share of the samples, the oldest, trained on
// (DefaultTrainShare), the rest held out for the test; L2 the penalty of the
// squared weights, the bias left out (DefaultL2, a negative one refused);
// Iterations and Tolerance bound the Newton steps (100, and a step under
// 1e-8); Threshold the threshold of the model (DefaultThreshold); Features
// the features, in order (FeatureNames); TrainedAt the time of the model, Unix
// seconds (the time of the newest sample trained on, so that the same samples
// make the same model).
type TrainOptions struct {
	TrainShare float64
	L2         float64
	Iterations int
	Tolerance  float64
	Threshold  float64
	Features   []string
	TrainedAt  int64
}

// ThresholdStat is how a threshold does on the samples held out for the test:
// how many of them it takes (a probability at or above it), their share of the
// test (Coverage), and the share of positives among them (Precision, 0 for
// none taken).
type ThresholdStat struct {
	Threshold float64 `json:"threshold"`
	Taken     int     `json:"taken"`
	Coverage  float64 `json:"coverage"`
	Precision float64 `json:"precision"`
}

// Report is how a model was trained and how it does: the samples read, the
// ones skipped (a feature missing), trained on, purged (their outcome known
// only after the first sample of the test, see Train) and tested, their
// positives; the base rates (the share of positives: of the test, the
// precision of taking every entry, and of the training); the areas under the
// ROC curve of the training and of the test (0 when the test holds a single
// label); the Newton steps and whether they converged; and the precision and
// the coverage of ReportThresholds on the test.
type Report struct {
	Samples        int             `json:"samples"`
	Skipped        int             `json:"skipped"`
	Train          int             `json:"train"`
	Purged         int             `json:"purged"`
	Test           int             `json:"test"`
	TrainPositives int             `json:"train_positives"`
	TestPositives  int             `json:"test_positives"`
	BaseRate       float64         `json:"base_rate"`
	TrainBaseRate  float64         `json:"train_base_rate"`
	TrainAUC       float64         `json:"train_auc"`
	TestAUC        float64         `json:"test_auc"`
	Iterations     int             `json:"iterations"`
	Converged      bool            `json:"converged"`
	Thresholds     []ThresholdStat `json:"thresholds"`
}

// Train fits a model on samples: a logistic regression of their label on their
// features standardized (by the mean and the population standard deviation of
// the training samples, a constant feature by 1), with an L2 penalty, by
// iteratively reweighted least squares (Newton's method), deterministic: the
// same samples and options give the same model. The samples are ordered by
// time (then pair, side and entry): the oldest TrainShare of them are trained
// on, the rest held out for the test, never shuffled, the future never
// training the past; and the training samples whose outcome is known only
// after the first test sample (their ExitTime at or after its Time) are
// purged (AFML §7.4.1), their labels overlapping the test. A sample missing a
// feature, or of a label other than 0 or 1, is skipped. Fewer than 20 samples
// to train on, or a single label among them, is an error. The model keeps the
// latest exit time of the samples trained on (Model.TrainedThrough): what it
// learned ends there.
func Train(samples []Sample, opts TrainOptions) (*Model, Report, error) {
	var report Report
	if opts.TrainShare == 0 {
		opts.TrainShare = DefaultTrainShare
	}
	if opts.L2 == 0 {
		opts.L2 = DefaultL2
	}
	if opts.Iterations == 0 {
		opts.Iterations = defaultIterations
	}
	if opts.Tolerance == 0 {
		opts.Tolerance = defaultTolerance
	}
	if opts.Threshold == 0 {
		opts.Threshold = DefaultThreshold
	}
	if len(opts.Features) == 0 {
		opts.Features = FeatureNames()
	}
	switch {
	case !(opts.TrainShare > 0 && opts.TrainShare < 1):
		return nil, report, fmt.Errorf("metalabel: the share trained on, %g, out of (0, 1)", opts.TrainShare)
	case opts.L2 < 0 || math.IsNaN(opts.L2) || math.IsInf(opts.L2, 0):
		return nil, report, errors.New("metalabel: the L2 penalty must be 0 or more")
	case opts.Iterations < 0 || !(opts.Tolerance > 0):
		return nil, report, errors.New("metalabel: the iterations and the tolerance must be above 0")
	case !(opts.Threshold > 0 && opts.Threshold < 1):
		return nil, report, fmt.Errorf("metalabel: the threshold, %g, out of (0, 1)", opts.Threshold)
	}

	report.Samples = len(samples)
	usable := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if (s.Label == 0 || s.Label == 1) && hasFeatures(s.Features, opts.Features) {
			usable = append(usable, s)
		}
	}
	report.Skipped = len(samples) - len(usable)
	slices.SortStableFunc(usable, func(a, b Sample) int {
		return cmp.Or(cmp.Compare(a.Time, b.Time), cmp.Compare(a.Pair, b.Pair), cmp.Compare(a.Side, b.Side),
			cmp.Compare(a.Entry, b.Entry))
	})
	split := int(math.Round(float64(len(usable)) * opts.TrainShare))
	train, test := usable[:split], usable[split:]
	if len(test) > 0 {
		first := test[0].Time
		kept := make([]Sample, 0, len(train))
		for _, s := range train {
			if s.ExitTime == 0 || s.ExitTime < first {
				kept = append(kept, s)
			}
		}
		report.Purged = len(train) - len(kept)
		train = kept
	}
	report.Train, report.Test = len(train), len(test)
	report.TrainPositives, report.TestPositives = positives(train), positives(test)
	switch {
	case len(train) < minTrainSamples:
		return nil, report, fmt.Errorf("metalabel: %d samples to train on, %d at least", len(train), minTrainSamples)
	case report.TrainPositives == 0 || report.TrainPositives == len(train):
		return nil, report, errors.New("metalabel: the samples trained on hold a single label")
	}

	k := len(opts.Features)
	mean, std := make([]float64, k), make([]float64, k)
	for j, name := range opts.Features {
		for _, s := range train {
			mean[j] += s.Features[name]
		}
		mean[j] /= float64(len(train))
		for _, s := range train {
			d := s.Features[name] - mean[j]
			std[j] += d * d
		}
		std[j] = math.Sqrt(std[j] / float64(len(train)))
		if !(std[j] > 1e-12) {
			std[j] = 1 // a constant feature: its weight stays at 0
		}
	}
	design := func(set []Sample) *mat.Dense {
		x := mat.NewDense(len(set), k+1, nil)
		for i, s := range set {
			x.Set(i, 0, 1)
			for j, name := range opts.Features {
				x.Set(i, j+1, (s.Features[name]-mean[j])/std[j])
			}
		}
		return x
	}
	x := design(train)
	y := make([]float64, len(train))
	for i, s := range train {
		y[i] = float64(s.Label)
	}
	w, iterations, converged, err := irls(x, y, opts.L2, opts.Iterations, opts.Tolerance)
	if err != nil {
		return nil, report, err
	}
	report.Iterations, report.Converged = iterations, converged

	m := &Model{Version: ModelVersion, Features: slices.Clone(opts.Features), Mean: mean, Std: std,
		Weights: w[1:], Bias: w[0], Threshold: opts.Threshold, TrainedAt: opts.TrainedAt, Samples: len(train),
		Positives: report.TrainPositives, TestSamples: len(test), L2: opts.L2}
	if m.TrainedAt == 0 {
		m.TrainedAt = train[len(train)-1].Time
	}
	// the newest outcome learned: a sample's label is known once it exited (its
	// time for one without an exit time)
	for _, s := range train {
		m.TrainedThrough = max(m.TrainedThrough, s.ExitTime, s.Time)
	}
	probabilities := func(set []Sample) []float64 {
		p := make([]float64, len(set))
		for i, s := range set {
			p[i], _ = m.Probability(s.Features)
		}
		return p
	}
	report.TrainBaseRate = float64(report.TrainPositives) / float64(len(train))
	report.TrainAUC = auc(probabilities(train), train)
	tested := probabilities(test)
	if len(test) > 0 {
		report.BaseRate = float64(report.TestPositives) / float64(len(test))
	}
	report.TestAUC = auc(tested, test)
	m.TestAUC = report.TestAUC
	for _, threshold := range ReportThresholds {
		stat := ThresholdStat{Threshold: threshold}
		hits := 0
		for i, p := range tested {
			if p >= threshold {
				stat.Taken++
				hits += test[i].Label
			}
		}
		if len(test) > 0 {
			stat.Coverage = float64(stat.Taken) / float64(len(test))
		}
		if stat.Taken > 0 {
			stat.Precision = float64(hits) / float64(stat.Taken)
		}
		report.Thresholds = append(report.Thresholds, stat)
	}
	return m, report, nil
}

// irls fits the logistic regression of y on x (its first column the
// intercept, never penalized) with the L2 penalty l2 by Newton's method: the
// coefficients, the steps made, and whether the last one was under tolerance.
func irls(x *mat.Dense, y []float64, l2 float64, iterations int, tolerance float64) ([]float64, int, bool,
	error) {
	n, k := x.Dims()
	w := mat.NewVecDense(k, nil)
	p := mat.NewVecDense(n, nil)
	for step := 1; step <= iterations; step++ {
		p.MulVec(x, w)
		for i := range n {
			p.SetVec(i, sigmoid(p.AtVec(i)))
		}
		// the gradient of the loss, X'(p-y) + l2·w, and its Hessian, X'WX + l2·I
		residual := mat.NewVecDense(n, nil)
		weighted := mat.NewDense(n, k, nil)
		for i := range n {
			pi := p.AtVec(i)
			residual.SetVec(i, pi-y[i])
			weight := pi * (1 - pi)
			for j := range k {
				weighted.Set(i, j, weight*x.At(i, j))
			}
		}
		gradient := mat.NewVecDense(k, nil)
		gradient.MulVec(x.T(), residual)
		var hessian mat.Dense
		hessian.Mul(x.T(), weighted)
		for j := 1; j < k; j++ {
			gradient.SetVec(j, gradient.AtVec(j)+l2*w.AtVec(j))
			hessian.Set(j, j, hessian.At(j, j)+l2)
		}
		symmetric := mat.NewSymDense(k, nil)
		for a := range k {
			for b := a; b < k; b++ {
				symmetric.SetSym(a, b, (hessian.At(a, b)+hessian.At(b, a))/2)
			}
		}
		var chol mat.Cholesky
		if ok := chol.Factorize(symmetric); !ok {
			return nil, step, false, errors.New("metalabel: the fit is singular (the samples separated, or a " +
				"feature repeating another): raise the L2 penalty")
		}
		var delta mat.VecDense
		if err := chol.SolveVecTo(&delta, gradient); err != nil {
			return nil, step, false, fmt.Errorf("metalabel: the fit: %w", err)
		}
		largest := 0.0
		for j := range k {
			largest = math.Max(largest, math.Abs(delta.AtVec(j)))
			w.SetVec(j, w.AtVec(j)-delta.AtVec(j))
		}
		if math.IsNaN(largest) {
			return nil, step, false, errors.New("metalabel: the fit diverged")
		}
		if largest < tolerance {
			return vecData(w), step, true, nil
		}
	}
	return vecData(w), iterations, false, nil
}

func vecData(v *mat.VecDense) []float64 {
	out := make([]float64, v.Len())
	for i := range out {
		out[i] = v.AtVec(i)
	}
	return out
}

// hasFeatures reports whether features holds every one of names, finite.
func hasFeatures(features map[string]float64, names []string) bool {
	for _, name := range names {
		value, ok := features[name]
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

// positives returns how many samples are labeled 1.
func positives(samples []Sample) int {
	n := 0
	for _, s := range samples {
		n += s.Label
	}
	return n
}

// auc returns the area under the ROC curve of the probabilities of samples: the
// probability that a positive ranks above a negative, the ties half (the
// statistic of Mann-Whitney, by the mean ranks); 0 without both labels.
func auc(probabilities []float64, samples []Sample) float64 {
	n := len(samples)
	pos := positives(samples)
	neg := n - pos
	if pos == 0 || neg == 0 {
		return 0
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(probabilities[a], probabilities[b]) })
	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i
		for j+1 < n && probabilities[order[j+1]] == probabilities[order[i]] {
			j++
		}
		rank := float64(i+j)/2 + 1 // the mean rank of the ties, from 1
		for t := i; t <= j; t++ {
			ranks[order[t]] = rank
		}
		i = j + 1
	}
	var sum float64
	for i, s := range samples {
		if s.Label == 1 {
			sum += ranks[i]
		}
	}
	return (sum - float64(pos)*float64(pos+1)/2) / (float64(pos) * float64(neg))
}
