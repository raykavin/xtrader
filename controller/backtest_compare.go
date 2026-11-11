package controller

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The columns the comparison of a batch sorts by, see sortValue.
var comparisonSorts = []string{ui.CompareAgent, ui.CompareNet, ui.CompareReturn, ui.CompareDrawdown,
	ui.CompareRatio, ui.CompareFactor, ui.CompareWinRate, ui.CompareTrades, ui.CompareMonths, ui.CompareValidation}

// comparison is how the rows of a batch are compared: sorted by Sort (one of
// comparisonSorts), ascending or not, without the agents of fewer trades than
// MinTrades (a run without its summary yet kept, to show its status) nor the
// LLM agents unless LLM.
type comparison struct {
	Sort      string
	Ascending bool
	MinTrades int
	LLM       bool
}

// comparisonOf returns the comparison the request asks for: ?sort= (ratio by
// default), ?order= (asc or desc; ascending by default for the agent only),
// ?min_trades= (0 by default) and ?llm= (true by default); it answers 400 for
// one invalid.
func comparisonOf(w http.ResponseWriter, r *http.Request) (comparison, bool) {
	values := r.URL.Query()
	c := comparison{Sort: ui.CompareRatio, LLM: true}
	if value := values.Get("sort"); value != "" {
		if !slices.Contains(comparisonSorts, value) {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Ordenação inválida: use %s.",
				strings.Join(comparisonSorts, ", ")))
			return comparison{}, false
		}
		c.Sort = value
	}
	c.Ascending = c.Sort == ui.CompareAgent
	switch values.Get("order") {
	case "":
	case "asc":
		c.Ascending = true
	case "desc":
		c.Ascending = false
	default:
		writeJSONError(w, http.StatusBadRequest, "Parâmetro order inválido: use asc ou desc.")
		return comparison{}, false
	}
	if value := values.Get("min_trades"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			writeJSONError(w, http.StatusBadRequest, "Parâmetro min_trades inválido: use um número a partir de 0.")
			return comparison{}, false
		}
		c.MinTrades = n
	}
	if value := values.Get("llm"); value != "" {
		llm, err := strconv.ParseBool(value)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "Parâmetro llm inválido: use true ou false.")
			return comparison{}, false
		}
		c.LLM = llm
	}
	return c, true
}

// degradedValidation reports whether the validation degraded from the fit: a
// positive fit with a negative validation, or the return per drawdown of the
// validation under half of the fit's.
func degradedValidation(fit, validation ui.BacktestSummary) bool {
	if fit.Net > 0 && validation.Net <= 0 {
		return true
	}
	a, b := fit.ReturnOverDrawdown, validation.ReturnOverDrawdown
	return a != nil && b != nil && *a > 0 && *b < *a/2
}

// comparisonRows returns the rows of the comparison of the runs of a batch, an
// agent each, in the order of its runs.
func comparisonRows(runs []ui.BacktestRunInfo) []ui.BacktestComparisonRow {
	var rows []ui.BacktestComparisonRow
	index := map[string]int{}
	for _, run := range runs {
		i, ok := index[run.Agent]
		if !ok {
			i = len(rows)
			index[run.Agent] = i
			rows = append(rows, ui.BacktestComparisonRow{Agent: run.Agent, Kind: run.Kind, Strategy: run.Strategy,
				Pairs: run.Pairs, LLM: run.LLM})
		}
		if run.Segment == store.SegmentValidation {
			rows[i].Validation = &run
		} else {
			rows[i].Fit = &run
		}
	}
	for i := range rows {
		fit, validation := rows[i].Fit, rows[i].Validation
		rows[i].Degraded = fit != nil && validation != nil && fit.Summary != nil && validation.Summary != nil &&
			degradedValidation(*fit.Summary, *validation.Summary)
	}
	return rows
}

// sortValue returns the value of row the column key sorts by, false when it
// has none, sorted last whatever the order: a number, or the name of the agent
// for the agent.
func sortValue(row ui.BacktestComparisonRow, key string) (float64, bool) {
	var s *ui.BacktestSummary
	if row.Fit != nil {
		s = row.Fit.Summary
	}
	if s == nil && key != ui.CompareValidation {
		return 0, false
	}
	switch key {
	case ui.CompareNet:
		return s.Net, true
	case ui.CompareReturn:
		return s.Return, true
	case ui.CompareDrawdown:
		return -s.MaxDrawdownPct, true // the shallowest first, descending
	case ui.CompareRatio:
		if s.ReturnOverDrawdown == nil {
			return 0, false
		}
		return *s.ReturnOverDrawdown, true
	case ui.CompareFactor:
		if s.ProfitFactor == nil {
			return 0, false
		}
		return *s.ProfitFactor, true
	case ui.CompareWinRate:
		return s.WinRate, s.Trades > 0
	case ui.CompareTrades:
		return float64(s.Trades), true
	case ui.CompareMonths:
		if s.Months == 0 {
			return 0, false
		}
		return float64(s.PositiveMonths) / float64(s.Months), true
	case ui.CompareValidation:
		if row.Validation == nil || row.Validation.Summary == nil ||
			row.Validation.Summary.ReturnOverDrawdown == nil {
			return 0, false
		}
		return *row.Validation.Summary.ReturnOverDrawdown, true
	}
	return 0, false
}

// byAgent compares the names of two agents, the case ignored first.
func byAgent(a, b string) int {
	return cmp.Or(strings.Compare(strings.ToLower(a), strings.ToLower(b)), strings.Compare(a, b))
}

// sortRows sorts rows by c.Sort, the ones without a value last, by agent
// between them; equal ones kept in their order.
func sortRows(rows []ui.BacktestComparisonRow, c comparison) {
	slices.SortStableFunc(rows, func(a, b ui.BacktestComparisonRow) int {
		var order int
		if c.Sort == ui.CompareAgent {
			order = byAgent(a.Agent, b.Agent)
		} else {
			x, okX := sortValue(a, c.Sort)
			y, okY := sortValue(b, c.Sort)
			switch {
			case !okX && !okY:
				return byAgent(a.Agent, b.Agent)
			case !okX:
				return 1
			case !okY:
				return -1
			}
			order = cmp.Compare(x, y)
		}
		if c.Ascending {
			return order
		}
		return -order
	})
}

// filterRows returns the rows c keeps: the LLM agents unless c.LLM; the
// agents of c.MinTrades trades at least, a run without its summary kept.
func filterRows(rows []ui.BacktestComparisonRow, c comparison) []ui.BacktestComparisonRow {
	kept := []ui.BacktestComparisonRow{}
	for _, row := range rows {
		if !c.LLM && row.LLM {
			continue
		}
		if row.Fit != nil && row.Fit.Summary != nil && row.Fit.Summary.Trades < c.MinTrades {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

// conditionDifferences returns what differs between the rows compared, eg:
// the assets: the batch fixes the period, the capital and the costs, the
// agents may trade other pairs.
func conditionDifferences(rows []ui.BacktestComparisonRow) []string {
	differences := []string{}
	pairs, timeframes, executions := map[string]bool{}, map[string]bool{}, map[string]bool{}
	llm, others := false, false
	for _, row := range rows {
		sorted := slices.Clone(row.Pairs)
		slices.Sort(sorted)
		pairs[strings.Join(sorted, ";")] = true
		timeframe := ""
		if row.Fit != nil {
			timeframe = row.Fit.Timeframe
		}
		timeframes[timeframe] = true
		for _, run := range []*ui.BacktestRunInfo{row.Fit, row.Validation} {
			if run != nil && run.Summary != nil && run.Summary.ExecutionTimeframe != "" {
				executions[run.Summary.ExecutionTimeframe] = true
			}
		}
		if row.LLM {
			llm = true
		} else {
			others = true
		}
	}
	if len(pairs) > 1 {
		differences = append(differences, "ativos diferentes")
	}
	if len(timeframes) > 1 {
		differences = append(differences, "timeframes diferentes")
	}
	if len(executions) > 1 {
		list := make([]string, 0, len(executions))
		for e := range executions {
			list = append(list, e)
		}
		slices.Sort(list)
		differences = append(differences, fmt.Sprintf("execução em resoluções diferentes (%s)",
			strings.Join(list, ", ")))
	}
	if llm && others {
		differences = append(differences, "agentes LLM (não determinísticos) misturados aos demais")
	}
	return differences
}
