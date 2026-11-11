package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// backtestRunDetail is a run with its config, as it ran (copied: the params
// of the agent, its code and prompt versions, never a secret), and its report
// (agentapi.BacktestReport), see GET /api/backtests/runs/{id}.
type backtestRunDetail struct {
	ui.BacktestRunInfo
	Config json.RawMessage `json:"config"`
	Report json.RawMessage `json:"report,omitempty"`
}

// routes serves the backtests, each route needing the permissions it declares
// (see need).
func (b *Backtests) routes(r chi.Router) {
	view, manage := need(ui.PermBacktestsView), need(ui.PermBacktestsManage)
	r.With(view).Get("/backtests", b.handleList)
	r.With(manage).Post("/backtests", b.handleCreate)
	r.With(manage).Delete("/backtests/{id}", b.handleDelete)
	r.With(view).Get("/backtests/{id}/comparison", b.handleComparison)
	r.With(view).Get("/backtests/runs/{id}", b.handleRun)
	r.With(manage).Post("/backtests/runs/{id}/cancel", b.handleCancel)
}

// backtestRunInfo returns run as the dashboard shows it.
func backtestRunInfo(run store.BacktestRun) ui.BacktestRunInfo {
	info := ui.BacktestRunInfo{ID: run.ID, Agent: run.Agent, Kind: run.Kind, Segment: run.Segment,
		From: run.From.Unix(), To: run.To.Unix(), Status: run.Status, Processed: run.Processed, Total: run.Total,
		Error: run.Error, Pairs: []string{}, LLM: run.Kind == "llm"}
	if run.StartedAt != nil {
		info.StartedAt = run.StartedAt.Unix()
	}
	if run.FinishedAt != nil {
		info.FinishedAt = run.FinishedAt.Unix()
	}
	var snap backtestSnapshot
	if err := json.Unmarshal(run.Config, &snap); err == nil {
		info.Strategy, info.Timeframe = snap.Strategy, snap.Timeframe
		info.Pairs = append(info.Pairs, snap.Pairs...)
	}
	if len(run.Summary) > 0 {
		var summary ui.BacktestSummary
		if err := json.Unmarshal(run.Summary, &summary); err == nil {
			info.Summary = &summary
		}
	}
	return info
}

// backtestBatchInfo returns batch as the dashboard shows it, without its runs.
func backtestBatchInfo(batch store.BacktestBatch) ui.BacktestBatchInfo {
	info := ui.BacktestBatchInfo{ID: batch.ID, Name: batch.Name, From: batch.From.Unix(), To: batch.To.Unix(),
		ValidationShare: batch.ValidationShare, CreatedBy: batch.CreatedBy, CreatedAt: batch.CreatedAt.Unix(),
		Runs: []ui.BacktestRunInfo{}}
	_ = json.Unmarshal(batch.Assumptions, &info.Assumptions)
	return info
}

// handleList answers the batches with their runs, the last ones or the page
// asked for (see listQuery), the runs queued or running, every batch as a
// choice, the agents and their compatibility, and the limits, see
// ui.BacktestsResponse.
func (b *Backtests) handleList(w http.ResponseWriter, r *http.Request) {
	q, ok := listQuery(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var batches []store.BacktestBatch
	var page ui.PageInfo
	if q.Paged() {
		listed, err := b.store.FindBacktestBatches(ctx, q)
		if err != nil {
			b.fail(w, err)
			return
		}
		batches, page = listed.Rows, pageInfo(listed)
	} else {
		var err error
		if batches, err = b.store.BacktestBatches(ctx, backtestBatchesShown); err != nil {
			b.fail(w, err)
			return
		}
		page = wholePage(len(batches))
	}
	ids := make([]int64, 0, len(batches))
	for _, batch := range batches {
		ids = append(ids, batch.ID)
	}
	runs, err := b.store.BacktestRuns(ctx, ids, false)
	if err != nil {
		b.fail(w, err)
		return
	}
	running, err := b.store.CountActiveBacktestRuns(ctx)
	if err != nil {
		b.fail(w, err)
		return
	}
	choices, err := b.store.BacktestBatchChoices(ctx)
	if err != nil {
		b.fail(w, err)
		return
	}
	agents, err := b.store.Agents(ctx)
	if err != nil {
		b.fail(w, err)
		return
	}
	response := ui.BacktestsResponse{PageInfo: page, Batches: make([]ui.BacktestBatchInfo, 0, len(batches)),
		Running: int(running), Choices: make([]ui.BacktestBatchChoice, 0, len(choices)),
		Agents: make([]ui.BacktestCompatibility, 0, len(agents)), MaxPeriodDays: b.cfg.MaxPeriodDays,
		MaxRuns: b.cfg.MaxRuns, LLMMaxCalls: b.cfg.LLMMaxCalls, LLMMaxCost: b.cfg.LLMMaxCost}
	for _, batch := range batches {
		info := backtestBatchInfo(batch)
		for _, run := range runs {
			if run.BatchID == batch.ID {
				info.Runs = append(info.Runs, backtestRunInfo(run))
			}
		}
		response.Batches = append(response.Batches, info)
	}
	for _, choice := range choices {
		response.Choices = append(response.Choices, ui.BacktestBatchChoice{ID: choice.ID, Name: choice.Name,
			From: choice.From.Unix(), To: choice.To.Unix()})
	}
	for _, a := range agents {
		response.Agents = append(response.Agents, b.compatibility(a))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, response)
}

// handleComparison answers the comparison of the runs of the batch of the
// path, a row by agent: the page asked for (see listQuery) of the rows kept by
// the filters, sorted (see comparisonOf), with the rows hidden, what differs
// between all of them and the runs of the batch still to end, see
// ui.BacktestComparisonResponse; 404 for a batch unknown.
func (b *Backtests) handleComparison(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	q, ok := listQuery(w, r)
	if !ok {
		return
	}
	c, ok := comparisonOf(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	batch, err := b.store.BacktestBatch(ctx, id)
	if err != nil {
		b.fail(w, err)
		return
	}
	runs, err := b.store.BacktestRuns(ctx, []int64{id}, false)
	if err != nil {
		b.fail(w, err)
		return
	}
	infos := make([]ui.BacktestRunInfo, 0, len(runs))
	active := 0
	for _, run := range runs {
		infos = append(infos, backtestRunInfo(run))
		if !run.Final() {
			active++
		}
	}
	all := comparisonRows(infos)
	kept := filterRows(all, c)
	sortRows(kept, c)
	rows, page := pageSlice(kept, q)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, ui.BacktestComparisonResponse{PageInfo: page, Batch: backtestBatchInfo(batch), Rows: rows,
		Hidden: len(all) - len(kept), Differences: conditionDifferences(all), Active: active})
}

// handleCreate queues the batch of the body, see ui.BacktestRequest: 201
// with it, 400 for a batch refused.
func (b *Backtests) handleCreate(w http.ResponseWriter, r *http.Request) {
	var request ui.BacktestRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Pedido inválido.")
		return
	}
	batch, runs, err := b.plan(r.Context(), request, userOf(r.Context()), time.Now())
	if err != nil {
		b.fail(w, err)
		return
	}
	batch, runs, err = b.store.CreateBacktestBatch(r.Context(), batch, runs)
	if err != nil {
		b.fail(w, err)
		return
	}
	log.Infof("backtests: batch %d of %d runs asked by %s", batch.ID, len(runs), batch.CreatedBy)
	b.Wake()
	info := ui.BacktestBatchInfo{ID: batch.ID, Name: batch.Name, From: batch.From.Unix(), To: batch.To.Unix(),
		ValidationShare: batch.ValidationShare, CreatedBy: batch.CreatedBy, CreatedAt: batch.CreatedAt.Unix()}
	_ = json.Unmarshal(batch.Assumptions, &info.Assumptions)
	for _, run := range runs {
		info.Runs = append(info.Runs, backtestRunInfo(run))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(info)
}

// handleRun answers a run with its config and report.
func (b *Backtests) handleRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Execução inválida.")
		return
	}
	run, err := b.store.BacktestRun(r.Context(), id)
	if err != nil {
		b.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, backtestRunDetail{BacktestRunInfo: backtestRunInfo(run), Config: run.Config, Report: run.Result})
}

// handleCancel cancels a run, 409 when it ended already.
func (b *Backtests) handleCancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Execução inválida.")
		return
	}
	ok, err := b.Cancel(r.Context(), id)
	if err != nil {
		b.fail(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusConflict, "A execução já terminou.")
		return
	}
	log.Infof("backtests: run %d canceled by %s", id, userOf(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}

// handleDelete deletes a batch and its runs, 409 while some run.
func (b *Backtests) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Lote inválido.")
		return
	}
	if err := b.store.DeleteBacktestBatch(r.Context(), id); err != nil {
		b.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail answers err: 400 for a request refused, 404 for what doesn't exist,
// 409 for a batch busy, else 500, logged.
func (b *Backtests) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeJSONError(w, http.StatusBadRequest, userMessage(err))
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, userMessage(err))
	case errors.Is(err, store.ErrBacktestBusy):
		writeJSONError(w, http.StatusConflict, "O lote tem execuções na fila ou em andamento: cancele-as antes.")
	default:
		log.Errorf("backtests: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Não foi possível ler os backtests agora. "+
			"Tente de novo em instantes.")
	}
}
