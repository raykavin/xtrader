package store

import (
	"context"
)

// The outcomes of SetAgentParam on an agent.
const (
	ParamUpdated   = "updated"   // its value changed
	ParamAdded     = "added"     // added, the agent without it
	ParamUnchanged = "unchanged" // the value it had already, nothing written
	ParamSkipped   = "skipped"   // left as it was, see ParamSet.Detail
)

// ParamSet is what SetAgentParam did on an agent: its outcome (see
// ParamUpdated and the others), the value it had (Had, when it had the
// param), and why it was left (Detail, when skipped).
type ParamSet struct {
	Status string
	Had    string
	Detail string
}

// CheckParam checks the param name of value as the params of every agent are
// checked (see checkParams), the rules of the kind of an agent apart: the name
// of a flag, none of a field, the value a line of text.
func CheckParam(name, value string) error {
	return checkParam(name, value, "")
}

// SetAgentParam sets the param name to value on the agent of id, its row
// locked meanwhile: its other params and its settings kept, checked as
// UpdateAgent checks a change of its params (see checkAgent), eg: a param of
// a language model on an agent of another kind refused. It changes the agent
// holding the param, and the one without it when addMissing; nothing is
// written when the agent has the value already. Skipped: the agent without
// the param and not addMissing, or without a language model for one of it
// (see llmParam), which it can't hold. Its error refuses the change on this
// agent only: the caller goes on with the others.
func (s *Store) SetAgentParam(ctx context.Context, id int64, name, value string, addMissing bool) (ParamSet,
	error) {
	if err := CheckParam(name, value); err != nil {
		return ParamSet{}, err
	}
	var set ParamSet
	err := s.Transaction(ctx, func(ctx context.Context) error {
		agent, err := s.lockedAgent(ctx, id)
		if err != nil {
			return err
		}
		params := agent.AllParams()
		had, holds := params[name]
		switch {
		case holds && had == value:
			set = ParamSet{Status: ParamUnchanged, Had: had}
			return nil
		case !holds && !addMissing:
			set = ParamSet{Status: ParamSkipped, Detail: "sem o parâmetro"}
			return nil
		case !holds && llmParam(name) && agent.Kind != "llm":
			set = ParamSet{Status: ParamSkipped, Detail: "parâmetro só para agentes LLM"}
			return nil
		case !holds && llmParam(name) && agent.LLM == nil:
			set = ParamSet{Status: ParamSkipped, Detail: "parâmetro do LLM, que o agente ainda não tem configurado"}
			return nil
		case holds:
			set = ParamSet{Status: ParamUpdated, Had: had}
		default:
			set = ParamSet{Status: ParamAdded}
		}
		params[name] = value
		return s.updateLocked(ctx, agent, AgentUpdate{Params: params})
	})
	if err != nil {
		return ParamSet{}, err
	}
	return set, nil
}
