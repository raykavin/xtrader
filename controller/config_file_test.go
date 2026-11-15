//go:build unix

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// The secrets of a configuration file sealed with a password and opened with
// it; a wrong password, or secrets altered, errConfigPassword; params out of
// their bounds refused before any derivation; the secrets never in clear in
// what is sealed.
func TestConfigSecrets_SealAndOpen(t *testing.T) {
	values := configSecretValues{Agents: map[string]configAgentSecrets{"llm": {LLMAPIKey: "sk-the-key"}},
		MT5Accounts: map[string]configMT5Secrets{"demo": {Password: "mt5-password", BridgeToken: "bridge-token"}},
		Telegram:    "telegram-token"}
	sealed, err := sealConfigSecrets(values, "a password")
	require.NoError(t, err)
	data, err := json.Marshal(sealed)
	require.NoError(t, err)
	for _, secret := range []string{"sk-the-key", "mt5-password", "bridge-token", "telegram-token"} {
		assert.NotContains(t, string(data), secret)
	}
	assert.Equal(t, configKDF, sealed.KDF)

	opened, err := openConfigSecrets(sealed, "a password")
	require.NoError(t, err)
	assert.Equal(t, values, opened)

	_, err = openConfigSecrets(sealed, "another password")
	assert.ErrorIs(t, err, errConfigPassword)

	altered := *sealed
	altered.Data = strings.Repeat("A", len(sealed.Data))
	_, err = openConfigSecrets(&altered, "a password")
	assert.ErrorIs(t, err, errConfigPassword, "altered")

	again, err := sealConfigSecrets(values, "a password")
	require.NoError(t, err)
	assert.NotEqual(t, sealed.Salt, again.Salt, "a salt of its own")
	assert.NotEqual(t, sealed.Data, again.Data)

	for name, change := range map[string]func(s *ui.ConfigSecrets){
		"kdf":     func(s *ui.ConfigSecrets) { s.KDF = "scrypt" },
		"time":    func(s *ui.ConfigSecrets) { s.Time = maxConfigKDFTime + 1 },
		"memory":  func(s *ui.ConfigSecrets) { s.Memory = maxConfigKDFMemory + 1 },
		"threads": func(s *ui.ConfigSecrets) { s.Threads = 0 },
	} {
		bad := *sealed
		change(&bad)
		_, err := openConfigSecrets(&bad, "a password")
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, errConfigPassword, name)
	}
}

// exportOf asks the export of the items of selection as call's user, and
// returns its status and the file.
func exportOf(t *testing.T, call func(method, path, body string) (*http.Response, string),
	request ui.ConfigExportRequest) (int, ui.ConfigFile, string) {
	t.Helper()
	payload, err := json.Marshal(request)
	require.NoError(t, err)
	resp, body := call(http.MethodPost, "/api/config/export", string(payload))
	var file ui.ConfigFile
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.Unmarshal([]byte(body), &file), body)
	}
	return resp.StatusCode, file, body
}

// catalogOf returns the catalog of the configuration files of call's user, by
// section.
func catalogOf(t *testing.T, call func(method, path, body string) (*http.Response, string)) map[string]ui.ConfigSection {
	t.Helper()
	resp, body := call(http.MethodGet, "/api/config/catalog", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var catalog ui.ConfigCatalog
	require.NoError(t, json.Unmarshal([]byte(body), &catalog))
	sections := map[string]ui.ConfigSection{}
	for _, section := range catalog.Sections {
		sections[section.Key] = section
	}
	return sections
}

// itemKeys returns the keys of the items of section.
func itemKeys(section ui.ConfigSection) []string {
	keys := []string{}
	for _, item := range section.Items {
		keys = append(keys, item.Key)
	}
	return keys
}

// The configuration through the API: the catalog of every section and item;
// the file of the items asked, the agents by name with their prompt by name;
// without the option, no secret in it; with it, the password checked and the
// secrets sealed with it, opened back.
func TestConfigFile_Export(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	prompt, err := c.store.PromptByName(ctx, "smc_ict")
	require.NoError(t, err)
	c.agent(t, "llm", "llm", true, func(a *store.Agent) { a.LLM.PromptID = &prompt.ID })
	c.agent(t, "matrend", "trend", false)
	require.NoError(t, c.store.SetTelegram(ctx, boolPtr(false), secret("telegram-token")))
	_, srv := settingsSession(t, c)
	sessions := &accessSessions{t: t, store: c.store, srv: srv, by: map[string]func(string, string, string) (
		*http.Response, string){}}
	admin := sessions.as(ui.AllPermissions()...)

	catalog := catalogOf(t, admin)
	assert.Len(t, catalog, len(configAreas), "every section")
	assert.Equal(t, []string{"llm", "matrend"}, itemKeys(catalog[ui.ConfigSectionAgents]))
	assert.Contains(t, itemKeys(catalog[ui.ConfigSectionPrompts]), "smc_ict")
	assert.Equal(t, []string{ui.ConfigItemDailyLimit, ui.ConfigItemTradingHours, ui.ConfigItemExposure,
		ui.ConfigItemAssetAgents}, itemKeys(catalog[ui.ConfigSectionLimits]))
	assert.Equal(t, []string{ui.ConfigItemTelegramBot}, itemKeys(catalog[ui.ConfigSectionTelegram]))
	assert.True(t, catalog[ui.ConfigSectionAgents].Secrets)
	assert.False(t, catalog[ui.ConfigSectionPrompts].Secrets, "a prompt has none")

	selection := map[string][]string{ui.ConfigSectionAgents: {"llm"}, ui.ConfigSectionLimits: {ui.ConfigItemExposure},
		ui.ConfigSectionTelegram: {ui.ConfigItemTelegramBot}}
	status, file, body := exportOf(t, admin, ui.ConfigExportRequest{Selection: selection})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, ui.ConfigFormat, file.Format)
	assert.Equal(t, ui.ConfigVersion, file.Version)
	assert.Equal(t, store.SchemaVersion, file.AppSchema)
	assert.Equal(t, "user1", file.ExportedBy)
	assert.NotZero(t, file.ExportedAt)
	require.Len(t, file.Agents, 1, "the agent asked only")
	agent := file.Agents[0]
	assert.Equal(t, "llm", agent.Name)
	assert.True(t, agent.Enabled)
	assert.Equal(t, "smc_ict", agent.Prompt, "by name")
	assert.Equal(t, "XAUUSDm", agent.Params["pair"])
	assert.Equal(t, "1", agent.Params["llm-min-stop-atr"], "the params of its model too")
	require.NotNil(t, agent.LLM)
	assert.Equal(t, "gpt-5", agent.LLM.Model)
	assert.Empty(t, file.Prompts)
	require.NotNil(t, file.Limits)
	assert.NotNil(t, file.Limits.Exposure)
	assert.Nil(t, file.Limits.DailyLimit, "not asked")
	assert.Nil(t, file.Supervisor)
	assert.Nil(t, file.Secrets, "not asked")
	for _, secret := range []string{"sk-llm", "bridge-llm", "telegram-token"} {
		assert.NotContains(t, body, secret, "never a secret without the option")
	}

	status, _, _ = exportOf(t, admin, ui.ConfigExportRequest{Selection: selection, IncludeSecrets: true,
		Password: "short"})
	assert.Equal(t, http.StatusBadRequest, status, "a password too short")

	status, file, body = exportOf(t, admin, ui.ConfigExportRequest{Selection: selection, IncludeSecrets: true,
		Password: "the file password"})
	require.Equal(t, http.StatusOK, status, body)
	require.NotNil(t, file.Secrets)
	for _, secret := range []string{"sk-llm", "bridge-llm", "telegram-token"} {
		assert.NotContains(t, body, secret, "sealed")
	}
	values, err := openConfigSecrets(file.Secrets, "the file password")
	require.NoError(t, err)
	assert.Equal(t, configSecretValues{Agents: map[string]configAgentSecrets{
		"llm": {LLMAPIKey: "sk-llm", BridgeToken: "bridge-llm"}}, Telegram: "telegram-token"}, values,
		"the secrets of the items asked only")

	status, file, body = exportOf(t, admin, ui.ConfigExportRequest{
		Selection: map[string][]string{ui.ConfigSectionPrompts: {"smc_ict"}}, IncludeSecrets: true,
		Password: "the file password"})
	require.Equal(t, http.StatusOK, status, body)
	assert.Nil(t, file.Secrets, "none to seal")
	require.Len(t, file.Prompts, 1)
	assert.Equal(t, prompt.Content, file.Prompts[0].Content)
}

// A user sees in the catalog and exports the sections they see only, the
// others left out whatever they ask; the secrets of the sections they manage
// only.
func TestConfigFile_ExportByAccess(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "matrend", "trend", false)
	require.NoError(t, c.store.SetTelegram(ctx, boolPtr(false), secret("telegram-token")))
	_, srv := settingsSession(t, c)
	sessions := &accessSessions{t: t, store: c.store, srv: srv, by: map[string]func(string, string, string) (
		*http.Response, string){}}

	viewer := sessions.as(ui.PermAgentsView, ui.PermTelegramManage)
	catalog := catalogOf(t, viewer)
	assert.Len(t, catalog, 2)
	assert.False(t, catalog[ui.ConfigSectionAgents].Secrets, "seen, not managed")
	assert.True(t, catalog[ui.ConfigSectionTelegram].Secrets)

	everything := map[string][]string{ui.ConfigSectionAgents: {"matrend"}, ui.ConfigSectionPrompts: {"smc_ict"},
		ui.ConfigSectionLimits: {ui.ConfigItemDailyLimit}, ui.ConfigSectionTelegram: {ui.ConfigItemTelegramBot}}
	status, file, body := exportOf(t, viewer, ui.ConfigExportRequest{Selection: everything, IncludeSecrets: true,
		Password: "the file password"})
	require.Equal(t, http.StatusOK, status, body)
	assert.Len(t, file.Agents, 1)
	assert.Empty(t, file.Prompts, "not seen")
	assert.Nil(t, file.Limits, "not seen")
	require.NotNil(t, file.Secrets)
	values, err := openConfigSecrets(file.Secrets, "the file password")
	require.NoError(t, err)
	assert.Equal(t, configSecretValues{Telegram: "telegram-token"}, values, "the agents' left out: not managed")

	panel := sessions.as(ui.PermDashboardView)
	assert.Empty(t, catalogOf(t, panel), "the preferences of the browser only, the web's")
	status, file, _ = exportOf(t, panel, ui.ConfigExportRequest{Selection: everything})
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, file.Agents)
	assert.Nil(t, file.Telegram)

	resp, _ := sessions.as(ui.PermBacktestsView)(http.MethodGet, "/api/config/catalog", "")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// configSessions returns callers of the API of c as users of roles granting
// given permissions, see accessSessions.
func configSessions(t *testing.T, c *managed) *accessSessions {
	t.Helper()
	_, srv := settingsSession(t, c)
	return &accessSessions{t: t, store: c.store, srv: srv, by: map[string]func(string, string, string) (
		*http.Response, string){}}
}

// importOf asks the preview (commit false) or the import of request as call's
// user, and returns its status and its answer.
func importOf(t *testing.T, call func(method, path, body string) (*http.Response, string),
	request ui.ConfigImportRequest, commit bool) (int, ui.ConfigPreview, string) {
	t.Helper()
	payload, err := json.Marshal(request)
	require.NoError(t, err)
	path := "/api/config/import/preview"
	if commit {
		path = "/api/config/import"
	}
	resp, body := call(http.MethodPost, path, string(payload))
	var preview ui.ConfigPreview
	if resp.StatusCode == http.StatusOK {
		require.NoError(t, json.Unmarshal([]byte(body), &preview), body)
	}
	return resp.StatusCode, preview, body
}

// itemOf returns the item of key of section of preview, failing without it.
func itemOf(t *testing.T, preview ui.ConfigPreview, section, key string) ui.ConfigPreviewItem {
	t.Helper()
	for _, s := range preview.Sections {
		if s.Key != section {
			continue
		}
		for _, it := range s.Items {
			if it.Key == key {
				return it
			}
		}
	}
	require.FailNow(t, "no item", "%s/%s in %+v", section, key, preview)
	return ui.ConfigPreviewItem{}
}

// statuses returns the state of every item of preview, by section/key.
func statuses(preview ui.ConfigPreview) map[string]string {
	states := map[string]string{}
	for _, s := range preview.Sections {
		for _, it := range s.Items {
			states[s.Key+"/"+it.Key] = it.Status
		}
	}
	return states
}

// everything returns the selection of every item of the catalog of call's
// user.
func everything(t *testing.T, call func(method, path, body string) (*http.Response, string)) map[string][]string {
	t.Helper()
	selection := map[string][]string{}
	for key, section := range catalogOf(t, call) {
		selection[key] = itemKeys(section)
	}
	return selection
}

// withoutStamp returns file without when and by whom it was exported, nor
// its secrets.
func withoutStamp(file ui.ConfigFile) ui.ConfigFile {
	file.ExportedAt, file.ExportedBy, file.Secrets = 0, "", nil
	return file
}

// A configuration exported with its secrets and imported in another
// installation: every item new, all applied, and the configuration exported
// there the same, the secrets with it; previewed again, every item the same.
func TestConfigFile_ImportRoundTrip(t *testing.T) {
	source := newManaged(t)
	ctx := context.Background()
	account, err := source.store.CreateMT5Account(ctx, store.MT5AccountInput{Name: ptrTo("demo"),
		Login: ptrTo[int64](90807060), Server: ptrTo("Exness-MT5Trial"), BridgeURL: ptrTo("http://127.0.0.1:18813"),
		Password: secret("mt5-password"), BridgeToken: secret("mt5-token")}, false, "",
		store.MT5Change{By: "tests"})
	require.NoError(t, err)
	prompt, err := source.store.CreatePrompt(ctx, "meu_prompt", "o do teste", "Analise o mercado.", "tests")
	require.NoError(t, err)
	llm := source.agent(t, "llm", "llm", false, func(a *store.Agent) { a.LLM.PromptID = &prompt.ID })
	trend := source.agent(t, "matrend", "trend", false, func(a *store.Agent) {
		a.Params["timeframe"] = "M5"
		a.Account.Magic = ptrTo[int64](4242)
	})
	require.NoError(t, source.store.SetAgentMT5Account(ctx, trend.ID, &account.ID, store.MT5Change{By: "tests"}))
	_, _, err = source.store.SetAgentDailyLimit(ctx, llm.ID, ui.DailyLimitSettings{Enabled: true,
		MaxLoss: ptrTo(50.0)}, store.DailyChange{Day: "2026-11-11", Via: store.ViaSettings, By: "tests"})
	require.NoError(t, err)
	_, err = source.store.SetExposureLimits(ctx, ui.ExposureSettings{Pairs: map[string]int{"XAUUSDm": 2}}, "tests")
	require.NoError(t, err)
	_, err = source.store.SetAssetAgents(ctx, ui.AssetAgentsSettings{Rules: []ui.AssetAgentRule{
		{Pairs: []string{"XAUUSDm"}, Agents: []string{"matrend"}}}}, "tests")
	require.NoError(t, err)
	_, err = source.store.CreateRole(ctx, store.RoleChange{Name: ptrTo("Analista"), Description: ptrTo("vê"),
		Permissions: &[]string{ui.PermDashboardView, ui.PermAgentsView}, By: "tests"})
	require.NoError(t, err)
	require.NoError(t, source.store.SetTelegram(ctx, boolPtr(false), secret("telegram-token")))
	_, err = source.store.AddTelegramUser(ctx, 111, "Ray")
	require.NoError(t, err)
	exporter := configSessions(t, source).as(ui.AllPermissions()...)
	status, file, body := exportOf(t, exporter, ui.ConfigExportRequest{Selection: everything(t, exporter),
		IncludeSecrets: true, Password: "the file password"})
	require.Equal(t, http.StatusOK, status, body)

	target := newManaged(t)
	importer := configSessions(t, target).as(ui.AllPermissions()...)
	status, preview, body := importOf(t, importer, ui.ConfigImportRequest{File: file}, false)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, ui.ConfigSecretsLocked, preview.Secrets, "no password")
	assert.False(t, itemOf(t, preview, ui.ConfigSectionAgents, "llm").Secrets, "locked: none written")

	status, preview, body = importOf(t, importer, ui.ConfigImportRequest{File: file, Password: "wrong password"}, false)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, ui.ConfigSecretsLocked, preview.Secrets)
	assert.Contains(t, preview.SecretsError, "Senha incorreta")

	status, preview, body = importOf(t, importer, ui.ConfigImportRequest{File: file, Password: "the file password"},
		false)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, ui.ConfigSecretsUnlocked, preview.Secrets)
	states := statuses(preview)
	for _, key := range []string{"agents/llm", "agents/matrend", "prompts/meu_prompt", "mt5_accounts/demo",
		"roles/Analista", "telegram/111"} {
		assert.Equal(t, ui.ConfigStatusNew, states[key], key)
	}
	assert.Equal(t, ui.ConfigStatusSame, states["prompts/smc_ict"], "the seeds of both")
	assert.Equal(t, ui.ConfigStatusChanged, states["limits/exposure"])
	assert.Equal(t, ui.ConfigStatusChanged, states["telegram/bot"], "its token")
	llmItem := itemOf(t, preview, ui.ConfigSectionAgents, "llm")
	assert.True(t, llmItem.Secrets)
	assert.Contains(t, llmItem.Warnings, "Entra desligado.")
	assert.Empty(t, llmItem.Error)
	assert.Contains(t, itemOf(t, preview, ui.ConfigSectionMT5Accounts, "demo").Warnings[0], "Criada inativa")
	_, err = target.store.AgentByName(ctx, "llm")
	require.ErrorIs(t, err, store.ErrNotFound, "a preview applies nothing")

	status, imported, body := importOf(t, importer, ui.ConfigImportRequest{File: file, Password: "the file password",
		Selection: everything(t, importer)}, true)
	require.Equal(t, http.StatusOK, status, body)
	// the selection of the catalog of the target, the items new missing from it
	assert.False(t, imported.Applied, "nothing picked of the new ones: %s", body)

	selection := map[string][]string{}
	for _, s := range preview.Sections {
		for _, it := range s.Items {
			selection[s.Key] = append(selection[s.Key], it.Key)
		}
	}
	status, imported, body = importOf(t, importer, ui.ConfigImportRequest{File: file, Password: "the file password",
		Selection: selection}, true)
	require.Equal(t, http.StatusOK, status, body)
	require.True(t, imported.Applied, body)

	status, again, body := exportOf(t, importer, ui.ConfigExportRequest{Selection: everything(t, importer),
		IncludeSecrets: true, Password: "another password"})
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, withoutStamp(file), withoutStamp(again), "the same configuration")
	sent, err := openConfigSecrets(file.Secrets, "the file password")
	require.NoError(t, err)
	got, err := openConfigSecrets(again.Secrets, "another password")
	require.NoError(t, err)
	assert.Equal(t, sent, got, "the same secrets")
	assert.Equal(t, "mt5-password", got.MT5Accounts["demo"].Password)

	status, preview, body = importOf(t, importer, ui.ConfigImportRequest{File: file, Password: "the file password"},
		false)
	require.Equal(t, http.StatusOK, status, body)
	for key, state := range statuses(preview) {
		assert.Equal(t, ui.ConfigStatusSame, state, key)
	}
}

// The preview of a file against the settings as they stand: an item changed
// with its fields, a new one, one refused with why, each told; an import of
// the items picked only, all of them or none: one refused picked, nothing
// applied; an agent naming a prompt new in the same file imported with it.
func TestConfigFile_ImportPreviewAndAtomicity(t *testing.T) {
	c := newManaged(t)
	ctx := context.Background()
	c.agent(t, "matrend", "trend", false)
	c.agent(t, "orb", "trend", false)
	sessions := configSessions(t, c)
	admin := sessions.as(ui.AllPermissions()...)
	status, file, body := exportOf(t, admin, ui.ConfigExportRequest{Selection: map[string][]string{
		ui.ConfigSectionAgents: {"matrend", "orb"}}})
	require.Equal(t, http.StatusOK, status, body)

	file.Agents[0].Params["lot"] = "0.02"
	file.Agents[0].Enabled = true
	file.Agents[1].Kind = "llm"
	file.Agents[1].LLM = &ui.ConfigAgentLLM{Provider: "openai", Model: "gpt-5"}
	file.Prompts = []ui.ConfigPrompt{{Name: "novo_prompt", Content: "Analise."}}
	file.Agents = append(file.Agents, ui.ConfigAgent{Name: "analista", Kind: "llm", Mode: "paper",
		APIAddress: "localhost:15999", Launched: true, Params: map[string]string{"pair": "BTCUSDm"},
		Account: &ui.ConfigAgentAccount{BridgeURL: "http://127.0.0.1:18812"},
		LLM:     &ui.ConfigAgentLLM{Provider: "openai", Model: "gpt-5"}, Prompt: "novo_prompt"},
		file.Agents[0])
	status, preview, body := importOf(t, admin, ui.ConfigImportRequest{File: file}, false)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, ui.ConfigSecretsNone, preview.Secrets)
	changed := itemOf(t, preview, ui.ConfigSectionAgents, "matrend")
	assert.Equal(t, ui.ConfigStatusChanged, changed.Status)
	assert.Equal(t, []string{"ligado", "parâmetros (lot)"}, changed.Changes)
	assert.Contains(t, changed.Warnings, "Será ligado: começa a operar na conta.")
	refused := itemOf(t, preview, ui.ConfigSectionAgents, "orb")
	assert.Equal(t, ui.ConfigStatusInvalid, refused.Status)
	assert.Contains(t, refused.Error, "já existe com o tipo trend")
	added := itemOf(t, preview, ui.ConfigSectionAgents, "analista")
	assert.Equal(t, ui.ConfigStatusNew, added.Status)
	assert.Empty(t, added.Error, "its prompt new in the file, applied before it")
	require.Len(t, preview.Sections[1].Items, 4, "the repeated one listed")
	assert.Contains(t, preview.Sections[1].Items[3].Error, "Repetido no arquivo")

	status, imported, body := importOf(t, admin, ui.ConfigImportRequest{File: file, Selection: map[string][]string{
		ui.ConfigSectionAgents: {"matrend", "orb"}}}, true)
	require.Equal(t, http.StatusOK, status, body)
	assert.False(t, imported.Applied, "one refused: none")
	a, err := c.store.AgentByName(ctx, "matrend")
	require.NoError(t, err)
	assert.Equal(t, "0.01", a.Params["lot"], "nothing applied")

	status, imported, body = importOf(t, admin, ui.ConfigImportRequest{File: file, Selection: map[string][]string{
		ui.ConfigSectionAgents: {"analista"}}}, true)
	require.Equal(t, http.StatusOK, status, body)
	assert.False(t, imported.Applied, "its prompt not picked")
	assert.Contains(t, itemOf(t, imported, ui.ConfigSectionAgents, "analista").Error, "novo_prompt")
	require.Len(t, imported.Sections, 1, "the items picked only")

	status, imported, body = importOf(t, admin, ui.ConfigImportRequest{File: file, Selection: map[string][]string{
		ui.ConfigSectionAgents: {"analista", "matrend"}, ui.ConfigSectionPrompts: {"novo_prompt"}}}, true)
	require.Equal(t, http.StatusOK, status, body)
	require.True(t, imported.Applied, body)
	a, err = c.store.AgentByName(ctx, "matrend")
	require.NoError(t, err)
	assert.Equal(t, "0.02", a.Params["lot"])
	assert.True(t, a.Enabled, "on as in the file")
	created, err := c.store.AgentByName(ctx, "analista")
	require.NoError(t, err)
	p, err := c.store.PromptByName(ctx, "novo_prompt")
	require.NoError(t, err)
	assert.Equal(t, p.ID, *created.LLM.PromptID)
	assert.False(t, created.Enabled)
}

// A user imports the sections they manage only: the others refused, told why;
// the routes of the import refused to a user managing none; a file of another
// format or version refused as a whole.
func TestConfigFile_ImportByAccess(t *testing.T) {
	c := newManaged(t)
	c.agent(t, "matrend", "trend", false)
	sessions := configSessions(t, c)
	admin := sessions.as(ui.AllPermissions()...)
	status, file, body := exportOf(t, admin, ui.ConfigExportRequest{Selection: map[string][]string{
		ui.ConfigSectionAgents: {"matrend"}, ui.ConfigSectionPrompts: {"smc_ict"}}})
	require.Equal(t, http.StatusOK, status, body)
	file.Agents[0].Params["lot"] = "0.03"
	file.Prompts[0].Description = "outra"

	agents := sessions.as(ui.PermAgentsManage, ui.PermPromptsView)
	status, preview, body := importOf(t, agents, ui.ConfigImportRequest{File: file}, false)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, ui.ConfigStatusChanged, itemOf(t, preview, ui.ConfigSectionAgents, "matrend").Status)
	prompt := itemOf(t, preview, ui.ConfigSectionPrompts, "smc_ict")
	assert.Equal(t, ui.ConfigStatusInvalid, prompt.Status)
	assert.Equal(t, "Sem permissão para alterar Prompts.", prompt.Error)

	viewer := sessions.as(ui.PermAgentsView, ui.PermPromptsView)
	resp, _ := viewer(http.MethodPost, "/api/config/import/preview", "{}")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	other := file
	other.Format = "profit"
	status, _, body = importOf(t, admin, ui.ConfigImportRequest{File: other}, false)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "não é um arquivo de configuração")
	other = file
	other.Version = ui.ConfigVersion + 1
	status, _, body = importOf(t, admin, ui.ConfigImportRequest{File: other}, false)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "atualize o XTrader")
}

// The fields that change between two items: the empty ones equal to the
// absent ones, the params by name, who changed a setting and when ignored.
func TestFieldChanges(t *testing.T) {
	before := ui.ConfigAgent{Name: "a", Params: map[string]string{"pair": "XAUUSDm", "lot": "0.01"}}
	next := ui.ConfigAgent{Name: "a", Params: map[string]string{"pair": "XAUUSDm", "lot": "0.02", "tf": "M5"},
		Enabled: true, DailyLimit: ui.DailyLimitSettings{UpdatedBy: "admin"}}
	assert.Equal(t, []string{"ligado", "parâmetros (lot, tf)"}, fieldChanges(before, next))
	assert.Empty(t, fieldChanges(ui.ConfigAgent{Params: map[string]string{}}, ui.ConfigAgent{}))
	assert.True(t, sameFields(ui.ConfigTelegram{Users: []ui.ConfigTelegramUser{}}, ui.ConfigTelegram{}))
}
