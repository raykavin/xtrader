package controller

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/internal/recovery"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// restartParallel is how many agents the global parametrizer restarts at once:
// their stops waited together rather than one after the other.
const restartParallel = 8

// paramStatus is the outcome of the global parametrizer of the outcome of
// store.SetAgentParam.
var paramStatus = map[string]string{store.ParamUpdated: ui.ParamUpdated, store.ParamAdded: ui.ParamAdded,
	store.ParamUnchanged: ui.ParamUnchanged, store.ParamSkipped: ui.ParamSkipped}

// handleAgentsParam is the global parametrizer of the agents, see
// ui.AgentsParamRequest: the param set on each agent in turn (see
// store.SetAgentParam), one refused on an agent told and the others done all
// the same; the agents changed applied (their restart pending, see
// AgentSettings.RestartPending); then, when asked, every agent enabled
// restarted, whatever its param did, the disabled ones left stopped. It goes
// on to the end once begun, whatever the request.
func (s *settings) handleAgentsParam(w http.ResponseWriter, r *http.Request) {
	var request ui.AgentsParamRequest
	if !decode(w, r, &request) {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	key, value := strings.TrimSpace(request.Key), strings.TrimSpace(request.Value)
	if key == "" {
		writeJSONError(w, http.StatusBadRequest, "Informe a chave do parâmetro.")
		return
	}
	if err := store.CheckParam(key, value); err != nil {
		s.fail(w, err)
		return
	}
	agents, err := s.store.Agents(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	results := make([]ui.AgentsParamResult, len(agents))
	changed := 0
	for i, a := range agents {
		result := ui.AgentsParamResult{Agent: a.Name, Enabled: a.Enabled}
		set, err := s.store.SetAgentParam(ctx, a.ID, key, value, request.AddMissing)
		if err != nil {
			result.Status, result.Detail = ui.ParamFailed, userMessage(err)
		} else {
			result.Status, result.Detail = paramStatus[set.Status], set.Detail
			switch set.Status {
			case store.ParamUpdated:
				result.Previous = set.Had
				changed++
			case store.ParamAdded:
				changed++
			}
		}
		results[i] = result
	}
	by := userOf(ctx)
	log.Infof("ui: the param %s set on %d of %d agents by %s (added where missing: %t, restart: %t)", key, changed,
		len(agents), by, request.AddMissing, request.RestartActive)
	if changed > 0 {
		if err := s.manager.Reconcile(ctx); err != nil {
			log.Errorf("ui: %v", err)
		}
	}
	if request.RestartActive {
		s.restartEnabled(ctx, agents, results)
	}
	writeJSON(w, ui.AgentsParamResponse{Key: key, Value: value, Results: results})
}

// restartEnabled restarts every agent of agents enabled, restartParallel at a
// time, telling how in its result of results (by their index): restarted,
// failed, or left when the controller doesn't launch it. The disabled ones are
// left stopped.
func (s *settings) restartEnabled(ctx context.Context, agents []store.Agent, results []ui.AgentsParamResult) {
	slots := make(chan struct{}, restartParallel)
	var wg sync.WaitGroup
	for i, a := range agents {
		switch {
		case !a.Enabled:
			continue
		case !a.Launched:
			results[i].Restart = ui.ParamRestartLeft
			results[i].RestartDetail = "roda fora do controller: reinicie-o onde ele roda"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			var err error
			if p := recovery.Call("the restart of the agent "+a.Name, time.Time{}, func() {
				err = s.manager.Restart(ctx, a.Name)
			}); p != nil {
				log.Errorf("ui: %v\n%s", p, p.Stack)
				err = p
			}
			if err != nil {
				results[i].Restart, results[i].RestartDetail = ui.ParamRestartFailed, userMessage(err)
				return
			}
			results[i].Restart = ui.ParamRestarted
		}()
	}
	wg.Wait()
}
