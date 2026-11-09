package controller

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	log "github.com/sirupsen/logrus"
)

// statusWriter is a ResponseWriter telling the status of the answer written
// through it, flushed as it comes as the proxy of the agents flushes it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// handleBotControl proxies the stop (when stopped) or the start of the bot of
// an agent, from the dashboard (Parar bot, Iniciar bot), to the agent, see
// handleAgent, and records it once the agent took it, see recordBotControl.
func (d *Dashboard) handleBotControl(stopped bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		answer := &statusWriter{ResponseWriter: w}
		d.handleAgent(answer, r)
		if answer.status >= 200 && answer.status < 300 {
			d.recordBotControl(r.Context(), chi.URLParam(r, "agent"), stopped, userOf(r.Context()))
		}
	}
}

// recordBotControl records the bot of the agent of name stopped, or started,
// by by, as the agent took it (see store.AgentBotControl): the agent starts
// as it was left, whatever restarts its process. A failure is logged: the bot
// is stopped or started all the same.
func (d *Dashboard) recordBotControl(ctx context.Context, name string, stopped bool, by string) {
	if d.settings == nil {
		return
	}
	state := "started"
	if stopped {
		state = "stopped"
	}
	a, err := d.settings.store.AgentByName(ctx, name)
	if err == nil {
		err = d.settings.store.SetAgentBotStopped(ctx, a.ID, stopped, by)
	}
	if err != nil {
		log.Errorf("ui: the bot of the agent %s %s by %s, not recorded: %v", name, state, by, err)
		return
	}
	log.Infof("ui: the bot of the agent %s %s by %s", name, state, by)
}
