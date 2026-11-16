package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// errDryRun rolls the writes of a preview of an import back: nothing applied.
var errDryRun = errors.New("the preview of an import")

// configRefusal is a configuration file refused as a whole, its text told the
// user.
type configRefusal string

func (r configRefusal) Error() string { return string(r) }

// importReason is the reason recorded of the changes of an import.
const importReason = "importação de arquivo de configuração"

// importItem is an item of a configuration file to import: its preview and its
// writes, in the transaction of the import (nil for an item refused, or as it
// is: nothing to write).
type importItem struct {
	section  string
	preview  ui.ConfigPreviewItem
	apply    func(ctx context.Context) error
	repeated bool // its key taken by an item before it in the file: told on the preview only
}

// refuse refuses the item, why told the user.
func (it *importItem) refuse(why string) {
	it.preview.Status, it.preview.Error, it.apply = ui.ConfigStatusInvalid, sentence(why), nil
}

// warn tells what importing the item does beyond its fields.
func (it *importItem) warn(warning string) {
	it.preview.Warnings = append(it.preview.Warnings, warning)
}

// configImporter imports a configuration file, or previews it, for the user
// of the context it is made in: each item compared with the settings as they
// stand, checked, and written by the store functions the screens call.
type configImporter struct {
	s        *settings
	file     ui.ConfigFile
	secrets  configSecretValues     // opened: empty when locked or none
	current  ui.ConfigFile          // the settings as they stand
	stored   configSecretValues     // their secrets, when the file's are opened
	records  map[string]store.Agent // the agents, by name
	accounts map[string]store.MT5Account
	roles    map[string]store.Role
	users    map[int64]store.TelegramUser
	manages  func(section string) bool
	by       string
	userID   *int64
	seen     map[string]bool // the items of the file, by section and key: a repeated one refused

	effects []string                             // the keys of after, in their order
	after   map[string]func(ctx context.Context) // what applies the items written, once committed
}

// effect adds fn to what applies the items written once committed, under key:
// once only.
func (imp *configImporter) effect(key string, fn func(ctx context.Context)) {
	if _, ok := imp.after[key]; ok {
		return
	}
	imp.effects = append(imp.effects, key)
	imp.after[key] = fn
}

// item returns the item of key of section, labelled label: refused when the
// user doesn't manage its section, or repeated in the file.
func (imp *configImporter) item(section, key, label string) *importItem {
	it := &importItem{section: section, preview: ui.ConfigPreviewItem{Key: key, Label: label}}
	area, _ := areaOf(section)
	switch id := section + "\x00" + key; {
	case strings.TrimSpace(key) == "":
		it.refuse("Item sem nome no arquivo")
	case imp.seen[id]:
		it.refuse("Repetido no arquivo: só o primeiro é importado")
		it.repeated = true
	case !imp.manages(section):
		imp.seen[id] = true
		it.refuse("Sem permissão para alterar " + area.label)
	default:
		imp.seen[id] = true
	}
	return it
}

// compare sets the state of it as next stands to before (exists false: none
// yet), the secrets of the file differing from the ones kept when secrets,
// and reports whether it has anything to write.
func (imp *configImporter) compare(it *importItem, exists bool, before, next any, secrets bool) bool {
	if it.preview.Status == ui.ConfigStatusInvalid {
		return false
	}
	if !exists {
		it.preview.Status = ui.ConfigStatusNew
		return true
	}
	changes := fieldChanges(before, next)
	if secrets {
		changes = append(changes, "segredos")
	}
	if len(changes) == 0 {
		it.preview.Status = ui.ConfigStatusSame
		return false
	}
	it.preview.Status, it.preview.Changes = ui.ConfigStatusChanged, changes
	return true
}

// mt5Change returns who changes the accounts of MT5 in the import, and why.
func (imp *configImporter) mt5Change() store.MT5Change {
	return store.MT5Change{By: imp.by, UserID: imp.userID, Reason: importReason}
}

// dailyChange returns who changes the daily limits in the import, and why.
func (imp *configImporter) dailyChange() store.DailyChange {
	if sup := imp.s.supervision; sup != nil {
		return sup.daily.dailyChange(imp.by, imp.userID, importReason)
	}
	return store.DailyChange{Day: time.Now().Format(time.DateOnly), Via: store.ViaSettings, By: imp.by,
		UserID: imp.userID, Reason: importReason}
}

// dailyPlan is what a change of daily limits does now, see
// dailyLimiter.preview, to settle once saved.
type dailyPlan struct {
	preview ui.DailyLimitPreview
	guard   *store.DailyGuard
}

// planDaily returns what setting limits of the agent a (all the agents when
// nil) does now, warned on it; nil when it can't be told (no supervisor).
func (imp *configImporter) planDaily(ctx context.Context, it *importItem, a *store.Agent,
	limits ui.DailyLimitSettings) *dailyPlan {
	sup := imp.s.supervision
	if sup == nil {
		return nil
	}
	preview, guard, err := sup.daily.preview(ctx, a, limits)
	if err != nil {
		log.Warnf("ui: the effect of the daily limits imported: %v", err)
		it.warn("O efeito do limite diário não pôde ser calculado agora: ele vale na próxima avaliação.")
		return nil
	}
	if preview.Effect != ui.DailyEffectNone {
		it.warn(preview.Message)
	}
	return &dailyPlan{preview: preview, guard: guard}
}

// settleDaily settles the limits of the agent a (all the agents when nil) once
// committed, as plan told of them: the guard released, or the limits
// evaluated again.
func (imp *configImporter) settleDaily(a *store.Agent, plan *dailyPlan, change store.DailyChange) {
	sup := imp.s.supervision
	if sup == nil {
		return
	}
	key := "daily"
	if a != nil {
		key += ":" + a.Name
	}
	imp.effect(key, func(ctx context.Context) {
		if plan == nil {
			sup.daily.Reevaluate()
			return
		}
		if err := sup.daily.settle(ctx, a, plan.preview, plan.guard, change); err != nil {
			log.Errorf("ui: the daily limits imported: %v", err)
		}
	})
}

// refreshRuntimes publishes the runtime of every agent again once committed:
// the limits and the news imported applied at once.
func (imp *configImporter) refreshRuntimes() {
	imp.effect("runtime", func(ctx context.Context) {
		agents, err := imp.s.store.Agents(ctx)
		if err != nil {
			log.Warnf("ui: the configuration imported not sent to the agents: %v", err)
			return
		}
		for _, a := range agents {
			if err := imp.s.manager.RefreshRuntime(ctx, a.Name); err != nil {
				log.Warnf("ui: the configuration imported not sent to the agent %s: %v", a.Name, err)
			}
		}
	})
}

// trigger applies the settings the manager reads once committed: the prompts,
// the accounts of MT5 and the bot of Telegram.
func (imp *configImporter) trigger() {
	imp.effect("trigger", func(context.Context) { imp.s.manager.Trigger() })
}

// reconcile launches and stops the agents as imported once committed.
func (imp *configImporter) reconcile() {
	imp.effect("reconcile", func(ctx context.Context) {
		if err := imp.s.manager.Reconcile(ctx); err != nil {
			log.Errorf("ui: %v", err)
		}
	})
}

// nonEmpty returns a pointer to value, nil for "": a secret or an optional
// field written only when set.
func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// nonZero returns a pointer to value, nil for 0.
func nonZero[T int | int64](value T) *T {
	if value == 0 {
		return nil
	}
	return &value
}

// differs reports whether the secret of a file, when set, differs from the
// one kept.
func differs(file, kept string) bool {
	return file != "" && file != kept
}

// prompts returns the prompts of the file to import.
func (imp *configImporter) prompts() []*importItem {
	current := map[string]ui.ConfigPrompt{}
	for _, p := range imp.current.Prompts {
		current[p.Name] = p
	}
	items := []*importItem{}
	for _, p := range imp.file.Prompts {
		it := imp.item(ui.ConfigSectionPrompts, p.Name, p.Name)
		items = append(items, it)
		before, exists := current[p.Name]
		if !imp.compare(it, exists, before, p, false) {
			continue
		}
		it.apply = func(ctx context.Context) error {
			if !exists {
				_, err := imp.s.store.CreatePrompt(ctx, p.Name, p.Description, p.Content, "web:"+imp.by)
				return err
			}
			prompt, err := imp.s.store.PromptByName(ctx, p.Name)
			if err != nil {
				return err
			}
			if _, err := imp.s.store.UpdatePrompt(ctx, prompt.ID, store.PromptEdit{Description: &p.Description,
				Content: &p.Content}, "web:"+imp.by); err != nil {
				return err
			}
			imp.trigger() // the agents using it apply it from their next analysis
			return nil
		}
	}
	return items
}

// mt5Accounts returns the accounts of MT5 of the file to import: a new one
// inactive, as created on its screen; one active never moved (its login, its
// server or its bridge), its terminal switched on its screen only.
func (imp *configImporter) mt5Accounts() []*importItem {
	current := map[string]ui.ConfigMT5Account{}
	for _, a := range imp.current.MT5Accounts {
		current[a.Name] = a
	}
	items := []*importItem{}
	for _, a := range imp.file.MT5Accounts {
		it := imp.item(ui.ConfigSectionMT5Accounts, a.Name, fmt.Sprintf("%s (%d · %s)", a.Name, a.Login, a.Server))
		items = append(items, it)
		secrets := imp.secrets.MT5Accounts[a.Name]
		kept := imp.stored.MT5Accounts[a.Name]
		it.preview.Secrets = secrets.Password != "" || secrets.BridgeToken != ""
		before, exists := current[a.Name]
		if !imp.compare(it, exists, before, a, differs(secrets.Password, kept.Password) ||
			differs(secrets.BridgeToken, kept.BridgeToken)) {
			continue
		}
		record := imp.accounts[a.Name]
		if exists && record.Active && (a.Login != before.Login || !strings.EqualFold(a.Server, before.Server) ||
			a.BridgeURL != before.BridgeURL) {
			it.refuse("A conta está ativa no terminal: troque o login, o servidor ou a bridge dela em " +
				"Configurações › Contas MT5")
			continue
		}
		if !exists {
			it.warn("Criada inativa: ative-a em Configurações › Contas MT5 para o terminal entrar nela.")
		}
		input := store.MT5AccountInput{Name: &a.Name, Login: &a.Login, Server: &a.Server, BridgeURL: &a.BridgeURL,
			AllowReal: &a.AllowReal, ReadOnly: &a.ReadOnly, Password: nonEmpty(secrets.Password),
			BridgeToken: nonEmpty(secrets.BridgeToken)}
		it.apply = func(ctx context.Context) error {
			if !exists {
				_, err := imp.s.store.CreateMT5Account(ctx, input, false, "", imp.mt5Change())
				return err
			}
			_, next, err := imp.s.store.UpdateMT5Account(ctx, record.ID, input, imp.mt5Change())
			if err != nil {
				return err
			}
			if next.Active { // its switches, or its password, given to its terminal
				imp.effect("mt5", func(ctx context.Context) { imp.s.mt5.reconcile(ctx) })
			}
			imp.trigger()
			return nil
		}
	}
	return items
}

// accountByName returns the account of MT5 of name, as the transaction of ctx
// reads it: one created by the import too.
func (imp *configImporter) accountByName(ctx context.Context, name string) (store.MT5Account, error) {
	accounts, err := imp.s.store.MT5Accounts(ctx)
	if err != nil {
		return store.MT5Account{}, err
	}
	for _, account := range accounts {
		if account.Name == name {
			return account, nil
		}
	}
	return store.MT5Account{}, fmt.Errorf("%w: a conta MT5 %s não existe: importe-a junto, ou crie-a antes",
		store.ErrNotFound, name)
}

// agentWarnings warns on what importing the agent a does beyond its fields,
// as it stands (before, exists false: none yet).
func (imp *configImporter) agentWarnings(it *importItem, a, before ui.ConfigAgent, exists bool) {
	operates := "começa a operar na conta"
	if a.Mode != "live" {
		operates = "começa a rodar em paper"
	}
	switch {
	case !exists && a.Enabled:
		it.warn("Entra ligado: " + operates + " assim que importado.")
	case !exists:
		it.warn("Entra desligado.")
	case a.Enabled && !before.Enabled:
		it.warn("Será ligado: " + operates + ".")
	case !a.Enabled && before.Enabled:
		it.warn("Será desligado: o agente para.")
	}
	if !exists || !a.Enabled || !imp.s.manager.State(a.Name).Running {
		return
	}
	live := []string{"ligado", "painel de trading", "controle via Telegram", "reanálise", "prompt", "proteção",
		"limite diário", "segredos"} // applied while it runs
	if slices.ContainsFunc(it.preview.Changes, func(change string) bool {
		return !slices.ContainsFunc(live, func(l string) bool { return strings.HasPrefix(change, l) })
	}) {
		it.warn("Está rodando: as mudanças valem quando ele for reiniciado.")
	}
}

// agents returns the agents of the file to import: on or off as in the file;
// one of another kind than the agent of its name refused, and the account of
// MT5 of one running or holding positions never switched.
func (imp *configImporter) agents(ctx context.Context) []*importItem {
	current := map[string]ui.ConfigAgent{}
	for _, a := range imp.current.Agents {
		current[a.Name] = a
	}
	items := []*importItem{}
	for _, a := range imp.file.Agents {
		it := imp.item(ui.ConfigSectionAgents, a.Name, a.Name)
		items = append(items, it)
		secrets, kept := imp.secrets.Agents[a.Name], imp.stored.Agents[a.Name]
		if a.MT5Account != "" {
			secrets.BridgeToken = "" // the account's
		}
		it.preview.Secrets = secrets.LLMAPIKey != "" || secrets.BridgeToken != ""
		before, exists := current[a.Name]
		if it.preview.Status != ui.ConfigStatusInvalid {
			switch {
			case a.Kind == "llm" && a.LLM == nil:
				it.refuse("Um agente LLM precisa do LLM (provedor e modelo)")
			case a.Kind != "llm" && (a.LLM != nil || a.Prompt != ""):
				it.refuse("Só agentes LLM têm LLM e prompt")
			case exists && a.Kind != before.Kind:
				it.refuse(fmt.Sprintf("O agente já existe com o tipo %s: o tipo de um agente não muda", before.Kind))
			}
		}
		a.DailyLimit = limitOnly(a.DailyLimit)
		if !imp.compare(it, exists, before, a, differs(secrets.LLMAPIKey, kept.LLMAPIKey) ||
			differs(secrets.BridgeToken, kept.BridgeToken)) {
			continue
		}
		record := imp.records[a.Name]
		if exists && a.MT5Account != before.MT5Account {
			if why := imp.s.mt5.unlinkable(ctx, record); why != "" {
				it.refuse(why)
				continue
			}
		}
		imp.agentWarnings(it, a, before, exists)
		var daily *dailyPlan
		if exists && !sameFields(a.DailyLimit, before.DailyLimit) {
			daily = imp.planDaily(ctx, it, &record, a.DailyLimit)
		}
		it.apply = func(ctx context.Context) error {
			if !exists {
				return imp.createAgent(ctx, a, secrets)
			}
			return imp.updateAgent(ctx, a, before, record, secrets, daily)
		}
	}
	return items
}

// agentRefs returns the IDs of the account of MT5 and of the prompt of the
// agent a by their names, as the transaction of ctx reads them, nil for none.
func (imp *configImporter) agentRefs(ctx context.Context, a ui.ConfigAgent) (account *store.MT5Account,
	prompt *int64, err error) {
	if a.MT5Account != "" {
		found, err := imp.accountByName(ctx, a.MT5Account)
		if err != nil {
			return nil, nil, err
		}
		account = &found
	}
	if a.Prompt != "" {
		found, err := imp.s.store.PromptByName(ctx, a.Prompt)
		if err != nil {
			return nil, nil, err
		}
		prompt = &found.ID
	}
	return account, prompt, nil
}

// importedParams returns the params of the agent a, all of them: an import
// replaces them.
func importedParams(a ui.ConfigAgent) store.Params {
	params := store.Params{}
	maps.Copy(params, a.Params)
	return params
}

// createAgent creates the agent a of a configuration file, with its secrets,
// its account of MT5, its protection and its daily limits.
func (imp *configImporter) createAgent(ctx context.Context, a ui.ConfigAgent, secrets configAgentSecrets) error {
	account, prompt, err := imp.agentRefs(ctx, a)
	if err != nil {
		return err
	}
	agent := store.Agent{Name: a.Name, Kind: a.Kind, Enabled: a.Enabled, Mode: a.Mode, APIAddress: a.APIAddress,
		Launched: a.Launched, Restart: a.Restart, TradePanel: a.TradePanel, TelegramControl: a.TelegramControl,
		Reanalysis: &store.AgentReanalysis{Allowed: a.Reanalysis}, Params: importedParams(a)}
	if acc := a.Account; acc != nil {
		agent.Account = &store.Account{BridgeURL: acc.BridgeURL, Magic: nonZero(acc.Magic),
			Comment: nonEmpty(acc.Comment), RealAllowed: acc.RealAllowed}
		if account != nil {
			agent.Account.BridgeURL = account.BridgeURL // the bridge of its account
		}
	}
	if l := a.LLM; l != nil {
		agent.LLM = &store.LLMSettings{Provider: l.Provider, Model: l.Model, BaseURL: nonEmpty(l.BaseURL),
			ReasoningEffort: nonEmpty(l.ReasoningEffort), MaxTokens: nonZero(l.MaxTokens), PromptID: prompt}
	}
	created, err := imp.s.store.CreateAgent(ctx, agent, store.AgentSecrets{LLMAPIKey: nonEmpty(secrets.LLMAPIKey),
		BridgeToken: nonEmpty(secrets.BridgeToken)})
	if err != nil {
		return err
	}
	if account != nil {
		if err := imp.s.store.SetAgentMT5Account(ctx, created.ID, &account.ID, imp.mt5Change()); err != nil {
			return err
		}
	}
	if !sameFields(a.Protection, created.ProtectionOf()) {
		if _, err := imp.s.store.SetAgentProtection(ctx, created.ID, a.Protection, imp.by); err != nil {
			return err
		}
	}
	if !sameFields(limitOnly(a.DailyLimit), limitOnly(ui.DailyLimitSettings{})) {
		change := imp.dailyChange()
		if _, _, err := imp.s.store.SetAgentDailyLimit(ctx, created.ID, a.DailyLimit, change); err != nil {
			return err
		}
		imp.settleDaily(&created, nil, change)
	}
	imp.reconcile()
	return nil
}

// updateAgent replaces the agent record by a of a configuration file, as it
// stood (before), as its screen changes it: its account of MT5 switched first.
func (imp *configImporter) updateAgent(ctx context.Context, a, before ui.ConfigAgent, record store.Agent,
	secrets configAgentSecrets, daily *dailyPlan) error {
	account, prompt, err := imp.agentRefs(ctx, a)
	if err != nil {
		return err
	}
	if a.MT5Account != before.MT5Account {
		var target *int64
		if account != nil {
			target = &account.ID
		}
		if err := imp.s.store.SetAgentMT5Account(ctx, record.ID, target, imp.mt5Change()); err != nil {
			return err
		}
	}
	update := store.AgentUpdate{Enabled: &a.Enabled, TradePanel: &a.TradePanel, TelegramControl: &a.TelegramControl,
		Mode: &a.Mode, APIAddress: &a.APIAddress, Launched: &a.Launched, Restart: &a.Restart, Params: importedParams(a)}
	if a.Kind == "llm" || a.Reanalysis {
		update.Reanalysis = &a.Reanalysis
	}
	if acc := a.Account; acc != nil {
		update.Account = &store.AccountUpdate{Magic: &acc.Magic, Comment: &acc.Comment, RealAllowed: &acc.RealAllowed}
		if account == nil {
			update.Account.BridgeURL = &acc.BridgeURL // else the bridge of its account
		}
	}
	if l := a.LLM; l != nil {
		update.LLM = &store.LLMUpdate{Provider: &l.Provider, Model: &l.Model, BaseURL: &l.BaseURL,
			ReasoningEffort: &l.ReasoningEffort, MaxTokens: &l.MaxTokens}
	}
	if _, err := imp.s.store.UpdateAgent(ctx, record.ID, update); err != nil {
		return err
	}
	if a.Kind == "llm" && a.Prompt != before.Prompt {
		if err := imp.s.store.SetAgentPrompt(ctx, record.ID, prompt); err != nil {
			return err
		}
	}
	if !sameFields(a.Protection, before.Protection) {
		if _, err := imp.s.store.SetAgentProtection(ctx, record.ID, a.Protection, imp.by); err != nil {
			return err
		}
	}
	if !sameFields(a.DailyLimit, before.DailyLimit) {
		change := imp.dailyChange()
		if _, _, err := imp.s.store.SetAgentDailyLimit(ctx, record.ID, a.DailyLimit, change); err != nil {
			return err
		}
		imp.settleDaily(&record, daily, change)
	}
	if a.Enabled && !before.Enabled {
		// enabled again: a stop of the supervisor ends, as on its screen
		if _, err := imp.s.store.ClearAgentStop(ctx, record.ID, imp.by); err != nil {
			return err
		}
	}
	if secrets.LLMAPIKey != "" || secrets.BridgeToken != "" {
		if err := imp.s.store.SetAgentSecrets(ctx, record.ID, store.AgentSecrets{
			LLMAPIKey: nonEmpty(secrets.LLMAPIKey), BridgeToken: nonEmpty(secrets.BridgeToken)}); err != nil {
			return err
		}
	}
	imp.reconcile()
	return nil
}

// rolesOf returns the roles of the file to import: the fixed one never.
func (imp *configImporter) rolesOf() []*importItem {
	current := map[string]ui.ConfigRole{}
	for _, r := range imp.current.Roles {
		current[r.Name] = r
	}
	items := []*importItem{}
	for _, r := range imp.file.Roles {
		it := imp.item(ui.ConfigSectionRoles, r.Name, r.Name)
		items = append(items, it)
		if record, ok := imp.roles[r.Name]; ok && record.Fixed() && it.preview.Status != ui.ConfigStatusInvalid {
			it.refuse("O papel " + r.Name + " é fixo: não é importado")
		}
		before, exists := current[r.Name]
		if !imp.compare(it, exists, before, r, false) {
			continue
		}
		change := store.RoleChange{Name: &r.Name, Description: &r.Description, Permissions: &r.Permissions,
			By: imp.by}
		it.apply = func(ctx context.Context) error {
			var err error
			if exists {
				_, err = imp.s.store.UpdateRole(ctx, imp.roles[r.Name].ID, change)
			} else {
				_, err = imp.s.store.CreateRole(ctx, change)
			}
			if err == nil {
				imp.effect("users", func(context.Context) { imp.s.auth().refreshUsers() })
			}
			return err
		}
	}
	return items
}

// limits returns the limits of the file to import: the daily limits of all
// the agents warned on what they do now, as on their screen.
func (imp *configImporter) limits(ctx context.Context) []*importItem {
	l := imp.file.Limits
	if l == nil {
		return nil
	}
	current := deref(imp.current.Limits)
	items := []*importItem{}
	if l.DailyLimit != nil {
		it := imp.item(ui.ConfigSectionLimits, ui.ConfigItemDailyLimit, configItemLabels[ui.ConfigItemDailyLimit])
		items = append(items, it)
		limits := limitOnly(*l.DailyLimit)
		if imp.compare(it, current.DailyLimit != nil, deref(current.DailyLimit), limits, false) {
			plan := imp.planDaily(ctx, it, nil, limits)
			it.apply = func(ctx context.Context) error {
				change := imp.dailyChange()
				if _, _, err := imp.s.store.SetGlobalDailyLimit(ctx, limits, change); err != nil {
					return err
				}
				imp.settleDaily(nil, plan, change)
				return nil
			}
		}
	}
	if l.TradingHours != nil {
		it := imp.item(ui.ConfigSectionLimits, ui.ConfigItemTradingHours,
			configItemLabels[ui.ConfigItemTradingHours])
		items = append(items, it)
		hours := *l.TradingHours
		if imp.compare(it, current.TradingHours != nil, deref(current.TradingHours), hours, false) {
			it.warn("Vale na hora: as entradas passam a seguir as janelas do arquivo.")
			it.apply = func(ctx context.Context) error {
				if _, _, err := imp.s.store.SetTradingHours(ctx, ui.TradingHoursSettings{Enabled: hours.Enabled,
					Windows: hours.Windows}, imp.by, imp.userID, importReason); err != nil {
					return err
				}
				if sup := imp.s.supervision; sup != nil {
					imp.effect("hours", func(ctx context.Context) { sup.daily.evaluateHours(ctx) })
				}
				return nil
			}
		}
	}
	if l.Exposure != nil {
		it := imp.item(ui.ConfigSectionLimits, ui.ConfigItemExposure, configItemLabels[ui.ConfigItemExposure])
		items = append(items, it)
		exposure := *l.Exposure
		exposure.UpdatedBy, exposure.UpdatedAt = "", 0
		if imp.compare(it, current.Exposure != nil, deref(current.Exposure), exposure, false) {
			it.apply = func(ctx context.Context) error {
				if _, err := imp.s.store.SetExposureLimits(ctx, exposure, imp.by); err != nil {
					return err
				}
				imp.refreshRuntimes()
				return nil
			}
		}
	}
	if l.AssetAgents != nil {
		it := imp.item(ui.ConfigSectionLimits, ui.ConfigItemAssetAgents, configItemLabels[ui.ConfigItemAssetAgents])
		items = append(items, it)
		rules := *l.AssetAgents
		rules.UpdatedBy, rules.UpdatedAt = "", 0
		if imp.compare(it, current.AssetAgents != nil, deref(current.AssetAgents), rules, false) {
			it.apply = func(ctx context.Context) error {
				if _, err := imp.s.store.SetAssetAgents(ctx, rules, imp.by); err != nil {
					return err
				}
				imp.refreshRuntimes()
				return nil
			}
		}
	}
	return items
}

// news returns the news of the file to import.
func (imp *configImporter) news() []*importItem {
	n := imp.file.News
	if n == nil {
		return nil
	}
	it := imp.item(ui.ConfigSectionNews, ui.ConfigItemSettings, "Notícias")
	token := imp.secrets.News
	it.preview.Secrets = token != ""
	if imp.compare(it, imp.current.News != nil, deref(imp.current.News), *n, differs(token, imp.stored.News)) {
		news := *n
		it.apply = func(ctx context.Context) error {
			if _, err := imp.s.store.SetNews(ctx, store.NewsUpdate{Enabled: &news.Enabled, Token: nonEmpty(token),
				URL: &news.URL, BlockBefore: &news.BlockBefore, BlockAfter: &news.BlockAfter,
				BlockImportance: &news.BlockImportance, By: imp.by}); err != nil {
				return err
			}
			imp.refreshRuntimes()
			return nil
		}
	}
	return []*importItem{it}
}

// supervisor returns the settings of the XTrader AI of the file to import.
func (imp *configImporter) supervisor() []*importItem {
	sup := imp.file.Supervisor
	if sup == nil {
		return nil
	}
	it := imp.item(ui.ConfigSectionSupervisor, ui.ConfigItemSettings, "Configuração do XTrader AI")
	key := imp.secrets.Supervisor
	it.preview.Secrets = key != ""
	if imp.compare(it, imp.current.Supervisor != nil, deref(imp.current.Supervisor), *sup,
		differs(key, imp.stored.Supervisor)) {
		settings := *sup
		autonomy := store.Autonomy(settings.Autonomy)
		it.apply = func(ctx context.Context) error {
			if _, err := imp.s.store.SetSupervisor(ctx, store.SupervisorUpdate{Enabled: &settings.Enabled,
				IntervalSeconds: &settings.IntervalSeconds, Provider: &settings.Provider, Model: &settings.Model,
				BaseURL: &settings.BaseURL, ReasoningEffort: &settings.ReasoningEffort,
				MaxTokens: &settings.MaxTokens, TimeoutSeconds: &settings.TimeoutSeconds,
				Instructions: &settings.Instructions, Autonomy: &autonomy, APIKey: nonEmpty(key)}); err != nil {
				return err
			}
			if s := imp.s.supervision; s != nil {
				imp.effect("supervisor", func(context.Context) { s.Trigger() })
			}
			return nil
		}
	}
	return []*importItem{it}
}

// telegram returns the bot of Telegram and its users of the file to import.
func (imp *configImporter) telegram() []*importItem {
	t := imp.file.Telegram
	if t == nil {
		return nil
	}
	current := deref(imp.current.Telegram)
	items := []*importItem{}
	if t.Bot != nil {
		it := imp.item(ui.ConfigSectionTelegram, ui.ConfigItemTelegramBot, configItemLabels[ui.ConfigItemTelegramBot])
		items = append(items, it)
		token := imp.secrets.Telegram
		it.preview.Secrets = token != ""
		bot := *t.Bot
		if imp.compare(it, current.Bot != nil, deref(current.Bot), bot, differs(token, imp.stored.Telegram)) {
			it.apply = func(ctx context.Context) error {
				if err := imp.s.store.SetTelegram(ctx, &bot.Enabled, nonEmpty(token)); err != nil {
					return err
				}
				imp.trigger()
				return nil
			}
		}
	}
	users := map[int64]ui.ConfigTelegramUser{}
	for _, u := range current.Users {
		users[u.TelegramID] = u
	}
	for _, u := range t.Users {
		label := telegramUserKey(u.TelegramID)
		if u.Name != "" {
			label = fmt.Sprintf("%s (%d)", u.Name, u.TelegramID)
		}
		it := imp.item(ui.ConfigSectionTelegram, telegramUserKey(u.TelegramID), label)
		items = append(items, it)
		before, exists := users[u.TelegramID]
		if !imp.compare(it, exists, before, u, false) {
			continue
		}
		it.apply = func(ctx context.Context) error {
			var err error
			if exists {
				err = imp.s.store.SetTelegramUserName(ctx, imp.users[u.TelegramID].ID, u.Name)
			} else {
				_, err = imp.s.store.AddTelegramUser(ctx, u.TelegramID, u.Name)
			}
			if err == nil {
				imp.trigger()
			}
			return err
		}
	}
	return items
}

// items returns the items of the file to import, in the order they are
// applied in: the ones an item names (a prompt, an account of MT5, an agent of
// the limits) before it.
func (imp *configImporter) items(ctx context.Context) []*importItem {
	items := imp.prompts()
	items = append(items, imp.mt5Accounts()...)
	items = append(items, imp.agents(ctx)...)
	items = append(items, imp.rolesOf()...)
	items = append(items, imp.limits(ctx)...)
	items = append(items, imp.news()...)
	items = append(items, imp.supervisor()...)
	return append(items, imp.telegram()...)
}

// refused reports whether err is the refusal of an item, told the user: an
// error of the store of the request, or of what the agent trades.
func refused(err error) bool {
	return errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrConflict) ||
		errors.Is(err, store.ErrNotFound) || errors.As(err, new(samePositions))
}

// newImporter returns the importer of file for the user of ctx, the secrets
// of the file opened with password, if any, told on preview.
func (s *settings) newImporter(ctx context.Context, file ui.ConfigFile, password string,
	preview *ui.ConfigPreview) (*configImporter, error) {
	switch {
	case file.Format != ui.ConfigFormat:
		return nil, configRefusal("O arquivo não é um arquivo de configuração do XTrader.")
	case file.Version < 1 || file.Version > ui.ConfigVersion:
		return nil, configRefusal(fmt.Sprintf("O arquivo é da versão %d do formato, e esta instalação lê até a "+
			"versão %d: atualize o XTrader antes de importá-lo.", file.Version, ui.ConfigVersion))
	}
	imp := &configImporter{s: s, file: file, manages: canManage(ctx), by: userOf(ctx), seen: map[string]bool{},
		after: map[string]func(context.Context){}, records: map[string]store.Agent{},
		accounts: map[string]store.MT5Account{}, roles: map[string]store.Role{}, users: map[int64]store.TelegramUser{}}
	if id := userIDOf(ctx); id != 0 {
		imp.userID = &id
	}
	if file.Secrets != nil {
		preview.Secrets = ui.ConfigSecretsLocked
		if password != "" {
			values, err := openConfigSecrets(file.Secrets, password)
			if err != nil {
				preview.SecretsError = sentence(err.Error())
			} else {
				imp.secrets, preview.Secrets = values, ui.ConfigSecretsUnlocked
			}
		}
	}
	var err error
	if imp.current, imp.stored, err = currentConfig(ctx, s.store, preview.Secrets == ui.ConfigSecretsUnlocked); err != nil {
		return nil, err
	}
	agents, err := s.store.Agents(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		imp.records[a.Name] = a
	}
	accounts, err := s.store.MT5Accounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		imp.accounts[a.Name] = a
	}
	roles, err := s.store.Roles(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		imp.roles[r.Name] = r
	}
	users, err := s.store.TelegramUsers(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		imp.users[u.TelegramID] = u
	}
	return imp, nil
}

// importConfig previews the import of the configuration file of request
// (commit false: every item checked by applying it, in a transaction always
// rolled back, a savepoint each so that one refused doesn't hide the others)
// or imports the items it picks (commit true: all of them in one transaction,
// none applied when one is refused), then applies them as their screens do.
func (s *settings) importConfig(ctx context.Context, request ui.ConfigImportRequest,
	commit bool) (ui.ConfigPreview, error) {
	preview := ui.ConfigPreview{Secrets: ui.ConfigSecretsNone, Sections: []ui.ConfigPreviewSection{}}
	imp, err := s.newImporter(ctx, request.File, request.Password, &preview)
	if err != nil {
		return preview, err
	}
	items := imp.items(ctx)
	picked := func(it *importItem) bool {
		return !commit || !it.repeated && selected(request.Selection, it.section, it.preview.Key)
	}
	failed := false
	for _, it := range items {
		if picked(it) && it.preview.Status == ui.ConfigStatusInvalid {
			failed = true // picked and refused: nothing applied
		}
	}
	err = s.store.Transaction(ctx, func(ctx context.Context) error {
		for i, it := range items {
			if it.apply == nil || !picked(it) {
				continue
			}
			if err := s.store.Savepoint(ctx, fmt.Sprintf("config_item_%d", i), it.apply); err != nil {
				if !refused(err) {
					return err
				}
				it.refuse(userMessage(err))
				failed = true
			}
		}
		if !commit || failed {
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return preview, err
	}
	preview.Applied = commit && !failed
	written := 0
	for _, area := range configAreas {
		section := ui.ConfigPreviewSection{Key: area.key, Label: area.label, Items: []ui.ConfigPreviewItem{}}
		for _, it := range items {
			if it.section == area.key && picked(it) {
				section.Items = append(section.Items, it.preview)
				if it.apply != nil {
					written++
				}
			}
		}
		if len(section.Items) > 0 {
			preview.Sections = append(preview.Sections, section)
		}
	}
	if preview.Applied {
		log.Infof("ui: the configuration imported by %s: %d items written, the secrets %s", imp.by, written,
			preview.Secrets)
		ctx := context.WithoutCancel(ctx)
		for _, key := range imp.effects {
			imp.after[key](ctx)
		}
	}
	return preview, nil
}

// handleConfigPreview answers the preview of the import of a configuration
// file, see ui.ConfigPreview: nothing applied.
func (s *settings) handleConfigPreview(w http.ResponseWriter, r *http.Request) {
	s.configImport(w, r, false)
}

// handleConfigImport imports the items picked of a configuration file, all
// of them or none, see ui.ConfigPreview.
func (s *settings) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	s.configImport(w, r, true)
}

func (s *settings) configImport(w http.ResponseWriter, r *http.Request, commit bool) {
	var request ui.ConfigImportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, ui.MaxConfigFile)).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "O arquivo não pôde ser lido: escolha um arquivo de configuração "+
			"do XTrader (JSON) de até 8 MB.")
		return
	}
	preview, err := s.importConfig(r.Context(), request, commit)
	var refusal configRefusal
	switch {
	case errors.As(err, &refusal):
		writeJSONError(w, http.StatusBadRequest, refusal.Error())
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, preview)
}

// The names of the fields of the items of a configuration file, by their
// JSON key, as the preview tells the ones that change.
var configFieldLabels = map[string]string{
	"name": "nome", "description": "descrição", "content": "conteúdo", "login": "login", "server": "servidor",
	"bridge_url": "bridge", "allow_real": "permitir conta real", "read_only": "somente leitura", "kind": "tipo",
	"mode": "modo", "api_address": "endereço da API", "enabled": "ligado", "launched": "iniciado pelo controller",
	"restart": "reiniciar se cair", "trade_panel": "painel de trading", "telegram_control": "controle via Telegram",
	"reanalysis": "reanálise", "params": "parâmetros", "account": "conta", "llm": "LLM", "prompt": "prompt",
	"mt5_account": "conta MT5", "protection": "proteção", "daily_limit": "limite diário",
	"permissions": "permissões", "max_loss": "perda máxima", "max_gain": "ganho máximo", "windows": "janelas",
	"broker_stops": "stops na corretora",
	"pairs":        "ativos", "rules": "regras", "url": "servidor de notícias", "block_before": "bloqueio antes",
	"block_after": "bloqueio depois", "block_importance": "importância", "interval_seconds": "intervalo",
	"timeout_seconds": "prazo", "provider": "provedor", "model": "modelo", "base_url": "URL do provedor",
	"reasoning_effort": "raciocínio", "max_tokens": "máximo de tokens", "instructions": "instruções",
	"autonomy": "autonomia",
}

// metaFields are the fields of a setting telling who changed it last and
// when, and the currency of a daily limit (the one of each account): never
// compared.
var metaFields = map[string]bool{"updated_by": true, "updated_at": true, "currency": true}

// fieldsOf returns v as JSON, the empty values (null, "", 0, false, {}, [])
// and the meta fields (see metaFields) left out of its objects, so that a
// field absent and one empty compare equal.
func fieldsOf(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return nil
	}
	return pruned(value)
}

// pruned returns value without its empty values, nil when nothing is left.
func pruned(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, field := range v {
			if metaFields[key] {
				delete(v, key)
			} else if field = pruned(field); field == nil {
				delete(v, key)
			} else {
				v[key] = field
			}
		}
		if len(v) == 0 {
			return nil
		}
		return v
	case []any:
		if len(v) == 0 {
			return nil
		}
		for i := range v {
			v[i] = pruned(v[i])
		}
		return v
	case string:
		if v == "" {
			return nil
		}
	case float64:
		if v == 0 {
			return nil
		}
	case bool:
		if !v {
			return nil
		}
	}
	return value
}

// sameFields reports whether a and b have the same fields, see fieldsOf.
func sameFields(a, b any) bool {
	return reflect.DeepEqual(fieldsOf(a), fieldsOf(b))
}

// fieldChanges returns the names of the fields of next differing from the
// ones of before (see configFieldLabels, fieldsOf), in their order by key: of
// the params, the ones that change, by name.
func fieldChanges(before, next any) []string {
	was, _ := fieldsOf(before).(map[string]any)
	now, _ := fieldsOf(next).(map[string]any)
	keys := slices.Sorted(maps.Keys(was))
	for key := range now {
		if _, ok := was[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	changes := []string{}
	for _, key := range keys {
		if reflect.DeepEqual(was[key], now[key]) {
			continue
		}
		label := configFieldLabels[key]
		if label == "" {
			label = key
		}
		if key == "params" {
			label += " (" + strings.Join(changedKeys(was[key], now[key]), ", ") + ")"
		}
		changes = append(changes, label)
	}
	return changes
}

// changedKeys returns the keys of the objects a and b whose values differ, in
// their order.
func changedKeys(a, b any) []string {
	was, _ := a.(map[string]any)
	now, _ := b.(map[string]any)
	keys := []string{}
	for key := range was {
		if !reflect.DeepEqual(was[key], now[key]) {
			keys = append(keys, key)
		}
	}
	for key := range now {
		if _, ok := was[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}
