package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/ui"
)

// An agent disabled and stopped is renamed from the dashboard, its outbox
// with it, the records it holds not sent yet kept; enabled, it isn't, nor to
// a name out of the rules or taken, and a rename failing on its outbox leaves
// the agent as it was.
func TestSettingsAPI_RenameAnAgent(t *testing.T) {
	c := newManaged(t)
	c.manager.agents.Dir = t.TempDir()
	llm := c.agent(t, "llm", "llm", true)
	c.agent(t, "matrend", "trend", false)
	call, _ := settingsSession(t, c)
	outbox := filepath.Join(c.manager.agents.Dir, outboxDir)
	require.NoError(t, os.MkdirAll(outbox, 0o700))
	file := func(name string) string { return filepath.Join(outbox, fmt.Sprintf("%s-%d.db", name, llm.ID)) }
	require.NoError(t, os.WriteFile(file("llm"), []byte("records not sent yet"), 0o600))
	require.NoError(t, os.WriteFile(file("llm")+".lock", nil, 0o600))
	rename := func(name, to string) (*http.Response, string) {
		return call(http.MethodPost, "/api/settings/agents/"+name+"/rename", ui.AgentRenameRequest{Name: to})
	}

	resp, body := rename("llm", "llm-tendencia")
	require.Equal(t, http.StatusConflict, resp.StatusCode, body)
	require.Contains(t, body, "Desligue o agente llm antes.")
	off := false
	resp, body = call(http.MethodPatch, "/api/settings/agents/llm", ui.AgentSettingsRequest{Enabled: &off})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)

	resp, _ = rename("nobody", "x")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = rename("llm", "llm tendencia")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = rename("llm", "matrend")
	require.Equal(t, http.StatusConflict, resp.StatusCode, "a name taken")

	// an outbox of the new name there already: nothing renamed
	require.NoError(t, os.WriteFile(file("llm-tendencia"), []byte("another"), 0o600))
	resp, body = rename("llm", "llm-tendencia")
	require.Equal(t, http.StatusConflict, resp.StatusCode, body)
	_, err := c.store.AgentByName(t.Context(), "llm")
	require.NoError(t, err, "the agent kept its name")
	require.FileExists(t, file("llm"))
	require.NoError(t, os.Remove(file("llm-tendencia")))

	resp, body = rename("llm", "llm-tendencia")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var renamed ui.AgentSettings
	require.NoError(t, json.Unmarshal([]byte(body), &renamed))
	require.Equal(t, "llm-tendencia", renamed.Name)
	data, err := os.ReadFile(file("llm-tendencia"))
	require.NoError(t, err)
	require.Equal(t, "records not sent yet", string(data), "the outbox renamed with its records")
	require.NoFileExists(t, file("llm"))
	require.NoFileExists(t, file("llm")+".lock")

	resp, body = call(http.MethodGet, "/api/settings/agents", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list ui.AgentSettingsResponse
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	var listed []string
	for _, a := range list.Agents {
		listed = append(listed, a.Name)
	}
	require.ElementsMatch(t, []string{"llm-tendencia", "matrend"}, listed)

	resp, body = rename("llm-tendencia", "llm-tendencia")
	require.Equal(t, http.StatusOK, resp.StatusCode, "the same name: nothing to do")
	require.Contains(t, body, `"name":"llm-tendencia"`)
}
