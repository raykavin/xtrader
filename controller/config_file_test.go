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
