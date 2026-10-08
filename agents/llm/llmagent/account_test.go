package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/exchange"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/service"
	"github.com/raykavin/xtrader/ui"
)

// accountOf is a broker reporting an account in its currency.
type accountOf string

func (a accountOf) AccountSummary() (service.AccountSummary, bool) {
	return service.AccountSummary{Currency: string(a)}, true
}

func (a accountOf) ClosedProfit(model.Order, model.Order) (float64, bool) {
	return 0, false
}

// promptReports records what the agent tells the controller of its prompt.
type promptReports []error

func (r *promptReports) ReportPrompt(_ ui.DecisionPrompt, err error) {
	*r = append(*r, err)
}

// B22: live, the prompt the agent starts with is checked against the account
// once the broker reports it, and what came of it told to the controller as
// for a prompt switched: daily limits in another currency refuse it, the
// agent stopping, rather than running with every entry refused.
func TestCheckPromptAccount(t *testing.T) {
	provider, err := llm.NewProvider("openai", "gpt-5", "sk-test")
	require.NoError(t, err)
	usd := llm.Prompt{Ref: ui.DecisionPrompt{ID: 3, Name: "usd", Version: 1},
		Content: "# usd: x\n# Daily loss limit: 150 USD\n\nText."}
	trader, err := llm.New(llm.DefaultConfig(), provider, llm.WithPrompt(usd, llm.PromptOverrides{}))
	require.NoError(t, err, "the account unknown when it is made")

	reports := &promptReports{}
	trader.SetAccountReporter(accountOf("BRL"))
	err = checkPromptAccount(trader, reports)
	require.ErrorContains(t, err, "the daily limits are in USD, the account in BRL")
	require.Len(t, *reports, 1)
	require.Equal(t, err, (*reports)[0], "told to the controller")

	reports = &promptReports{}
	trader.SetAccountReporter(accountOf("USD"))
	require.NoError(t, checkPromptAccount(trader, reports))
	require.Equal(t, promptReports{nil}, *reports, "applied")
}

// accountBrokerOf is an accountBroker reporting an account in its currency,
// without a position open.
type accountBrokerOf struct{ accountOf }

func (accountBrokerOf) OpenPosition(string) (exchange.MT5OpenPosition, bool) {
	return exchange.MT5OpenPosition{}, false
}

// B22: live, once connected, the account of the broker is the one of the daily
// limits and of the dashboard, the prompt the agent starts with checked
// against it then, and only then told to the controller: refused for daily
// limits in another currency, the agent stops; applied otherwise, or without
// an account reported.
func TestStartAccount(t *testing.T) {
	provider, err := llm.NewProvider("openai", "gpt-5", "sk-test")
	require.NoError(t, err)
	usd := llm.Prompt{Ref: ui.DecisionPrompt{ID: 3, Name: "usd", Version: 1},
		Content: "# usd: x\n# Daily loss limit: 150 USD\n\nText."}
	newTrader := func() *llm.Trader {
		trader, err := llm.New(llm.DefaultConfig(), provider, llm.WithPrompt(usd, llm.PromptOverrides{}))
		require.NoError(t, err)
		return trader
	}

	reports := &promptReports{}
	trader := newTrader()
	_, err = startAccount(trader, accountBrokerOf{"BRL"}, reports)
	require.ErrorContains(t, err, "the daily limits are in USD, the account in BRL")
	require.Equal(t, promptReports{err}, *reports, "told to the controller")
	require.Error(t, trader.CheckPromptAccount(), "the account of the broker set")

	reports = &promptReports{}
	options, err := startAccount(newTrader(), accountBrokerOf{"USD"}, reports)
	require.NoError(t, err)
	require.Len(t, options, 2, "the positions and the account on the dashboard")
	require.Equal(t, promptReports{nil}, *reports, "applied")

	reports = &promptReports{}
	options, err = startAccount(newTrader(), accountOf("BRL"), reports)
	require.NoError(t, err, "not an account broker: no account")
	require.Empty(t, options)
	require.Equal(t, promptReports{nil}, *reports, "applied")
}
