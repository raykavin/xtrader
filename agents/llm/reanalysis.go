package llm

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// The statuses of a reanalysis, see ui.ReanalysisResponse.
const (
	ReanalysisQueued  = "queued"
	ReanalysisRunning = "running"
)

// reanalysis is a reanalysis asked, see Reanalyze: when, the channel it comes
// from, and the note of the trader for the model, if any.
type reanalysis struct {
	at      time.Time
	channel ui.Channel
	note    string
}

// Reanalyze asks the model again about pair now, rather than at the next close:
// on the next tick of the candle in progress (see OnPartialCandle), in the flow
// of the strategy, the rules of its analyses applied, the price the one of that
// candle. It implements ui.Reanalyzer, from the control panel of the dashboard
// or of Telegram. An analysis of the pair in progress answers the request, as a
// reanalysis already queued does, so the model is never asked twice at once;
// but a note, sent to the model in the context (Input.ReanalysisNote), is
// answered by the analysis after the one in progress, asked without it, and
// replaces the note of a reanalysis queued. Its result is notified, see
// SetNotifier, and its decision kept as the others, with the note. Refused in
// a backtest, and while the provider is rate limited.
func (t *Trader) Reanalyze(pair string, channel ui.Channel, note string) (ui.ReanalysisResponse, error) {
	if !t.cfg.Live {
		return ui.ReanalysisResponse{}, errors.New("the reanalysis asks the model on the candles of the market, not " +
			"in a backtest")
	}
	t.reMtx.Lock()
	defer t.reMtx.Unlock()
	now := t.now()
	if now.Before(t.paused) {
		return ui.ReanalysisResponse{}, fmt.Errorf("%s is rate limited for %s: ask again later", t.name,
			t.paused.Sub(now).Round(time.Second))
	}

	note = strings.TrimSpace(note)
	request := &reanalysis{at: now, channel: channel, note: note}
	_, running := t.running[pair]
	if running && note == "" {
		if t.running[pair] == nil {
			t.running[pair] = request
		}
		return ui.ReanalysisResponse{Status: ReanalysisRunning,
			Message: "An analysis of " + pair + " is in progress: its decision answers the request."}, nil
	}
	if queued, ok := t.pending[pair]; ok {
		message := "A reanalysis of " + pair + " is queued already"
		if note != "" {
			queued.channel, queued.note = channel, note
			message += ", now with your note"
		}
		return ui.ReanalysisResponse{Status: ReanalysisQueued,
			Message: message + ": the model is asked on the next tick of the candle in progress."}, nil
	}
	t.pending[pair] = request
	with := ""
	if note != "" {
		with = ", with a note"
	}
	log.Infof("[LLM] %s %s | reanalysis asked from %s%s", pair, t.cfg.Timeframe, channel, with)
	message := "The model is asked about " + pair + " on the next tick of the candle in progress."
	if running {
		message = "An analysis of " + pair + " is in progress, asked without your note: the model is asked " +
			"again with it on the next tick of the candle in progress."
	}
	return ui.ReanalysisResponse{Status: ReanalysisQueued, Message: message}, nil
}

// reanalysisPending reports whether a reanalysis of pair waits for an
// analysis.
func (t *Trader) reanalysisPending(pair string) bool {
	t.reMtx.Lock()
	defer t.reMtx.Unlock()
	_, pending := t.pending[pair]
	return pending
}

// beginAnalysis marks pair analyzed, and returns the reanalysis the analysis
// answers, nil for none.
func (t *Trader) beginAnalysis(pair string) *reanalysis {
	t.reMtx.Lock()
	defer t.reMtx.Unlock()
	request := t.pending[pair]
	delete(t.pending, pair)
	t.running[pair] = request
	return request
}

// endAnalysis marks the analysis of pair over, and notifies its result to the
// reanalysis it answers, if any: the one asked before it, or the one asked
// while it ran.
func (t *Trader) endAnalysis(pair, result string) {
	t.reMtx.Lock()
	request := t.running[pair]
	delete(t.running, pair)
	t.reMtx.Unlock()
	if request == nil {
		return
	}

	if result == "" {
		result = "no decision"
	}
	message := fmt.Sprintf("🔄 Reanalysis of %s %s, asked from %s: %s", pair, t.cfg.Timeframe, request.channel,
		result)
	log.Infof("[LLM] %s", message)
	if notify := t.notifier(); notify != nil {
		notify(message)
	}
}

// pausedUntil returns the end of the pause of a rate limit, see pause.
func (t *Trader) pausedUntil() time.Time {
	t.reMtx.Lock()
	defer t.reMtx.Unlock()
	return t.paused
}

// pause pauses the requests until until: the Retry-After of a rate limit.
func (t *Trader) pause(until time.Time) {
	t.reMtx.Lock()
	defer t.reMtx.Unlock()
	t.paused = until
}

// SetNotifier sets where the Trader sends its messages, eg: ui.Chart.Notify,
// which the dashboard and the Telegram of the controller show: the results of
// the reanalyses.
func (t *Trader) SetNotifier(notify func(message string)) {
	t.decisionsMtx.Lock()
	defer t.decisionsMtx.Unlock()
	t.notify = notify
}

func (t *Trader) notifier() func(string) {
	t.decisionsMtx.RLock()
	defer t.decisionsMtx.RUnlock()
	return t.notify
}

// describeDecision sums a decision up for a message: its action, its
// confidence, its outcome and its orders, its justification, or its error.
func describeDecision(d ui.Decision) string {
	if d.Action == "" {
		return "no decision: " + d.Error
	}
	text := fmt.Sprintf("%s %.2f, %s", d.Action, d.Confidence, d.Outcome)
	if len(d.OrderIDs) > 0 {
		ids := make([]string, len(d.OrderIDs))
		for i, id := range d.OrderIDs {
			ids[i] = "#" + strconv.FormatInt(id, 10)
		}
		text += " (orders " + strings.Join(ids, ", ") + ")"
	}
	if d.Error != "" {
		text += ": " + d.Error
	}
	return text + " | " + d.Reason
}
