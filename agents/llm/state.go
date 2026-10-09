package llm

import (
	"fmt"
	"slices"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

// promptState is the prompt the Trader analyzes with, never changed once
// built: another takes its place at the start of an analysis (see
// refreshPrompt), so the analyses and the chart read one whole. It holds the
// instructions of the prompt, the indicators of its header with the candles
// they need, and its take profit.
type promptState struct {
	ref                ui.DecisionPrompt // names it in the records, empty when unknown
	system             string            // the instructions of the model, empty for the ones of the provider
	indicators         []string          // see Config.Indicators
	extras             []*extra          // the indicators of indicators other than the groups
	lookback           int               // candles the indicators need
	need               int               // the warmup the prompt needs
	takeProfitOptional bool              // see Config.TakeProfitOptional
	noTakeProfit       bool              // see Config.NoTakeProfit
	daily              dailyLimits       // see Config.DailyGain
	pendingOrders      []string          // see Config.PendingOrders
	drawings           bool              // see Config.Drawings
	smc                bool              // see Config.SMC
}

// newPromptState returns the state of the prompt ref, with the instructions
// system, empty for the ones of the provider, and the indicators, the take
// profit, the pending orders, the drawings and the context of cfg, which it
// validates.
func newPromptState(cfg Config, ref ui.DecisionPrompt, system string) (*promptState, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	extras, _ := parseExtras(cfg) // checked by validate
	lookback, need := sizing(cfg, extras)
	return &promptState{ref: ref, system: system, indicators: slices.Clone(cfg.Indicators), extras: extras,
		lookback: lookback, need: need, takeProfitOptional: cfg.TakeProfitOptional,
		noTakeProfit:  cfg.NoTakeProfit,
		daily:         dailyLimits{gain: cfg.DailyGain, loss: cfg.DailyLoss, currency: cfg.DailyCurrency},
		pendingOrders: slices.Clone(cfg.PendingOrders), drawings: cfg.Drawings, smc: cfg.SMC}, nil
}

// pends reports whether the prompt lets the model place the pending orders of
// kind, see Config.PendingOrders.
func (s *promptState) pends(kind string) bool {
	return slices.Contains(s.pendingOrders, kind)
}

// sizing returns the candles the indicators of cfg need, the groups and the
// extras, and the warmup: the candles talib needs not to fail, the recursive
// series (EMA, RSI, ATR, ADX, MACD) their seed washed out enough, the window
// of the model and the structure.
func sizing(cfg Config, extras []*extra) (lookback, need int) {
	lookback = max(cfg.TrendLen, cfg.EMASlowLen, cfg.RSILen+1, cfg.ATRLen+1, 2*cfg.ADXLen+1,
		cfg.MACDSlow+cfg.MACDSignal, cfg.BBLen, cfg.VolumeLen) + 1
	recursive := max(cfg.EMASlowLen, cfg.RSILen, cfg.ATRLen, 2*cfg.ADXLen, cfg.MACDSlow+cfg.MACDSignal)
	for _, e := range extras {
		lookback, recursive = max(lookback, e.lookback+1), max(recursive, e.seed)
	}
	return lookback, max(cfg.Candles, lookback+cfg.History, 5*recursive, cfg.StructureCandles)
}

// shows tells whether the group of indicators is drawn and sent, see
// Config.Indicators.
func (s *promptState) shows(group string) bool {
	return len(s.indicators) == 0 || slices.Contains(s.indicators, group)
}

// decisionPrompt returns the prompt of the records of the decisions taken
// with it, nil when unknown.
func (s *promptState) decisionPrompt() *ui.DecisionPrompt {
	if s.ref.Name == "" {
		return nil
	}
	ref := s.ref
	return &ref
}

// promptState returns the state of the prompt p with the Config of the
// Trader and its overrides, refusing a prompt the Trader can't analyze with:
// an indicator unknown, a take profit its rules don't take, eg: none with a
// min risk:reward, more history than the candles it keeps, or daily limits
// unreadable or in a currency other than the one of the account.
func (t *Trader) promptState(p Prompt) (*promptState, error) {
	content := p.Content
	if content == "" {
		content = defaultStrategy
	}
	if err := CheckPrompt(content); err != nil {
		return nil, err
	}
	if _, _, _, err := StrategyDailyLimits(content); err != nil {
		return nil, err
	}
	if err := checkHeader(content); err != nil {
		return nil, err
	}
	for _, line := range textDailyLines(content) {
		log.Warnf("[LLM] the prompt %s: the line %q is in the text, after the header: sent to the model, not a "+
			"daily limit the agent applies", describePrompt(p.Ref), line)
	}
	state, err := newPromptState(t.cfg.withPrompt(content, t.overrides), p.Ref, systemPrompt(content))
	if err != nil {
		return nil, err
	}
	if err := t.checkDailyCurrency(state.daily); err != nil {
		return nil, err
	}
	if t.warmup > 0 && state.need > t.warmup {
		return nil, fmt.Errorf("its indicators need %d candles of history, more than the %d the agent keeps: "+
			"raise its warmup (llm-warmup)", state.need, t.warmup)
	}
	return state, nil
}

// CheckPromptAccount checks the prompt the Trader analyzes with against the
// account of SetAccountReporter, set after the prompt was applied, eg: the one
// it was made with: daily limits in a currency other than the one of the
// account, when known, refuse it, as a prompt of SetPromptSource is. Once the
// reporter is set, before the bot runs.
func (t *Trader) CheckPromptAccount() error {
	st := t.state.Load()
	if err := t.checkDailyCurrency(st.daily); err != nil {
		return fmt.Errorf("llm: the prompt %s: %w", describePrompt(st.ref), err)
	}
	return nil
}

// SetPromptSource sets where the Trader reads the prompt it analyzes with, eg:
// the one selected for the agent in the controller, switched while it runs:
// at the start of each analysis, the one in progress finishing with the prompt
// it started with. A prompt it can't analyze with is refused, the previous one
// kept, and told to the source when it is a PromptReporter, as is the one
// applied. The provider must take the instructions with each request (see
// SystemPromptProvider). Before the bot runs.
func (t *Trader) SetPromptSource(source PromptSource) error {
	if _, ok := t.provider.(SystemPromptProvider); !ok {
		return fmt.Errorf("llm: %s takes no prompt with its requests", t.name)
	}
	t.prompts = source
	return nil
}

// Prompt returns the prompt the Trader analyzes with, empty when unknown: the
// instructions of its provider.
func (t *Trader) Prompt() ui.DecisionPrompt {
	return t.state.Load().ref
}

// refreshPrompt switches to the prompt of the source, when it changed, at the
// start of an analysis of df, and returns the state to analyze with: the
// indicators of the prompt computed on df when it switches, as the ones of the
// previous prompt were.
func (t *Trader) refreshPrompt(df *model.Dataframe) *promptState {
	current := t.state.Load()
	if t.prompts == nil {
		return current
	}
	known := current.ref
	if t.refused != (ui.DecisionPrompt{}) {
		known = t.refused
	}
	p, changed := t.prompts.Current(known)
	if !changed {
		return current
	}

	reporter, _ := t.prompts.(PromptReporter)
	next, err := t.promptState(p)
	if err != nil {
		t.refused = p.Ref
		log.Errorf("[LLM] the prompt %s refused, %s kept: %v", describePrompt(p.Ref), describePrompt(current.ref),
			err)
		if reporter != nil {
			reporter.ReportPrompt(p.Ref, err)
		}
		return current
	}
	t.refused = ui.DecisionPrompt{}
	t.state.Store(next)
	t.compute(df, next)
	log.Infof("[LLM] the prompt %s applied, after %s", describePrompt(next.ref), describePrompt(current.ref))
	if reporter != nil {
		reporter.ReportPrompt(p.Ref, nil)
	}
	return next
}

// describePrompt names the prompt of ref for the logs and the errors, eg:
// trend_following v3.
func describePrompt(ref ui.DecisionPrompt) string {
	switch {
	case ref.Name == "":
		return "of the provider"
	case ref.Version > 0:
		return fmt.Sprintf("%s v%d", ref.Name, ref.Version)
	}
	return ref.Name
}
