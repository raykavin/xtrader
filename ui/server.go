package ui

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	log "github.com/sirupsen/logrus"
)

// router wires the API of the bot, which the dashboard proxies.
func (c *Chart) router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer, c.guard)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", c.handleHealth)
		r.Get("/pairs", c.handlePairs)
		r.Get("/events", c.handleEvents)
		r.Get("/controls", c.handleControls)
		r.Get("/controls/balance", c.handleControlBalance)
		r.Post("/controls/start", c.handleControlStart)
		r.Post("/controls/stop", c.handleControlStop)
		r.Post("/controls/order", c.handleControlOrder)
		r.Post("/controls/orders/{id}/move", c.handleControlMove)
		r.Post("/controls/orders/{id}/cancel", c.handleControlCancel)
		r.Post("/controls/reanalyze", c.handleReanalyze)
		r.Get("/supervision", c.handleSupervision)
		r.Post("/supervision/entries", c.handleSupervisionEntries)
		r.Post("/supervision/close", c.handleSupervisionClose)
		r.Post("/supervision/cancel", c.handleSupervisionCancel)
		r.Post("/supervision/protect", c.handleSupervisionProtect)
		r.Get("/decisions", c.handleDecisions)
		r.Get("/decisions/{id}", c.handleDecision)
		r.Get("/decisions/{id}/attachments/{index}", c.handleDecisionAttachment)
		r.Get("/{pair}/snapshot", c.handleSnapshot)
		r.Get("/{pair}/candles", c.handleCandles)
		r.Get("/{pair}/orders.csv", c.handleOrdersCSV)
	})

	return r
}

// writeJSON replies payload as JSON, or an error when it can't be encoded, eg:
// holding a NaN, rather than an empty answer.
func writeJSON(w http.ResponseWriter, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Errorf("ui: failed to encode the answer: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to encode the answer: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(append(data, '\n')); err != nil {
		log.Error(err)
	}
}

func (c *Chart) handleHealth(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	lastUpdate := c.lastUpdate
	c.mu.Unlock()

	if time.Since(lastUpdate) > time.Hour+10*time.Minute {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(lastUpdate.String()))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (c *Chart) handlePairs(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	pairs := c.pairs()
	c.mu.Unlock()
	writeJSON(w, PairsResponse{Pairs: pairs})
}

func (c *Chart) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, ok := c.snapshotOf(chi.URLParam(r, "pair"))
	if !ok {
		http.Error(w, "unknown pair", http.StatusNotFound)
		return
	}
	writeJSON(w, snapshot)
}

// snapshotOf returns the snapshot of the pair named name, see resolvePair. It
// is made holding c.mu, and written without it: a client reading it slowly,
// eg: a phone through ngrok, never holds the chart, nor the bot feeding it its
// candles and orders. The account is read before, see readCurrency.
func (c *Chart) snapshotOf(name string) (Snapshot, bool) {
	c.readCurrency()
	c.mu.Lock()
	defer c.mu.Unlock()
	pair, ok := c.resolvePair(name)
	if !ok {
		return Snapshot{}, false
	}
	return c.snapshot(pair), true
}

// resolvePair returns the pair of the chart named name: the same name, or
// else the one that differs only in case, eg: BTCUSDm for BTCUSDM, as the
// symbols of MT5 end in a lower case suffix while the links and Telegram
// commands may be upper case. The caller holds c.mu.
func (c *Chart) resolvePair(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if _, ok := c.candles[name]; ok {
		return name, true
	}
	for pair := range c.candles {
		if strings.EqualFold(pair, name) {
			return pair, true
		}
	}
	return "", false
}

func (c *Chart) handleOrdersCSV(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	pair, ok := c.resolvePair(chi.URLParam(r, "pair"))
	rows := c.orderRowsByPair(pair)
	c.mu.Unlock()

	if !ok {
		http.Error(w, "unknown pair", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment;filename=history_"+pair+".csv")

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"created_at", "status", "side", "id", "type", "quantity", "price", "total",
		"fee", "profit"})
	_ = writer.WriteAll(rows)
	writer.Flush()
	if err := writer.Error(); err != nil {
		log.Errorf("ui: failed writing csv: %v", err)
	}
}
