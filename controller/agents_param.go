package controller

import (
	"context"
	"fmt"
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
// ui.AgentsParamRequest: the params set on each agent of the filter in turn,
// all at once (see store.SetAgentParams), the ones refused on an agent told
// and the others done all the same, the agents out of the filter untouched;
// the agents changed applied (their restart pending, see
// AgentSettings.RestartPending); then, when asked, every agent of the filter
// enabled restarted, whatever its params did, the disabled ones left stopped.
// It goes on to the end once begun, whatever the request.
func (s *settings) handleAgentsParam(w http.ResponseWriter, r *http.Request) {
	var request ui.AgentsParamRequest
	if !decode(w, r, &request) {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	params, problem := paramsOf(request)
	if problem != "" {
		writeJSONError(w, http.StatusBadRequest, problem)
		return
	}
	changes := make([]store.ParamChange, 0, len(params))
	keys := make([]string, 0, len(params))
	for _, param := range params {
		if err := store.CheckParam(param.Key, param.Value); err != nil {
			s.fail(w, err)
			return
		}
		changes = append(changes, store.ParamChange{Name: param.Key, Value: param.Value})
		keys = append(keys, param.Key)
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
		if !inParamFilter(request, a) {
			result.Status = ui.ParamExcluded
			results[i] = result
			continue
		}
		sets, err := s.store.SetAgentParams(ctx, a.ID, changes, request.AddMissing)
		if err != nil {
			result.Status, result.Detail = ui.ParamFailed, userMessage(err)
			results[i] = result
			continue
		}
		for j, set := range sets {
			outcome := ui.AgentParamResult{Key: params[j].Key, Status: paramStatus[set.Status], Detail: set.Detail}
			if set.Status == store.ParamUpdated {
				outcome.Previous = set.Had
			}
			result.Params = append(result.Params, outcome)
		}
		result.Status, result.Detail = paramsStatus(sets)
		if len(sets) == 1 && sets[0].Status == store.ParamUpdated {
			result.Previous = sets[0].Had
		}
		if result.Status == ui.ParamUpdated || result.Status == ui.ParamAdded {
			changed++
		}
		results[i] = result
	}
	by := userOf(ctx)
	log.Infof("ui: the params %s set on %d of %d agents by %s (added where missing: %t, restart: %t, LLM only: %t, "+
		"enabled only: %t)", strings.Join(keys, ", "), changed, len(agents), by, request.AddMissing,
		request.RestartActive, request.LLMOnly, request.EnabledOnly)
	if changed > 0 {
		if err := s.manager.Reconcile(ctx); err != nil {
			log.Errorf("ui: %v", err)
		}
	}
	if request.RestartActive {
		s.restartEnabled(ctx, agents, results)
	}
	writeJSON(w, ui.AgentsParamResponse{Key: params[0].Key, Value: params[0].Value, Params: params,
		Results: results})
}

// paramsOf returns the params of the request, trimmed: its Params, or the one
// of its Key and Value without them; or why they are refused: none, more than
// ui.MaxAgentsParams, a key empty or repeated.
func paramsOf(request ui.AgentsParamRequest) ([]ui.AgentParam, string) {
	list := request.Params
	if len(list) == 0 {
		list = []ui.AgentParam{{Key: request.Key, Value: request.Value}}
	}
	if len(list) > ui.MaxAgentsParams {
		return nil, fmt.Sprintf("Use no máximo %d parâmetros de uma vez.", ui.MaxAgentsParams)
	}
	params := make([]ui.AgentParam, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, param := range list {
		key, value := strings.TrimSpace(param.Key), strings.TrimSpace(param.Value)
		switch {
		case key == "":
			return nil, "Informe a chave do parâmetro."
		case seen[key]:
			return nil, fmt.Sprintf("A chave %s está repetida: deixe uma só.", key)
		}
		seen[key] = true
		params = append(params, ui.AgentParam{Key: key, Value: value})
	}
	return params, ""
}

// paramsStatus returns the status of an agent of what was done with each of its
// params, see ui.AgentsParamResult: a value changed, a param added only, the
// values it had already, or none of them taken (why, with a single param).
func paramsStatus(sets []store.ParamSet) (status, detail string) {
	has := func(status string) bool {
		for _, set := range sets {
			if set.Status == status {
				return true
			}
		}
		return false
	}
	switch {
	case has(store.ParamUpdated):
		return ui.ParamUpdated, ""
	case has(store.ParamAdded):
		return ui.ParamAdded, ""
	case has(store.ParamUnchanged):
		return ui.ParamUnchanged, ""
	case len(sets) == 1:
		return ui.ParamSkipped, sets[0].Detail
	}
	return ui.ParamSkipped, ""
}

// inParamFilter reports whether the agent a is in the filter of the request,
// see ui.AgentsParamRequest.
func inParamFilter(request ui.AgentsParamRequest, a store.Agent) bool {
	return (!request.LLMOnly || a.Kind == "llm") && (!request.EnabledOnly || a.Enabled)
}

// restartEnabled restarts every agent of agents enabled and in the filter,
// restartParallel at a time, telling how in its result of results (by their
// index): restarted, failed, or left when the controller doesn't launch it.
// The disabled ones are left stopped, the ones out of the filter untouched.
func (s *settings) restartEnabled(ctx context.Context, agents []store.Agent, results []ui.AgentsParamResult) {
	slots := make(chan struct{}, restartParallel)
	var wg sync.WaitGroup
	for i, a := range agents {
		switch {
		case !a.Enabled || results[i].Status == ui.ParamExcluded:
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
