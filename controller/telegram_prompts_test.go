package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/tucnak/telebot.v2"

	"github.com/raykavin/xtrader/store"
)

// fakePrompts are the prompts and the agents of the tests of the bot.
type fakePrompts struct {
	mu       sync.Mutex
	prompts  map[string]*store.Prompt
	agents   map[string]store.Agent
	edits    []string // who made each edit
	selected map[int64]*int64
	fails    map[string]error  // the error of the next call of a method, by its name
	befores  map[string]func() // run before the next call of a method, by its name
}

// failNext fails the next call of method with err.
func (f *fakePrompts) failNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails == nil {
		f.fails = make(map[string]error)
	}
	f.fails[method] = err
}

// beforeNext runs fn before the next call of method.
func (f *fakePrompts) beforeNext(method string, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.befores == nil {
		f.befores = make(map[string]func())
	}
	f.befores[method] = fn
}

// call runs what failNext and beforeNext set for this call of method,
// returning its error.
func (f *fakePrompts) call(method string) error {
	f.mu.Lock()
	err, before := f.fails[method], f.befores[method]
	delete(f.fails, method)
	delete(f.befores, method)
	f.mu.Unlock()
	if before != nil {
		before()
	}
	return err
}

func newFakePrompts() *fakePrompts {
	trendFollowing := int64(3)
	llm := store.Agent{Model: store.Model{ID: 1}, Name: "llm", Kind: "llm", Enabled: true, TelegramControl: true,
		LLM: &store.LLMSettings{AgentID: 1, PromptID: &trendFollowing}}
	trend := store.Agent{Model: store.Model{ID: 2}, Name: "matrend", Kind: "trend"}
	return &fakePrompts{
		prompts: map[string]*store.Prompt{
			"trend_following": {Model: store.Model{ID: 3}, Name: "trend_following", Version: 1,
				Content: "# Take profit: none\n\nSetup: the trend"},
			"scalp_1m": {Model: store.Model{ID: 4}, Name: "scalp_1m", Version: 2,
				Content: "Setup: " + strings.Repeat("the scalp. ", 500)},
		},
		agents:   map[string]store.Agent{"llm": llm, "matrend": trend},
		selected: make(map[int64]*int64),
	}
}

func (f *fakePrompts) Prompts(context.Context) ([]store.PromptSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []store.PromptSummary{{Prompt: *f.prompts["scalp_1m"], UsedBy: []string{}},
		{Prompt: *f.prompts["trend_following"], UsedBy: []string{"llm"}}}, nil
}

func (f *fakePrompts) PromptByName(_ context.Context, name string) (store.Prompt, error) {
	if err := f.call("PromptByName"); err != nil {
		return store.Prompt{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.prompts[name]; ok {
		return *p, nil
	}
	return store.Prompt{}, fmt.Errorf("%w: prompt %s", store.ErrNotFound, name)
}

func (f *fakePrompts) UpdatePrompt(_ context.Context, id int64, edit store.PromptEdit, by string) (store.Prompt,
	error) {
	if err := f.call("UpdatePrompt"); err != nil {
		return store.Prompt{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.prompts {
		if p.ID != id {
			continue
		}
		if edit.Version > 0 && edit.Version != p.Version {
			return store.Prompt{}, fmt.Errorf("%w: the prompt %s was edited meanwhile, version %d now",
				store.ErrConflict, p.Name, p.Version)
		}
		if strings.TrimSpace(*edit.Content) == "" {
			return store.Prompt{}, fmt.Errorf("%w: the prompt %s is empty", store.ErrInvalid, p.Name)
		}
		if *edit.Content != p.Content {
			p.Content, p.Version = *edit.Content, p.Version+1
		}
		f.edits = append(f.edits, by)
		return *p, nil
	}
	return store.Prompt{}, store.ErrNotFound
}

func (f *fakePrompts) AgentByName(_ context.Context, name string) (store.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[name]; ok {
		return a, nil
	}
	return store.Agent{}, fmt.Errorf("%w: agent %s", store.ErrNotFound, name)
}

func (f *fakePrompts) Agents(context.Context) ([]store.Agent, error) {
	if err := f.call("Agents"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	agents := make([]store.Agent, 0, len(f.agents))
	for _, a := range f.agents {
		agents = append(agents, a)
	}
	return agents, nil
}

// setAgent changes the agent of name with change.
func (f *fakePrompts) setAgent(name string, change func(*store.Agent)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[name]
	change(&a)
	f.agents[name] = a
}

// version returns the version of the prompt of name.
func (f *fakePrompts) version(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompts[name].Version
}

func (f *fakePrompts) SetAgentPrompt(_ context.Context, id int64, promptID *int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selected[id] = promptID
	return nil
}

// promptsTelegram creates a bot serving the fake prompts, and counts the
// changes it tells.
func promptsTelegram(t *testing.T) (*Telegram, *telegramServer, *fakePrompts, *atomic.Int32) {
	t.Helper()
	prompts := newFakePrompts()
	var changes atomic.Int32
	telegram, server := newTestTelegramWith(t, nil, []TelegramOption{WithPrompts(prompts,
		func() { changes.Add(1) })})
	return telegram, server, prompts, &changes
}

// message is a message of the user, not a command.
func message(text string) *tb.Message {
	return &tb.Message{Text: text, Sender: &tb.User{ID: telegramUserID}}
}

func TestTelegram_OnlyTheUsersAuthorized(t *testing.T) {
	telegram, server := newTestTelegram(t)
	update := func(id int64) *tb.Update {
		return &tb.Update{Message: &tb.Message{Text: "/agents", Sender: &tb.User{ID: id}}}
	}
	assert.True(t, telegram.authorized(update(telegramUserID)))
	assert.False(t, telegram.authorized(update(43)))
	assert.False(t, telegram.authorized(&tb.Update{}), "no message")

	telegram.SetUsers([]int64{telegramUserID, 43})
	assert.True(t, telegram.authorized(update(43)), "several users")
	telegram.notify("hello")
	sent := server.sent()
	require.Len(t, sent, 2, "every user notified")
	assert.ElementsMatch(t, []string{"42", "43"}, []string{sent[0].ChatID, sent[1].ChatID})

	telegram.SetUsers([]int64{43})
	assert.False(t, telegram.authorized(update(telegramUserID)), "no longer authorized")
}

func TestTelegram_ListAndShowThePrompts(t *testing.T) {
	telegram, server, _, _ := promptsTelegram(t)

	telegram.handlePrompts(command("/prompts"))
	list := server.last(t).Text
	assert.Contains(t, list, "• trend_following v1 — used by llm")
	assert.Contains(t, list, "• scalp_1m v2\n")

	telegram.handlePrompt(command("/prompt trend_following"))
	assert.Equal(t, "trend_following v1\n\n# Take profit: none\n\nSetup: the trend", server.last(t).Text)

	telegram.handlePrompt(command("/prompt scalp_1m"))
	documents := server.sentDocuments()
	require.Len(t, documents, 1, "too long for a message: a file")
	assert.Equal(t, "scalp_1m.txt", documents[0].Name)
	assert.Equal(t, "scalp_1m v2", documents[0].Caption)
	assert.True(t, strings.HasPrefix(documents[0].Content, "Setup: the scalp."))

	telegram.handlePrompt(command("/prompt nothing"))
	assert.Equal(t, "⚠️ not found: prompt nothing", server.last(t).Text)
	telegram.handlePrompt(command("/prompt"))
	assert.Contains(t, server.last(t).Text, "Name the prompt")
}

func TestTelegram_EditAPrompt(t *testing.T) {
	telegram, server, prompts, changes := promptsTelegram(t)

	// its content on the lines after the command, shown, then confirmed
	telegram.handleEditPrompt(message("/editprompt trend_following\n# Take profit: none\n\nSetup: v2"))
	shown := server.last(t).Text
	assert.Contains(t, shown, "New content of trend_following v1: 3 lines, 30 bytes (now 3 lines, 37 bytes)")
	assert.Contains(t, shown, "# Take profit: none\n\nSetup: v2\n\n/confirm saves it")
	assert.Contains(t, shown, "the LLM agents using it (llm) switch to it")
	assert.Equal(t, 1, prompts.version("trend_following"), "not saved before its confirmation")
	telegram.handleConfirm(command("/confirm"))
	assert.Contains(t, server.last(t).Text, "✅ trend_following saved, v2")
	assert.Equal(t, "# Take profit: none\n\nSetup: v2", prompts.prompts["trend_following"].Content)
	assert.Equal(t, []string{"telegram:42"}, prompts.edits)
	assert.EqualValues(t, 1, changes.Load(), "the manager told")
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, "Nothing to confirm.", server.last(t).Text)

	// in the next message
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	assert.Contains(t, server.last(t).Text, "Send the new content of trend_following (now v2)")
	assert.Contains(t, server.last(t).Text, "within 10 minutes")
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, "Send the new content of trend_following first, or /cancel.", server.last(t).Text)
	telegram.handleText(message("Setup: v3,\non two lines"))
	assert.Contains(t, server.last(t).Text, "New content of trend_following v2: 2 lines")
	telegram.handleText(message("another text"))
	assert.Contains(t, server.last(t).Text, "awaits your confirmation")
	telegram.handleConfirm(command("/confirm"))
	assert.Contains(t, server.last(t).Text, "✅ trend_following saved, v3")
	assert.Equal(t, "Setup: v3,\non two lines", prompts.prompts["trend_following"].Content, "the whole text")
	telegram.handleText(message("a message after the edit"))
	assert.Equal(t, 3, prompts.version("trend_following"), "not taken for a prompt")

	// as a file
	server.userFile("file-1", "Setup: v4 of a file")
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleDocument(&tb.Message{Sender: &tb.User{ID: telegramUserID},
		Document: &tb.Document{File: tb.File{FileID: "file-1", FileSize: 19}, FileName: "trend_following.txt"}})
	assert.Contains(t, server.last(t).Text, "New content of trend_following v3: 1 lines, 19 bytes")
	telegram.handleConfirm(command("/confirm"))
	assert.Contains(t, server.last(t).Text, "✅ trend_following saved, v4")
	assert.Equal(t, "Setup: v4 of a file", prompts.prompts["trend_following"].Content)

	// given up, refused, unchanged
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleCancel(command("/cancel"))
	assert.Equal(t, "trend_following kept as it was.", server.last(t).Text)
	telegram.handleText(message("Setup: v5"))
	assert.Equal(t, 4, prompts.version("trend_following"))
	telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v5"))
	telegram.handleCancel(command("/cancel"))
	assert.Equal(t, "trend_following kept as it was.", server.last(t).Text, "given up once shown")
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, 4, prompts.version("trend_following"))
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleText(message("   "))
	assert.Equal(t, "⚠️ The content is empty: send the one of trend_following, or /cancel.", server.last(t).Text)
	telegram.handleCancel(command("/cancel"))
	telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v4 of a file"))
	assert.Equal(t, "trend_following unchanged: the same content (v4).", server.last(t).Text)
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, "Nothing to confirm.", server.last(t).Text)
	telegram.handleEditPrompt(message("/editprompt nothing"))
	assert.Equal(t, "⚠️ not found: prompt nothing", server.last(t).Text)
	telegram.handleDocument(&tb.Message{Sender: &tb.User{ID: telegramUserID},
		Document: &tb.Document{File: tb.File{FileID: "file-1"}}})
	assert.Equal(t, 4, prompts.version("trend_following"), "a file without an edit asked")
	assert.EqualValues(t, 3, changes.Load(), "only the saves")
}

func TestTelegramEditPrompt_MistypedCommandNotTaken(t *testing.T) {
	telegram, server, prompts, changes := promptsTelegram(t)

	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleText(message("/stauts"))
	assert.Equal(t, "⚠️ /stauts isn't a command of the bot, nor taken for the content of trend_following: send "+
		"its content, not starting with /, or /cancel to give up.", server.last(t).Text)
	telegram.handleText(message("  /stauts now\nplease"))
	assert.Contains(t, server.last(t).Text, "⚠️ /stauts isn't a command of the bot")
	assert.Equal(t, 1, prompts.version("trend_following"), "never saved")
	assert.Empty(t, prompts.edits)

	// the edit goes on
	telegram.handleText(message("Setup: v2"))
	assert.Contains(t, server.last(t).Text, "New content of trend_following v1")
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, 2, prompts.version("trend_following"))
	assert.EqualValues(t, 1, changes.Load())
}

func TestTelegramEditPrompt_SavedOnlyConfirmed(t *testing.T) {
	telegram, server, prompts, changes := promptsTelegram(t)

	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleText(message("Setup: any text sent later"))
	assert.Equal(t, 1, prompts.version("trend_following"), "shown, not saved")
	assert.Empty(t, prompts.edits)
	assert.Zero(t, changes.Load())

	// edited meanwhile, eg: on the dashboard: the confirmation refused
	edited := "Setup: from the dashboard"
	_, err := prompts.UpdatePrompt(context.Background(), 3, store.PromptEdit{Content: &edited}, "web:xtrader")
	require.NoError(t, err)
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, "⚠️ trend_following was edited meanwhile, v2 now: nothing saved, /editprompt trend_following "+
		"starts again.", server.last(t).Text)
	assert.Equal(t, edited, prompts.prompts["trend_following"].Content, "kept")
	assert.Zero(t, changes.Load())

	// the content sent once edited meanwhile
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	v3 := "Setup: v3"
	_, err = prompts.UpdatePrompt(context.Background(), 3, store.PromptEdit{Content: &v3}, "web:xtrader")
	require.NoError(t, err)
	telegram.handleText(message("Setup: from Telegram"))
	assert.Contains(t, server.last(t).Text, "⚠️ trend_following was edited meanwhile, v3 now")
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, "Nothing to confirm.", server.last(t).Text)
	assert.Equal(t, "Setup: v3", prompts.prompts["trend_following"].Content)
}

// B33: a failure for now of the database at /confirm (reading the prompt, its
// agents, or saving it) keeps the new content: /confirm tries again, nothing
// lost; a refusal for good gives it up.
func TestTelegramEditPrompt_ConfirmFailingForNowKeepsTheContent(t *testing.T) {
	for _, method := range []string{"PromptByName", "Agents", "UpdatePrompt"} {
		t.Run(method, func(t *testing.T) {
			telegram, server, prompts, changes := promptsTelegram(t)
			telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v2"))
			prompts.failNext(method, errors.New("pq: the database system is starting up"))
			telegram.handleConfirm(command("/confirm"))
			assert.Equal(t, "⚠️ The prompts can't be read or changed now: nothing saved. The new content of "+
				"trend_following is kept: /confirm tries again, /cancel gives up.", server.last(t).Text)
			assert.Equal(t, 1, prompts.version("trend_following"))
			assert.Zero(t, changes.Load())

			telegram.handleConfirm(command("/confirm"))
			assert.Contains(t, server.last(t).Text, "✅ trend_following saved, v2")
			assert.Equal(t, "Setup: v2", prompts.prompts["trend_following"].Content)
			assert.EqualValues(t, 1, changes.Load())
		})
	}

	t.Run("refused for good", func(t *testing.T) {
		telegram, server, prompts, changes := promptsTelegram(t)
		telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v2"))
		prompts.failNext("UpdatePrompt", fmt.Errorf("%w: the header of the prompt", store.ErrInvalid))
		telegram.handleConfirm(command("/confirm"))
		assert.Equal(t, "⚠️ invalid: the header of the prompt", server.last(t).Text)
		telegram.handleConfirm(command("/confirm"))
		assert.Equal(t, "Nothing to confirm.", server.last(t).Text, "given up")
		assert.Equal(t, 1, prompts.version("trend_following"))
		assert.Zero(t, changes.Load())
	})
}

// B33: the prompt edited between the read of /confirm and its save: the
// conflict tells the version now, not the one read before the save.
func TestTelegramEditPrompt_ConflictTellsTheVersionNow(t *testing.T) {
	telegram, server, prompts, changes := promptsTelegram(t)
	telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: from Telegram"))
	edited := "Setup: from the dashboard"
	prompts.beforeNext("UpdatePrompt", func() {
		_, err := prompts.UpdatePrompt(context.Background(), 3, store.PromptEdit{Content: &edited}, "web:xtrader")
		require.NoError(t, err)
	})
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, "⚠️ trend_following was edited meanwhile, v2 now: nothing saved, /editprompt trend_following "+
		"starts again.", server.last(t).Text)
	assert.Equal(t, edited, prompts.prompts["trend_following"].Content, "kept")
	assert.Zero(t, changes.Load())
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, "Nothing to confirm.", server.last(t).Text, "given up")
}

func TestTelegramEditPrompt_Expires(t *testing.T) {
	telegram, server, prompts, _ := promptsTelegram(t)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	telegram.now = func() time.Time { return now }

	// the content awaited for promptEditTTL
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	now = now.Add(promptEditTTL)
	telegram.handleText(message("Setup: a text an hour later"))
	assert.Equal(t, "The edit of trend_following expired after 10 minutes: nothing saved, /editprompt "+
		"trend_following starts again.", server.last(t).Text)
	telegram.handleText(message("Setup: again"))
	assert.Equal(t, 1, prompts.version("trend_following"))
	assert.Contains(t, server.last(t).Text, "expired", "told once")

	// the confirmation awaited for promptEditTTL from the content shown
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	now = now.Add(promptEditTTL - time.Second)
	telegram.handleText(message("Setup: v2"))
	now = now.Add(promptEditTTL - time.Second)
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, 2, prompts.version("trend_following"), "within its time")

	telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v3"))
	now = now.Add(promptEditTTL)
	telegram.handleConfirm(command("/confirm"))
	assert.Contains(t, server.last(t).Text, "The edit of trend_following expired after 10 minutes")
	assert.Equal(t, 2, prompts.version("trend_following"), "expired: nothing saved")
	telegram.handleCancel(command("/cancel"))
	assert.Equal(t, "Nothing to cancel.", server.last(t).Text)
}

func TestTelegramEditPrompt_RequiresTheControlsOfItsAgents(t *testing.T) {
	telegram, server, prompts, changes := promptsTelegram(t)
	other := int64(3)
	prompts.agents["llm2"] = store.Agent{Model: store.Model{ID: 5}, Name: "llm2", Kind: "llm", Enabled: true,
		LLM: &store.LLMSettings{AgentID: 5, PromptID: &other}}
	prompts.agents["llm3"] = store.Agent{Model: store.Model{ID: 6}, Name: "llm3", Kind: "llm", Enabled: false,
		LLM: &store.LLMSettings{AgentID: 6, PromptID: &other}} // disabled: not in the way

	telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v2"))
	assert.Equal(t, "⚠️ trend_following is used by agents with their Telegram controls off: llm2. Edit it on "+
		"the dashboard, or turn their controls on in their settings.", server.last(t).Text)
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	assert.Contains(t, server.last(t).Text, "Telegram controls off: llm2")
	telegram.handleText(message("Setup: v2"))
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, 1, prompts.version("trend_following"), "refused")

	// a prompt no agent enabled uses with the controls off
	telegram.handleEditPrompt(message("/editprompt scalp_1m\nSetup: v3"))
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, 3, prompts.version("scalp_1m"))

	// turned on: allowed; turned off between the content and its confirmation: refused
	prompts.setAgent("llm2", func(a *store.Agent) { a.TelegramControl = true })
	telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v2"))
	prompts.setAgent("llm", func(a *store.Agent) { a.TelegramControl = false })
	telegram.handleConfirm(command("/confirm"))
	assert.Contains(t, server.last(t).Text, "Telegram controls off: llm.")
	assert.Equal(t, 1, prompts.version("trend_following"))
	prompts.setAgent("llm", func(a *store.Agent) { a.TelegramControl = true })
	telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v2"))
	telegram.handleConfirm(command("/confirm"))
	assert.Equal(t, 2, prompts.version("trend_following"))
	assert.EqualValues(t, 2, changes.Load())
}

func TestTelegramUsePrompt_RequiresTheControlsOfTheAgent(t *testing.T) {
	telegram, server, prompts, changes := promptsTelegram(t)
	prompts.setAgent("llm", func(a *store.Agent) { a.TelegramControl = false })

	telegram.handleUsePrompt(command("/useprompt llm scalp_1m"))
	assert.Equal(t, "⚠️ [llm] The Telegram controls of the agent are off: turn them on in the settings of the "+
		"dashboard.", server.last(t).Text)
	_, selected := prompts.selected[1]
	assert.False(t, selected, "kept")
	assert.Zero(t, changes.Load())

	prompts.setAgent("llm", func(a *store.Agent) { a.TelegramControl = true })
	telegram.handleUsePrompt(command("/useprompt llm scalp_1m"))
	assert.Contains(t, server.last(t).Text, "✅ [llm] switches to scalp_1m v2")
}

func TestTelegram_UseAPrompt(t *testing.T) {
	telegram, server, prompts, changes := promptsTelegram(t)

	telegram.handleUsePrompt(command("/useprompt llm scalp_1m"))
	assert.Contains(t, server.last(t).Text, "✅ [llm] switches to scalp_1m v2 on its next analysis")
	require.NotNil(t, prompts.selected[1])
	assert.EqualValues(t, 4, *prompts.selected[1])
	assert.EqualValues(t, 1, changes.Load())

	telegram.handleUsePrompt(command("/useprompt llm default"))
	assert.Contains(t, server.last(t).Text, "switches to its default prompt")
	assert.Nil(t, prompts.selected[1])

	telegram.handleUsePrompt(command("/useprompt matrend scalp_1m"))
	assert.Equal(t, "⚠️ matrend doesn't trade with a language model: it has no prompt.", server.last(t).Text)
	telegram.handleUsePrompt(command("/useprompt nobody scalp_1m"))
	assert.Equal(t, "⚠️ not found: agent nobody", server.last(t).Text)
	telegram.handleUsePrompt(command("/useprompt llm"))
	assert.Contains(t, server.last(t).Text, "Name the agent and the prompt")
	assert.EqualValues(t, 2, changes.Load(), "only the switches")
}

func TestTelegramRunner_AppliesTheSettings(t *testing.T) {
	server := newTelegramServer(t)
	dashboard, err := NewDashboard([]Agent{{Name: "eth", Address: offlineAddress(t)}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runner := NewTelegramRunner(ctx, dashboard)
	greetings := func() int {
		count := 0
		for _, m := range server.sent() {
			if strings.HasPrefix(m.Text, "Controller initialized") {
				count++
			}
		}
		return count
	}

	runner.Apply(TelegramConfig{Enabled: true, Token: "token", Users: nil})
	assert.False(t, runner.Running(), "nobody to answer")
	runner.Apply(TelegramConfig{Enabled: true, Token: "token", Users: []int64{telegramUserID}})
	require.Eventually(t, runner.Running, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return greetings() == 1 }, 5*time.Second, 10*time.Millisecond)

	runner.Apply(TelegramConfig{Enabled: true, Token: "token", Users: []int64{telegramUserID, 43}})
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, greetings(), "the users changed without a restart")
	runner.mu.Lock()
	users := *runner.bot.users.Load()
	runner.mu.Unlock()
	assert.Equal(t, []int64{telegramUserID, 43}, users)

	runner.Apply(TelegramConfig{Enabled: false, Token: "token", Users: []int64{telegramUserID}})
	assert.False(t, runner.Running(), "disabled")

	// a token refused: tried again at the next Apply
	server.answer("getMe", `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	runner.Apply(TelegramConfig{Enabled: true, Token: "revoked", Users: []int64{telegramUserID}})
	time.Sleep(100 * time.Millisecond)
	assert.False(t, runner.Running())
	server.answer("getMe", "")
	runner.Apply(TelegramConfig{Enabled: true, Token: "renewed", Users: []int64{telegramUserID}})
	require.Eventually(t, runner.Running, 5*time.Second, 10*time.Millisecond, "the new token")
}
