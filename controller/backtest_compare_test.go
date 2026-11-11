//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// summaryOf returns a summary of 50 trades, half of them won, changed by
// change.
func summaryOf(change func(*ui.BacktestSummary)) *ui.BacktestSummary {
	s := &ui.BacktestSummary{Currency: "USD", InitialBalance: 10000, FinalEquity: 10000, Trades: 50, Wins: 25,
		Losses: 25, WinRate: 0.5, MaxDrawdown: 100, MaxDrawdownPct: 0.01, Months: 3, PositiveMonths: 2}
	if change != nil {
		change(s)
	}
	return s
}

// compared are the runs of the vitest of the comparison (web/src/lib/
// backtest.test.ts): a with a degraded validation, b of a small sample, c an
// LLM still running on another asset.
func compared() []ui.BacktestRunInfo {
	ratio := func(v float64) *float64 { return &v }
	return []ui.BacktestRunInfo{
		{ID: 1, Agent: "a", Kind: "trend", Segment: store.SegmentFit, Status: store.BacktestSucceeded,
			Pairs: []string{"XAUUSDm"}, Summary: summaryOf(func(s *ui.BacktestSummary) {
				s.Net, s.ReturnOverDrawdown = 500, ratio(5)
			})},
		{ID: 2, Agent: "a", Kind: "trend", Segment: store.SegmentValidation, Status: store.BacktestSucceeded,
			Pairs: []string{"XAUUSDm"}, Summary: summaryOf(func(s *ui.BacktestSummary) {
				s.Net, s.ReturnOverDrawdown = -10, ratio(-0.1)
			})},
		{ID: 3, Agent: "b", Kind: "trend", Segment: store.SegmentFit, Status: store.BacktestSucceeded,
			Pairs: []string{"XAUUSDm"}, Summary: summaryOf(func(s *ui.BacktestSummary) {
				s.Net, s.ReturnOverDrawdown, s.Trades = 100, ratio(1), 10
			})},
		{ID: 4, Agent: "c", Kind: "llm", LLM: true, Segment: store.SegmentFit, Status: store.BacktestRunning,
			Pairs: []string{"BTCUSDm"}},
	}
}

// agentsOf returns the agents of rows, in their order.
func agentsOf(rows []ui.BacktestComparisonRow) []string {
	list := []string{}
	for _, row := range rows {
		list = append(list, row.Agent)
	}
	return list
}

// The comparison of a batch, as the dashboard made it: the fit and the
// validation of each agent paired, the degradation told; sorted by any
// column, the unknown last whatever the order; filtered by the sample and the
// LLMs; what differs between the rows told.
func TestComparison_SortFilterAndDifferences(t *testing.T) {
	rows := comparisonRows(compared())
	require.Equal(t, []string{"a", "b", "c"}, agentsOf(rows), "in the order of the runs")
	require.NotNil(t, rows[0].Validation)
	assert.EqualValues(t, 2, rows[0].Validation.ID)
	assert.True(t, rows[0].Degraded)
	assert.False(t, rows[1].Degraded)

	sorted := func(c comparison) []string {
		list := filterRows(comparisonRows(compared()), c)
		sortRows(list, c)
		return agentsOf(list)
	}
	assert.Equal(t, []string{"a", "b", "c"}, sorted(comparison{Sort: ui.CompareNet, LLM: true}), "descending")
	assert.Equal(t, []string{"b", "a", "c"}, sorted(comparison{Sort: ui.CompareNet, Ascending: true, LLM: true}),
		"the unknown last whatever the order")
	assert.Equal(t, []string{"a", "b", "c"}, sorted(comparison{Sort: ui.CompareAgent, Ascending: true, LLM: true}))
	assert.Equal(t, []string{"c", "b", "a"}, sorted(comparison{Sort: ui.CompareAgent, LLM: true}))
	assert.Equal(t, []string{"a", "b", "c"}, sorted(comparison{Sort: ui.CompareValidation, LLM: true}),
		"by the validation, the ones without one last, by agent")
	assert.Equal(t, []string{"a", "c"}, sorted(comparison{Sort: ui.CompareNet, MinTrades: 30, LLM: true}),
		"the sample: a run without its summary kept")
	assert.Equal(t, []string{"a", "b"}, sorted(comparison{Sort: ui.CompareNet}), "without the LLMs")
	assert.Equal(t, []string{"b", "a"}, sorted(comparison{Sort: ui.CompareTrades, Ascending: true}),
		"filtered and sorted")

	assert.Equal(t, []string{"ativos diferentes", "agentes LLM (não determinísticos) misturados aos demais"},
		conditionDifferences(rows))
	coarser := compared()
	for i := range coarser {
		if coarser[i].Summary != nil {
			coarser[i].Summary.ExecutionTimeframe = "1m"
		}
	}
	coarser[2].Summary.ExecutionTimeframe = "5m"
	assert.Contains(t, conditionDifferences(comparisonRows(coarser)), "execução em resoluções diferentes (1m, 5m)")
	assert.Empty(t, conditionDifferences(nil))
}

// A validation much worse than the fit: a positive fit with a negative
// validation, or the return over the drawdown of the validation under half
// of the fit's.
func TestDegradedValidation(t *testing.T) {
	ratio := func(v float64) *float64 { return &v }
	of := func(net float64, rod *float64) ui.BacktestSummary {
		return *summaryOf(func(s *ui.BacktestSummary) { s.Net, s.ReturnOverDrawdown = net, rod })
	}
	assert.True(t, degradedValidation(of(100, nil), of(-1, nil)))
	assert.True(t, degradedValidation(of(100, ratio(2)), of(10, ratio(0.5))))
	assert.False(t, degradedValidation(of(100, ratio(2)), of(60, ratio(1.5))))
	assert.False(t, degradedValidation(of(-50, ratio(-1)), of(-80, ratio(-2))), "a negative fit")
}

// The comparison asked: ?sort= of the columns, ?order= asc or desc, the agent
// ascending by default and the others descending, ?min_trades= from 0, ?llm=
// a boolean; 400 for one invalid.
func TestComparisonOf(t *testing.T) {
	parse := func(query string) (comparison, int) {
		w := httptest.NewRecorder()
		c, ok := comparisonOf(w, httptest.NewRequest(http.MethodGet, "/api/backtests/1/comparison?"+query, nil))
		if !ok {
			return c, w.Code
		}
		return c, http.StatusOK
	}
	c, _ := parse("")
	assert.Equal(t, comparison{Sort: ui.CompareRatio, LLM: true}, c)
	c, _ = parse("sort=agent")
	assert.True(t, c.Ascending, "the agent ascending")
	c, _ = parse("sort=agent&order=desc&min_trades=30&llm=false")
	assert.Equal(t, comparison{Sort: ui.CompareAgent, MinTrades: 30}, c)
	c, _ = parse("sort=net&order=asc")
	assert.True(t, c.Ascending)
	for _, bad := range []string{"sort=profit", "order=up", "min_trades=-1", "min_trades=x", "llm=talvez"} {
		_, code := parse(bad)
		assert.Equal(t, http.StatusBadRequest, code, bad)
	}
}

// The batches by pages and the comparison of one through the API: the page
// asked of the batches, the runs still to end and every batch as a choice;
// the rows of the comparison filtered and sorted before the pages, with the
// rows hidden; 404 for a batch unknown, 400 for a sort invalid.
func TestBacktests_PagesAndComparison(t *testing.T) {
	c := newManaged(t)
	c.dashboard.ServeBacktests(c.store, config.Agents{}, config.Backtest{Concurrency: 1, Dir: t.TempDir(),
		MaxPeriodDays: 30, MaxRuns: 10, LLMMaxCalls: 10, LLMMaxCost: 1})
	call, _ := settingsSession(t, c)
	ctx := context.Background()
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var last store.BacktestBatch
	for i := 1; i <= 3; i++ {
		batch, runs, err := c.store.CreateBacktestBatch(ctx, store.BacktestBatch{Name: fmt.Sprintf("lote %d", i),
			From: from, To: from.AddDate(0, 1, 0), Assumptions: json.RawMessage(`{"balance":10000}`),
			CreatedBy: "admin"}, []store.BacktestRun{
			{Agent: "zeta", Kind: "trend", Segment: store.SegmentFit, From: from, To: from.AddDate(0, 1, 0),
				Config: json.RawMessage(`{"pairs":["XAUUSDm"]}`)},
			{Agent: "alfa", Kind: "trend", Segment: store.SegmentFit, From: from, To: from.AddDate(0, 1, 0),
				Config: json.RawMessage(`{"pairs":["XAUUSDm"]}`)},
			{Agent: "llm", Kind: "llm", Segment: store.SegmentFit, From: from, To: from.AddDate(0, 1, 0),
				Config: json.RawMessage(`{"pairs":["XAUUSDm"]}`)},
		})
		require.NoError(t, err)
		if i < 3 {
			for _, run := range runs {
				summary, err := json.Marshal(summaryOf(func(s *ui.BacktestSummary) { s.Net = float64(run.ID) }))
				require.NoError(t, err)
				require.NoError(t, c.store.FinishBacktestRun(ctx, run.ID, store.BacktestSucceeded, "", summary, nil))
			}
		}
		last = batch
	}

	resp, body := call(http.MethodGet, "/api/backtests?page=2&per_page=2", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var page ui.BacktestsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &page))
	assert.Equal(t, ui.PageInfo{Total: 3, Page: 2, PerPage: 2, TotalPages: 2, HasPrev: true}, page.PageInfo)
	require.Len(t, page.Batches, 1)
	assert.Equal(t, "lote 1", page.Batches[0].Name, "the oldest on the last page")
	assert.Len(t, page.Batches[0].Runs, 3, "with its runs")
	assert.Equal(t, 3, page.Running, "the runs of the last batch, queued")
	require.Len(t, page.Choices, 3, "every batch")
	assert.Equal(t, last.ID, page.Choices[0].ID, "the newest first")
	assert.Equal(t, "lote 3", page.Choices[0].Name)
	resp, body = call(http.MethodGet, "/api/backtests", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.NoError(t, json.Unmarshal([]byte(body), &page))
	assert.Len(t, page.Batches, 3, "the last ones without a page")

	first := page.Batches[2].ID
	comparisonPage := func(id int64, query string) (ui.BacktestComparisonResponse, int) {
		t.Helper()
		resp, body := call(http.MethodGet, fmt.Sprintf("/api/backtests/%d/comparison%s", id, query), nil)
		var response ui.BacktestComparisonResponse
		if resp.StatusCode == http.StatusOK {
			require.NoError(t, json.Unmarshal([]byte(body), &response))
		}
		return response, resp.StatusCode
	}
	got, code := comparisonPage(first, "?page=1&per_page=2&sort=agent")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"alfa", "llm"}, agentsOf(got.Rows), "by agent")
	assert.Equal(t, 3, got.Total)
	assert.Equal(t, "lote 1", got.Batch.Name)
	assert.Empty(t, got.Batch.Runs, "the batch without its runs")
	assert.Zero(t, got.Active)
	got, _ = comparisonPage(first, "?page=2&per_page=2&sort=agent")
	assert.Equal(t, []string{"zeta"}, agentsOf(got.Rows), "the next page")
	got, _ = comparisonPage(first, "?page=1&per_page=2&sort=net&llm=false")
	assert.Equal(t, []string{"alfa", "zeta"}, agentsOf(got.Rows), "the net, descending, without the LLMs")
	assert.Equal(t, 1, got.Hidden)
	assert.Equal(t, 2, got.Total, "the rows kept")
	got, _ = comparisonPage(first, "?page=1&per_page=10&min_trades=60")
	assert.Empty(t, got.Rows, "none with 60 trades")
	assert.Equal(t, 3, got.Hidden)
	got, _ = comparisonPage(last.ID, "?page=1&per_page=10")
	assert.Equal(t, 3, got.Active, "its runs queued")
	assert.Len(t, got.Rows, 3, "kept without their summaries")
	got, _ = comparisonPage(first, "")
	assert.Len(t, got.Rows, 3, "all of them without a page")

	_, code = comparisonPage(999999, "?page=1")
	assert.Equal(t, http.StatusNotFound, code)
	_, code = comparisonPage(first, "?sort=lucro")
	assert.Equal(t, http.StatusBadRequest, code)
	_, code = comparisonPage(first, "?page=0")
	assert.Equal(t, http.StatusBadRequest, code)
}
