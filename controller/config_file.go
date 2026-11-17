package controller

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// configArea is a section of a configuration file: its name and the
// permissions to see (export) and to change (import, and its secrets) it.
type configArea struct {
	key, label   string
	view, manage string
}

// configAreas are the sections of a configuration file the controller
// keeps, in the order they are imported in, see ui.ConfigFile: the
// preferences of the browser (ui.ConfigSectionDashboard) are the web's.
var configAreas = []configArea{
	{ui.ConfigSectionPrompts, "Prompts", ui.PermPromptsView, ui.PermPromptsManage},
	{ui.ConfigSectionMT5Accounts, "Contas MT5", ui.PermMT5AccountsView, ui.PermMT5AccountsManage},
	{ui.ConfigSectionAgents, "Agentes", ui.PermAgentsView, ui.PermAgentsManage},
	{ui.ConfigSectionRoles, "Papéis", ui.PermUsersView, ui.PermUsersManage},
	{ui.ConfigSectionLimits, "Limites", ui.PermLimitsView, ui.PermLimitsManage},
	{ui.ConfigSectionNews, "Notícias", ui.PermNewsView, ui.PermNewsManage},
	{ui.ConfigSectionSupervisor, "XTrader AI", ui.PermSupervisorView, ui.PermSupervisorManage},
	{ui.ConfigSectionTelegram, "Telegram", ui.PermTelegramView, ui.PermTelegramManage},
}

// configSecretSections are the sections whose items have secrets.
var configSecretSections = []string{ui.ConfigSectionMT5Accounts, ui.ConfigSectionAgents, ui.ConfigSectionNews,
	ui.ConfigSectionSupervisor, ui.ConfigSectionTelegram}

// configViewPermissions are the permissions of the routes of the
// configuration files: the panel (its preferences, the web's) or the view of
// an area.
func configViewPermissions() []string {
	perms := []string{ui.PermDashboardView}
	for _, area := range configAreas {
		perms = append(perms, area.view)
	}
	return perms
}

// configManagePermissions are the permissions of the imports: the change of
// an area.
func configManagePermissions() []string {
	perms := []string{}
	for _, area := range configAreas {
		perms = append(perms, area.manage)
	}
	return perms
}

// The labels of the items of the sections of single settings.
var configItemLabels = map[string]string{
	ui.ConfigItemDailyLimit:   "Limite diário global",
	ui.ConfigItemTradingHours: "Horário de operação",
	ui.ConfigItemExposure:     "Exposição por ativo",
	ui.ConfigItemAssetAgents:  "Agentes por ativo",
	ui.ConfigItemTelegramBot:  "Bot do Telegram",
}

// configItem is an item of a configuration file: its section, its key and
// its name.
type configItem struct {
	section, key, label string
}

// telegramUserKey returns the key of a user of Telegram in a configuration
// file: its ID on Telegram.
func telegramUserKey(id int64) string { return strconv.FormatInt(id, 10) }

// configItems returns the items of file, by section, in the order of
// configAreas.
func configItems(file ui.ConfigFile) map[string][]configItem {
	items := map[string][]configItem{}
	add := func(section, key, label string) {
		items[section] = append(items[section], configItem{section: section, key: key, label: label})
	}
	for _, p := range file.Prompts {
		add(ui.ConfigSectionPrompts, p.Name, p.Name)
	}
	for _, a := range file.MT5Accounts {
		add(ui.ConfigSectionMT5Accounts, a.Name, fmt.Sprintf("%s (%d · %s)", a.Name, a.Login, a.Server))
	}
	for _, a := range file.Agents {
		add(ui.ConfigSectionAgents, a.Name, a.Name)
	}
	for _, r := range file.Roles {
		add(ui.ConfigSectionRoles, r.Name, r.Name)
	}
	if l := file.Limits; l != nil {
		for key, set := range map[string]bool{ui.ConfigItemDailyLimit: l.DailyLimit != nil,
			ui.ConfigItemTradingHours: l.TradingHours != nil, ui.ConfigItemExposure: l.Exposure != nil,
			ui.ConfigItemAssetAgents: l.AssetAgents != nil} {
			if set {
				add(ui.ConfigSectionLimits, key, configItemLabels[key])
			}
		}
		slices.SortFunc(items[ui.ConfigSectionLimits], func(a, b configItem) int {
			return cmp.Compare(limitOrder(a.key), limitOrder(b.key))
		})
	}
	if file.News != nil {
		add(ui.ConfigSectionNews, ui.ConfigItemSettings, "Notícias")
	}
	if file.Supervisor != nil {
		add(ui.ConfigSectionSupervisor, ui.ConfigItemSettings, "Configuração do XTrader AI")
	}
	if t := file.Telegram; t != nil {
		if t.Bot != nil {
			add(ui.ConfigSectionTelegram, ui.ConfigItemTelegramBot, configItemLabels[ui.ConfigItemTelegramBot])
		}
		for _, u := range t.Users {
			label := telegramUserKey(u.TelegramID)
			if u.Name != "" {
				label = fmt.Sprintf("%s (%d)", u.Name, u.TelegramID)
			}
			add(ui.ConfigSectionTelegram, telegramUserKey(u.TelegramID), label)
		}
	}
	return items
}

// limitOrder returns the place of a limit among the items of the limits.
func limitOrder(key string) int {
	return slices.Index([]string{ui.ConfigItemDailyLimit, ui.ConfigItemTradingHours, ui.ConfigItemExposure,
		ui.ConfigItemAssetAgents}, key)
}

// selected reports whether the item of key of section is in selection.
func selected(selection map[string][]string, section, key string) bool {
	return slices.Contains(selection[section], key)
}

// selectConfig returns the items of file in selection, of the sections
// allowed: the others left out.
func selectConfig(file ui.ConfigFile, selection map[string][]string, allowed func(section string) bool) ui.ConfigFile {
	pick := func(section, key string) bool { return allowed(section) && selected(selection, section, key) }
	kept := ui.ConfigFile{Format: file.Format, Version: file.Version, AppSchema: file.AppSchema,
		ExportedAt: file.ExportedAt, ExportedBy: file.ExportedBy}
	for _, p := range file.Prompts {
		if pick(ui.ConfigSectionPrompts, p.Name) {
			kept.Prompts = append(kept.Prompts, p)
		}
	}
	for _, a := range file.MT5Accounts {
		if pick(ui.ConfigSectionMT5Accounts, a.Name) {
			kept.MT5Accounts = append(kept.MT5Accounts, a)
		}
	}
	for _, a := range file.Agents {
		if pick(ui.ConfigSectionAgents, a.Name) {
			kept.Agents = append(kept.Agents, a)
		}
	}
	for _, r := range file.Roles {
		if pick(ui.ConfigSectionRoles, r.Name) {
			kept.Roles = append(kept.Roles, r)
		}
	}
	if l := file.Limits; l != nil {
		limits := ui.ConfigLimits{}
		if pick(ui.ConfigSectionLimits, ui.ConfigItemDailyLimit) {
			limits.DailyLimit = l.DailyLimit
		}
		if pick(ui.ConfigSectionLimits, ui.ConfigItemTradingHours) {
			limits.TradingHours = l.TradingHours
		}
		if pick(ui.ConfigSectionLimits, ui.ConfigItemExposure) {
			limits.Exposure = l.Exposure
		}
		if pick(ui.ConfigSectionLimits, ui.ConfigItemAssetAgents) {
			limits.AssetAgents = l.AssetAgents
		}
		if limits != (ui.ConfigLimits{}) {
			kept.Limits = &limits
		}
	}
	if pick(ui.ConfigSectionNews, ui.ConfigItemSettings) {
		kept.News = file.News
	}
	if pick(ui.ConfigSectionSupervisor, ui.ConfigItemSettings) {
		kept.Supervisor = file.Supervisor
	}
	if t := file.Telegram; t != nil {
		telegram := ui.ConfigTelegram{}
		if pick(ui.ConfigSectionTelegram, ui.ConfigItemTelegramBot) {
			telegram.Bot = t.Bot
		}
		for _, u := range t.Users {
			if pick(ui.ConfigSectionTelegram, telegramUserKey(u.TelegramID)) {
				telegram.Users = append(telegram.Users, u)
			}
		}
		if telegram.Bot != nil || len(telegram.Users) > 0 {
			kept.Telegram = &telegram
		}
	}
	return kept
}

// limitOnly returns settings without who changed them and when: what a
// configuration file keeps of a daily limit, the stops at the broker on and
// the least margin by default when it doesn't tell them (a file of before
// them: the defaults).
func limitOnly(settings ui.DailyLimitSettings) ui.DailyLimitSettings {
	settings.UpdatedBy, settings.UpdatedAt, settings.Currency = "", 0, ""
	if settings.BrokerStops == nil {
		on := true
		settings.BrokerStops = &on
	}
	if settings.MinMarginPct == nil {
		margin := ui.DefaultMinMarginPct
		settings.MinMarginPct = &margin
	}
	return settings
}

// currentConfig returns the settings kept by the controller as a
// configuration file, every item of every section, and their secrets when
// withSecrets.
func currentConfig(ctx context.Context, s *store.Store, withSecrets bool) (ui.ConfigFile, configSecretValues, error) {
	file := ui.ConfigFile{Format: ui.ConfigFormat, Version: ui.ConfigVersion, AppSchema: store.SchemaVersion}
	secrets := configSecretValues{Agents: map[string]configAgentSecrets{}, MT5Accounts: map[string]configMT5Secrets{}}

	summaries, err := s.Prompts(ctx)
	if err != nil {
		return file, secrets, err
	}
	promptNames := map[int64]string{}
	for _, summary := range summaries {
		p, err := s.Prompt(ctx, summary.ID)
		if err != nil {
			return file, secrets, err
		}
		promptNames[p.ID] = p.Name
		file.Prompts = append(file.Prompts, ui.ConfigPrompt{Name: p.Name, Description: p.Description, Content: p.Content})
	}

	accounts, err := s.MT5Accounts(ctx)
	if err != nil {
		return file, secrets, err
	}
	accountNames := map[int64]string{}
	for _, a := range accounts {
		accountNames[a.ID] = a.Name
		file.MT5Accounts = append(file.MT5Accounts, ui.ConfigMT5Account{Name: a.Name, Login: a.Login,
			Server: a.Server, BridgeURL: a.BridgeURL, AllowReal: a.AllowReal, ReadOnly: a.ReadOnly})
		if withSecrets {
			password, token, err := s.MT5AccountSecrets(a)
			if err != nil {
				return file, secrets, err
			}
			if password != "" || token != "" {
				secrets.MT5Accounts[a.Name] = configMT5Secrets{Password: password, BridgeToken: token}
			}
		}
	}

	agents, err := s.Agents(ctx)
	if err != nil {
		return file, secrets, err
	}
	for _, a := range agents {
		agent := ui.ConfigAgent{Name: a.Name, Kind: a.Kind, Mode: a.Mode, APIAddress: a.APIAddress,
			Enabled: a.Enabled, Launched: a.Launched, Restart: a.Restart, TradePanel: a.TradePanel,
			TelegramControl: a.TelegramControl, Reanalysis: a.ReanalysisAllowed(), Params: map[string]string(a.AllParams()),
			Protection: a.ProtectionOf(), DailyLimit: limitOnly(a.DailyLimitOf())}
		if acc := a.Account; acc != nil {
			agent.Account = &ui.ConfigAgentAccount{BridgeURL: acc.BridgeURL, Magic: deref(acc.Magic),
				Comment: deref(acc.Comment), RealAllowed: acc.RealAllowed}
		}
		if llm := a.LLM; llm != nil {
			agent.LLM = &ui.ConfigAgentLLM{Provider: llm.Provider, Model: llm.Model, BaseURL: deref(llm.BaseURL),
				ReasoningEffort: deref(llm.ReasoningEffort), MaxTokens: deref(llm.MaxTokens)}
			if llm.PromptID != nil {
				agent.Prompt = promptNames[*llm.PromptID]
			}
		}
		if link := a.MT5Link; link != nil {
			agent.MT5Account = accountNames[link.AccountID]
		}
		file.Agents = append(file.Agents, agent)
		if withSecrets {
			values, err := s.AgentSecrets(ctx, a.ID)
			if err != nil {
				return file, secrets, err
			}
			if agent.MT5Account != "" {
				values.BridgeToken = "" // the account's
			}
			if values.LLMAPIKey != "" || values.BridgeToken != "" {
				secrets.Agents[a.Name] = configAgentSecrets{LLMAPIKey: values.LLMAPIKey, BridgeToken: values.BridgeToken}
			}
		}
	}

	roles, err := s.Roles(ctx)
	if err != nil {
		return file, secrets, err
	}
	for _, r := range roles {
		if !r.Fixed() {
			file.Roles = append(file.Roles, ui.ConfigRole{Name: r.Name, Description: r.Description,
				Permissions: r.Granted()})
		}
	}

	limits := ui.ConfigLimits{}
	daily, _, err := s.GlobalDailyLimit(ctx)
	if err != nil {
		return file, secrets, err
	}
	settings := limitOnly(daily.Settings())
	limits.DailyLimit = &settings
	hours, _, err := s.TradingHours(ctx)
	if err != nil {
		return file, secrets, err
	}
	hoursSettings := hours.Settings()
	limits.TradingHours = &ui.ConfigTradingHours{Enabled: hoursSettings.Enabled, Windows: hoursSettings.Windows}
	exposure, _, err := s.ExposureLimits(ctx)
	if err != nil {
		return file, secrets, err
	}
	exposureSettings := exposure.Settings()
	exposureSettings.UpdatedBy, exposureSettings.UpdatedAt = "", 0
	limits.Exposure = &exposureSettings
	assets, _, err := s.AssetAgents(ctx)
	if err != nil {
		return file, secrets, err
	}
	assetSettings := assets.Settings()
	assetSettings.UpdatedBy, assetSettings.UpdatedAt = "", 0
	limits.AssetAgents = &assetSettings
	file.Limits = &limits

	if news, found, err := s.News(ctx); err != nil {
		return file, secrets, err
	} else if found {
		rules, _, err := s.NewsRules(ctx)
		if err != nil {
			return file, secrets, err
		}
		file.News = &ui.ConfigNews{Enabled: news.Enabled, URL: deref(rules.MCPURL), BlockBefore: rules.BlockBefore,
			BlockAfter: rules.BlockAfter, BlockImportance: rules.BlockImportance}
		if withSecrets {
			if secrets.News, err = s.NewsToken(ctx); err != nil {
				return file, secrets, err
			}
		}
	}

	if sup, found, err := s.Supervisor(ctx); err != nil {
		return file, secrets, err
	} else if found {
		file.Supervisor = &ui.ConfigSupervisor{Enabled: sup.Enabled, IntervalSeconds: sup.IntervalSeconds,
			TimeoutSeconds: sup.TimeoutSeconds, Provider: deref(sup.Provider), Model: deref(sup.Model),
			BaseURL: deref(sup.BaseURL), ReasoningEffort: deref(sup.ReasoningEffort), MaxTokens: deref(sup.MaxTokens),
			Instructions: sup.Instructions, Autonomy: ui.SupervisorAutonomy(sup.Autonomy)}
		if withSecrets {
			if secrets.Supervisor, err = s.SupervisorKey(ctx); err != nil {
				return file, secrets, err
			}
		}
	}

	bot, _, err := s.Telegram(ctx)
	if err != nil {
		return file, secrets, err
	}
	users, err := s.TelegramUsers(ctx)
	if err != nil {
		return file, secrets, err
	}
	telegram := ui.ConfigTelegram{Bot: &ui.ConfigTelegramBot{Enabled: bot.Enabled}}
	for _, u := range users {
		telegram.Users = append(telegram.Users, ui.ConfigTelegramUser{TelegramID: u.TelegramID, Name: u.Name})
	}
	file.Telegram = &telegram
	if withSecrets {
		if secrets.Telegram, err = s.TelegramToken(ctx); err != nil {
			return file, secrets, err
		}
	}
	return file, secrets, nil
}

// secretsOf returns the secrets of the items of file of the sections allowed,
// of all.
func secretsOf(all configSecretValues, file ui.ConfigFile, allowed func(section string) bool) configSecretValues {
	kept := configSecretValues{}
	if allowed(ui.ConfigSectionAgents) {
		for _, a := range file.Agents {
			if values, ok := all.Agents[a.Name]; ok {
				if kept.Agents == nil {
					kept.Agents = map[string]configAgentSecrets{}
				}
				kept.Agents[a.Name] = values
			}
		}
	}
	if allowed(ui.ConfigSectionMT5Accounts) {
		for _, a := range file.MT5Accounts {
			if values, ok := all.MT5Accounts[a.Name]; ok {
				if kept.MT5Accounts == nil {
					kept.MT5Accounts = map[string]configMT5Secrets{}
				}
				kept.MT5Accounts[a.Name] = values
			}
		}
	}
	if file.News != nil && allowed(ui.ConfigSectionNews) {
		kept.News = all.News
	}
	if file.Supervisor != nil && allowed(ui.ConfigSectionSupervisor) {
		kept.Supervisor = all.Supervisor
	}
	if file.Telegram != nil && file.Telegram.Bot != nil && allowed(ui.ConfigSectionTelegram) {
		kept.Telegram = all.Telegram
	}
	return kept
}

// areaOf returns the area of section, false for an unknown one.
func areaOf(section string) (configArea, bool) {
	for _, area := range configAreas {
		if area.key == section {
			return area, true
		}
	}
	return configArea{}, false
}

// canSee and canManage return whether the user of ctx sees (exports) and
// changes (imports) the sections of a configuration file.
func canSee(ctx context.Context) func(section string) bool {
	a := accessOf(ctx)
	return func(section string) bool {
		area, ok := areaOf(section)
		return ok && a.can(area.view)
	}
}

func canManage(ctx context.Context) func(section string) bool {
	a := accessOf(ctx)
	return func(section string) bool {
		area, ok := areaOf(section)
		return ok && a.can(area.manage)
	}
}

// handleConfigCatalog answers the sections and the items of a configuration
// file the user may export, see ui.ConfigCatalog.
func (s *settings) handleConfigCatalog(w http.ResponseWriter, r *http.Request) {
	file, _, err := currentConfig(r.Context(), s.store, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	sees, manages := canSee(r.Context()), canManage(r.Context())
	items := configItems(file)
	catalog := ui.ConfigCatalog{Sections: []ui.ConfigSection{}}
	for _, area := range configAreas {
		if !sees(area.key) {
			continue
		}
		section := ui.ConfigSection{Key: area.key, Label: area.label, Items: []ui.ConfigItemRef{},
			Secrets: manages(area.key) && slices.Contains(configSecretSections, area.key)}
		for _, item := range items[area.key] {
			section.Items = append(section.Items, ui.ConfigItemRef{Key: item.key, Label: item.label})
		}
		catalog.Sections = append(catalog.Sections, section)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, catalog)
}

// handleConfigExport answers the configuration file of the items asked, of
// the sections the user sees, see ui.ConfigExportRequest: with their secrets,
// of the sections the user manages, encrypted with the password, when asked.
func (s *settings) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	var request ui.ConfigExportRequest
	if !decode(w, r, &request) {
		return
	}
	if request.IncludeSecrets && utf8.RuneCountInString(request.Password) < ui.MinConfigPassword {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("A senha dos segredos deve ter ao menos %d caracteres.", ui.MinConfigPassword))
		return
	}
	ctx := r.Context()
	current, all, err := currentConfig(ctx, s.store, request.IncludeSecrets)
	if err != nil {
		s.fail(w, err)
		return
	}
	file := selectConfig(current, request.Selection, canSee(ctx))
	file.ExportedAt, file.ExportedBy = time.Now().Unix(), userOf(ctx)
	withSecrets := false
	if request.IncludeSecrets {
		if values := secretsOf(all, file, canManage(ctx)); !values.empty() {
			if file.Secrets, err = sealConfigSecrets(values, request.Password); err != nil {
				s.fail(w, err)
				return
			}
			withSecrets = true
		}
	}
	counts := map[string]int{}
	for section, items := range configItems(file) {
		counts[section] = len(items)
	}
	log.Infof("ui: the configuration exported by %s: %v, with secrets: %t", userOf(ctx), counts, withSecrets)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, file)
}
