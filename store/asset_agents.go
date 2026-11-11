package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/ui"
)

// The rules of the agents per asset at most, and the agents of a rule.
const (
	maxAssetRules      = 50
	maxAssetRuleAgents = 200
)

// AssetAgents are the agents allowed on assets, of all the agents, a single
// record, see ui.AssetAgentsSettings: none while never set (every agent on
// every asset). Revision grows with every change, see Revised.
type AssetAgents struct {
	ID        int16           `gorm:"primaryKey;autoIncrement:false"`
	Rules     json.RawMessage `gorm:"type:jsonb"`
	Revision  int64
	UpdatedBy string
	UpdatedAt time.Time
}

func (AssetAgents) TableName() string { return "asset_agents" }

// Settings returns the rules as the dashboard takes them.
func (a AssetAgents) Settings() ui.AssetAgentsSettings {
	settings := ui.AssetAgentsSettings{Rules: []ui.AssetAgentRule{}, UpdatedBy: a.UpdatedBy}
	_ = json.Unmarshal(a.Rules, &settings.Rules)
	if settings.Rules == nil {
		settings.Rules = []ui.AssetAgentRule{}
	}
	if !a.UpdatedAt.IsZero() {
		settings.UpdatedAt = a.UpdatedAt.Unix()
	}
	return settings
}

// Allowed returns the pairs of pairs the agent of name may open positions on
// by the rules, in their order, and the others (excluded): a pair of a rule
// not naming it.
func (a AssetAgents) Allowed(name string, pairs []string) (allowed, excluded []string) {
	rules := a.Settings().Rules
	for _, pair := range pairs {
		ruled := false
		for _, rule := range rules {
			if slices.Contains(rule.Pairs, pair) {
				ruled = !slices.Contains(rule.Agents, name)
				break
			}
		}
		if ruled {
			excluded = append(excluded, pair)
		} else {
			allowed = append(allowed, pair)
		}
	}
	return allowed, excluded
}

// AgentsOf returns the agents of the rule of pair, false without a rule.
func (a AssetAgents) AgentsOf(pair string) ([]string, bool) {
	for _, rule := range a.Settings().Rules {
		if slices.Contains(rule.Pairs, pair) {
			return rule.Agents, true
		}
	}
	return nil, false
}

// validateAssetAgents returns why settings are refused: at most
// maxAssetRules rules, each of an asset at least (as MT5 names it, each asset
// in one rule only) and of agents that exist (known, by name), each once.
func validateAssetAgents(settings ui.AssetAgentsSettings, known []string) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: agentes por ativo: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	if len(settings.Rules) > maxAssetRules {
		return invalid("use no máximo %d regras", maxAssetRules)
	}
	seen := map[string]bool{}
	for i, rule := range settings.Rules {
		n := i + 1
		switch {
		case len(rule.Pairs) == 0:
			return invalid("a regra %d não tem ativo", n)
		case len(rule.Agents) == 0:
			return invalid("a regra %d não tem agente: escolha quem pode operar os ativos dela", n)
		case len(rule.Agents) > maxAssetRuleAgents:
			return invalid("a regra %d tem mais de %d agentes", n, maxAssetRuleAgents)
		}
		for _, pair := range rule.Pairs {
			switch {
			case !exposureSymbol.MatchString(pair):
				return invalid("o ativo %q é inválido: use o nome do ativo no MT5, ex.: XAUUSDm", pair)
			case seen[pair]:
				return invalid("o ativo %s está em mais de uma regra", pair)
			}
			seen[pair] = true
		}
		agents := map[string]bool{}
		for _, name := range rule.Agents {
			switch {
			case !slices.Contains(known, name):
				return invalid("o agente %s não existe", name)
			case agents[name]:
				return invalid("o agente %s está repetido na regra %d", name, n)
			}
			agents[name] = true
		}
	}
	return nil
}

// AssetAgents returns the agents allowed on assets, false while never set.
func (s *Store) AssetAgents(ctx context.Context) (AssetAgents, bool, error) {
	var rules AssetAgents
	err := s.conn(ctx).Where("id = 1").Take(&rules).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AssetAgents{}, false, nil
	}
	return rules, err == nil, err
}

// SetAssetAgents sets the agents allowed on assets, validated against the
// agents (see validateAssetAgents), by by, its revision grown, and returns
// them.
func (s *Store) SetAssetAgents(ctx context.Context, settings ui.AssetAgentsSettings, by string) (AssetAgents,
	error) {
	var saved AssetAgents
	err := s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.lockAgents(ctx); err != nil {
			return err
		}
		var known []string
		if err := s.conn(ctx).Model(&Agent{}).Pluck("name", &known).Error; err != nil {
			return err
		}
		if err := validateAssetAgents(settings, known); err != nil {
			return err
		}
		rules := settings.Rules
		if rules == nil {
			rules = []ui.AssetAgentRule{}
		}
		data, err := json.Marshal(rules)
		if err != nil {
			return err
		}
		current, _, err := s.AssetAgents(ctx)
		if err != nil {
			return err
		}
		saved = AssetAgents{ID: 1, Rules: data, Revision: current.Revision + 1, UpdatedBy: by,
			UpdatedAt: time.Now()}
		return s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
			Create(&saved).Error
	})
	return saved, err
}

// renameInAssetAgents renames the agent old to name in the rules of the agents
// per asset, their revision grown when it was in one; in the transaction of
// the rename.
func (s *Store) renameInAssetAgents(ctx context.Context, old, name string) error {
	current, found, err := s.AssetAgents(ctx)
	if err != nil || !found {
		return err
	}
	settings := current.Settings()
	renamed := false
	for i, rule := range settings.Rules {
		for j, agent := range rule.Agents {
			if agent == old {
				settings.Rules[i].Agents[j] = name
				renamed = true
			}
		}
	}
	if !renamed {
		return nil
	}
	data, err := json.Marshal(settings.Rules)
	if err != nil {
		return err
	}
	return s.conn(ctx).Model(&AssetAgents{}).Where("id = 1").
		Updates(map[string]any{"rules": data, "revision": current.Revision + 1}).Error
}
