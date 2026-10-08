package tools

// TrailingStop follows the best price of a position, its stop moved by as
// much as the price moves in favor of the position, never back: of a BUY when
// started with the stop below the price, of a SELL when above it.
//
// Deprecated: no agent uses it, only the unregistered strategy of
// agents/strategies.NewTrailing; the agents trail their stops at the broker,
// see agents/strategies (trailing-atr). Kept until its removal is decided.
type TrailingStop struct {
	current float64 // the best price since the start
	stop    float64
	sell    bool // the stop above the price
	active  bool
}

// NewTrailingStop returns a trailing stop not started.
//
// Deprecated: see TrailingStop.
func NewTrailingStop() *TrailingStop {
	return &TrailingStop{}
}

// Start follows a position at current with its stop: of a SELL when stop is
// above current, else of a BUY.
func (t *TrailingStop) Start(current, stop float64) {
	t.stop = stop
	t.current = current
	t.sell = stop > current
	t.active = true
}

func (t *TrailingStop) Stop() {
	t.active = false
}

func (t TrailingStop) Active() bool {
	return t.active
}

// Update moves the stop with a price better than the best one since the start,
// by as much, so a price back from a worse one doesn't move it, and tells
// whether current reached it: at or below it for a BUY, at or above it for a
// SELL.
func (t *TrailingStop) Update(current float64) bool {
	if !t.active {
		return false
	}

	if t.sell {
		if current < t.current {
			t.stop = t.stop - (t.current - current)
			t.current = current
			return false
		}
		return current >= t.stop
	}

	if current > t.current {
		t.stop = t.stop + (current - t.current)
		t.current = current
		return false
	}

	return current <= t.stop
}
