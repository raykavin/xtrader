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

// ParamChange is a param to set on an agent, see SetAgentParams.
type ParamChange struct {
	Name, Value string
}

// SetAgentParam sets the param name to value on the agent of id, see
// SetAgentParams.
func (s *Store) SetAgentParam(ctx context.Context, id int64, name, value string, addMissing bool) (ParamSet,
	error) {
	sets, err := s.SetAgentParams(ctx, id, []ParamChange{{Name: name, Value: value}}, addMissing)
	if err != nil {
		return ParamSet{}, err
	}
	return sets[0], nil
}

// SetAgentParams sets the params of changes on the agent of id at once, its
// row locked meanwhile: its other params and its settings kept, checked as
// UpdateAgent checks a change of its params (see checkAgent), eg: a param of
// a language model on an agent of another kind refused. Each param changes on
// the agent holding it, and on the one without it when addMissing; nothing is
// written when the agent has every value already. A param is skipped on the
// agent without it and not addMissing, or without a language model for one of
// it (see llmParam), which it can't hold. It returns what was done with each
// param, in their order. Its error refuses them all on this agent, nothing
// changed on it: the caller goes on with the others.
func (s *Store) SetAgentParams(ctx context.Context, id int64, changes []ParamChange, addMissing bool) ([]ParamSet,
	error) {
	for _, change := range changes {
		if err := CheckParam(change.Name, change.Value); err != nil {
			return nil, err
		}
	}
	var sets []ParamSet
	err := s.Transaction(ctx, func(ctx context.Context) error {
		agent, err := s.lockedAgent(ctx, id)
		if err != nil {
			return err
		}
		params := agent.AllParams()
		sets = make([]ParamSet, 0, len(changes))
		changed := false
		for _, change := range changes {
			name, value := change.Name, change.Value
			had, holds := params[name]
			var set ParamSet
			switch {
			case holds && had == value:
				set = ParamSet{Status: ParamUnchanged, Had: had}
			case !holds && !addMissing:
				set = ParamSet{Status: ParamSkipped, Detail: "sem o parâmetro"}
			case !holds && llmParam(name) && agent.Kind != "llm":
				set = ParamSet{Status: ParamSkipped, Detail: "parâmetro só para agentes LLM"}
			case !holds && llmParam(name) && agent.LLM == nil:
				set = ParamSet{Status: ParamSkipped, Detail: "parâmetro do LLM, que o agente ainda não tem configurado"}
			case holds:
				set = ParamSet{Status: ParamUpdated, Had: had}
			default:
				set = ParamSet{Status: ParamAdded}
			}
			if set.Status == ParamUpdated || set.Status == ParamAdded {
				params[name], changed = value, true
			}
			sets = append(sets, set)
		}
		if !changed {
			return nil
		}
		return s.updateLocked(ctx, agent, AgentUpdate{Params: params})
	})
	if err != nil {
		return nil, err
	}
	return sets, nil
}
