package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

// R7: a record the database refuses for its limits, eg: an index row too
// large (54000), fails the same way again: refused for good (422), rather
// than retried by the agent forever (503); the database out of reach never
// is.
func TestRefusedRecord_TheLimitsOfTheDatabase(t *testing.T) {
	pg := func(code string) error {
		return fmt.Errorf("controller: the order: %w", &pgconn.PgError{Code: code})
	}
	for _, code := range []string{"22001", "23505", "54000", "54001", "54011"} {
		assert.True(t, refusedRecord(pg(code)), code)
	}
	for _, code := range []string{"08006", "53100", "53300", "57014", "40001", "40P01"} {
		assert.False(t, refusedRecord(pg(code)), code)
	}
	assert.False(t, refusedRecord(errors.New("connection refused")))
}

// R7: what the outbox of an agent holds is as its last batch told: the records
// left once it is written, the first of them added when the agent tells (the
// last one written added before, maybe long), the ones of the batch included
// when it failed, none once sent whole.
func TestBacklogs_AsTheAgentTells(t *testing.T) {
	b := newBacklogs()
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	records := []agentapi.Record{{Seq: 1, At: at}, {Seq: 2, At: at.Add(time.Minute)}}
	next := at.Add(3 * time.Hour)

	b.report(7, agentapi.OutboxRequest{Records: records, Pending: 5, NextAt: next, SetApart: 1}, false)
	backlog, ok := b.of(7)
	require.True(t, ok)
	assert.Equal(t, Backlog{Pending: 5, Since: at, Failing: true, SetApart: 1}, backlog)

	b.report(7, agentapi.OutboxRequest{Records: records, Pending: 5, NextAt: next, SetApart: 1}, true)
	backlog, _ = b.of(7)
	assert.Equal(t, Backlog{Pending: 3, Since: next, SetApart: 1}, backlog,
		"the first one left added after the ones written")
	b.report(7, agentapi.OutboxRequest{Records: records, Pending: 5, SetApart: 1}, true)
	backlog, _ = b.of(7)
	assert.Equal(t, Backlog{Pending: 3, SetApart: 1}, backlog, "unknown to an agent of a version before")

	b.report(7, agentapi.OutboxRequest{Records: records, Pending: 2}, true)
	_, ok = b.of(7)
	assert.False(t, ok, "sent whole")
	b.report(8, agentapi.OutboxRequest{Records: records}, true)
	_, ok = b.of(8)
	assert.False(t, ok, "an agent of a version before it")
}

// R7: the records an outbox holds apart, failing again and again, are told
// while it holds them, none pending.
func TestBacklogs_TheRecordsHeldApart(t *testing.T) {
	b := newBacklogs()
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	records := []agentapi.Record{{Seq: 1, At: at}}
	b.report(7, agentapi.OutboxRequest{Records: records, Pending: 1, Held: 2}, true)
	backlog, ok := b.of(7)
	require.True(t, ok)
	assert.Equal(t, Backlog{Held: 2}, backlog)
	b.report(7, agentapi.OutboxRequest{Records: records, Pending: 1}, true)
	_, ok = b.of(7)
	assert.False(t, ok, "none held any more")
}

// R7: POST /outbox/check answers as POST /outbox would, none written; what an
// outbox holds reaches the settings of the dashboard.
func TestInternalAPI_OutboxCheckedAndItsBacklog(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	llm := c.agent(t, "llm", "llm", true)
	token, err := c.manager.tokens.issue(llm.ID)
	require.NoError(t, err)
	srv := httptest.NewServer(c.manager.InternalHandler())
	t.Cleanup(srv.Close)

	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	order := model.Order{ID: 1, ExchangeID: 99, Pair: "XAUUSDm", Side: model.SideTypeBuy, Type: model.OrderTypeMarket,
		Status: model.OrderStatusTypeFilled, Price: 4154.2, Quantity: 1, CreatedAt: at, UpdatedAt: at}
	good := record(t, 1, agentapi.KindOrder, "1", order)
	good.At = at
	bad := order
	bad.ID, bad.Side = 2, "HOLD"

	var answer agentapi.OutboxResponse
	resp := internalCall(t, srv, token, http.MethodPost, "/outbox/check",
		agentapi.OutboxRequest{Records: []agentapi.Record{good}, Pending: 9}, &answer)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, uint64(1), answer.Acked)
	orders, err := c.store.Orders(ctx, llm.ID, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, orders, "none written")
	assert.Nil(t, c.manager.Outbox(llm.ID), "a check tells nothing of the outbox")

	resp = internalCall(t, srv, token, http.MethodPost, "/outbox/check", agentapi.OutboxRequest{
		Records: []agentapi.Record{good, record(t, 2, agentapi.KindOrder, "2", bad)}}, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)

	resp = internalCall(t, srv, token, http.MethodPost, "/outbox", agentapi.OutboxRequest{
		Records: []agentapi.Record{good}, Pending: 4, NextAt: at.Add(time.Minute), SetApart: 1, Held: 2}, &answer)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	orders, err = c.store.Orders(ctx, llm.ID, 0, 10)
	require.NoError(t, err)
	assert.Len(t, orders, 1)
	want := &ui.AgentOutbox{Pending: 3, Since: at.Add(time.Minute).Unix(), SetApart: 1, Held: 2}
	assert.Equal(t, want, c.manager.Outbox(llm.ID))

	call, _ := settingsSession(t, c)
	listed, body := call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, listed.StatusCode)
	var list ui.AgentSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Agents, 1)
	assert.Equal(t, want, list.Agents[0].Outbox)

	internalCall(t, srv, token, http.MethodPost, "/outbox",
		agentapi.OutboxRequest{Records: []agentapi.Record{good}, Pending: 1}, nil)
	assert.Nil(t, c.manager.Outbox(llm.ID), "sent whole")
}
