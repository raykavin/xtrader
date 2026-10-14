package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/store"
)

// The positions an agent discarded are written from its outbox, once, the
// same sent again changing nothing, and read back by it only; a discard
// without its pair, its side or its fill is refused for good, named, none of
// its batch written.
func TestInternalAPI_TheDiscardsOfTheAgent(t *testing.T) {
	c := newManaged(t)
	llm := c.agent(t, "llm", "llm", true)
	other := c.agent(t, "other", "llm", true, func(a *store.Agent) { a.Account.Comment = secret("nb:other") })
	token, err := c.manager.tokens.issue(llm.ID)
	require.NoError(t, err)
	otherToken, err := c.manager.tokens.issue(other.ID)
	require.NoError(t, err)
	srv := httptest.NewServer(c.manager.InternalHandler())
	t.Cleanup(srv.Close)

	var discards []model.Discard
	require.Equal(t, http.StatusOK, internalCall(t, srv, token, http.MethodGet, "/discards", nil,
		&discards).StatusCode)
	assert.Empty(t, discards)

	at := time.Date(2026, 10, 14, 0, 42, 57, 0, time.UTC)
	gold := model.Discard{Pair: "XAUUSDm", Side: model.SideTypeBuy, Quantity: 3, AfterID: 6001, At: at}
	var acked agentapi.OutboxResponse
	resp := internalCall(t, srv, token, http.MethodPost, "/outbox", agentapi.OutboxRequest{Records: []agentapi.Record{
		record(t, 1, agentapi.KindDiscard, gold.Key(), gold),
		record(t, 2, agentapi.KindDiscard, gold.Key(), gold), // sent again
	}}, &acked)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 2, acked.Acked)

	internalCall(t, srv, token, http.MethodGet, "/discards", nil, &discards)
	require.Len(t, discards, 1, "once")
	assert.True(t, discards[0].Same(gold))
	assert.Equal(t, model.SideTypeBuy, discards[0].Side)
	assert.InDelta(t, 3, discards[0].Quantity, 1e-9)
	assert.True(t, discards[0].At.Equal(at))
	internalCall(t, srv, otherToken, http.MethodGet, "/discards", nil, &discards)
	assert.Empty(t, discards, "never the discards of another")

	for name, bad := range map[string]model.Discard{
		"pair": {Side: model.SideTypeSell, Quantity: 1, AfterID: 7, At: at},
		"side": {Pair: "XAUUSDm", Side: "HOLD", Quantity: 1, AfterID: 7, At: at},
		"fill": {Pair: "XAUUSDm", Side: model.SideTypeSell, Quantity: 1, At: at},
	} {
		good := model.Discard{Pair: "BTCUSDm", Side: model.SideTypeSell, Quantity: 1, AfterID: 8, At: at}
		resp := internalCall(t, srv, token, http.MethodPost, "/outbox", agentapi.OutboxRequest{
			Records: []agentapi.Record{record(t, 3, agentapi.KindDiscard, good.Key(), good),
				record(t, 4, agentapi.KindDiscard, bad.Key(), bad)}}, nil)
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, name)
	}
	internalCall(t, srv, token, http.MethodGet, "/discards", nil, &discards)
	assert.Len(t, discards, 1, "the batches rolled back")
}
