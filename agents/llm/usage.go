package llm

import (
	"sync"
	"time"

	"github.com/raykavin/xtrader/tools/log"
)

// Usage is the tokens of the answers of a provider: Input the tokens of the
// prompts, Cached the ones of them read from the cache of the provider, billed
// at a discount, Output the tokens answered, and Reasoning the ones of them
// the model spent reasoning, billed as output but not shown.
type Usage struct {
	Input     int
	Cached    int
	Output    int
	Reasoning int
}

func (u *Usage) add(other Usage) {
	u.Input += other.Input
	u.Cached += other.Cached
	u.Output += other.Output
	u.Reasoning += other.Reasoning
}

// usageMeter sums the usage of a provider over the day, in UTC, as the bills
// of the providers do.
type usageMeter struct {
	mtx      sync.Mutex
	day      string
	today    Usage
	requests int
	now      func() time.Time
}

func newUsageMeter() *usageMeter {
	return &usageMeter{now: time.Now}
}

// add records the usage of an answer and returns the ones of the day, and the
// answers of the day, this one included.
func (m *usageMeter) add(usage Usage) (Usage, int) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if day := m.now().UTC().Format(time.DateOnly); day != m.day {
		m.day, m.today, m.requests = day, Usage{}, 0
	}
	m.today.add(usage)
	m.requests++
	return m.today, m.requests
}

// Today returns the usage of the answers of the day, in UTC, and their count.
func (m *usageMeter) Today() (Usage, int) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if m.now().UTC().Format(time.DateOnly) != m.day {
		return Usage{}, 0
	}
	return m.today, m.requests
}

// record logs the usage of the answer to the context of in, with the one of
// the day, and adds it to the meter of the provider.
func (c client) record(in Input, usage Usage) {
	if c.usage == nil {
		return
	}
	today, requests := c.usage.add(usage)
	log.Infof("[LLM] %s %s | %s | tokens: in %d (cached %d), out %d (reasoning %d) | today: %d requests, "+
		"in %d (cached %d), out %d (reasoning %d)", in.Symbol, in.Timeframe, c, usage.Input, usage.Cached,
		usage.Output, usage.Reasoning, requests, today.Input, today.Cached, today.Output, today.Reasoning)
}

// TodayUsage returns the tokens the provider used today, in UTC, and the count
// of its answers.
func (c client) TodayUsage() (Usage, int) {
	if c.usage == nil {
		return Usage{}, 0
	}
	return c.usage.Today()
}
