package controller

import (
	"context"
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
}

func newFakePrompts() *fakePrompts {
	llm := store.Agent{Model: store.Model{ID: 1}, Name: "llm", Kind: "llm", LLM: &store.LLMSettings{AgentID: 1}}
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.prompts[name]; ok {
		return *p, nil
	}
	return store.Prompt{}, fmt.Errorf("%w: prompt %s", store.ErrNotFound, name)
}

func (f *fakePrompts) UpdatePrompt(_ context.Context, id int64, edit store.PromptEdit, by string) (store.Prompt,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.prompts {
		if p.ID != id {
			continue
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

	// its content on the lines after the command
	telegram.handleEditPrompt(message("/editprompt trend_following\n# Take profit: none\n\nSetup: v2"))
	assert.Contains(t, server.last(t).Text, "✅ trend_following saved, v2")
	assert.Equal(t, "# Take profit: none\n\nSetup: v2", prompts.prompts["trend_following"].Content)
	assert.Equal(t, []string{"telegram:42"}, prompts.edits)
	assert.EqualValues(t, 1, changes.Load(), "the manager told")

	// in the next message
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	assert.Contains(t, server.last(t).Text, "Send the new content of trend_following (now v2)")
	telegram.handleText(message("Setup: v3,\non two lines"))
	assert.Contains(t, server.last(t).Text, "✅ trend_following saved, v3")
	assert.Equal(t, "Setup: v3,\non two lines", prompts.prompts["trend_following"].Content, "the whole text")
	telegram.handleText(message("a message after the edit"))
	assert.Equal(t, 3, prompts.prompts["trend_following"].Version, "not taken for a prompt")

	// as a file
	server.userFile("file-1", "Setup: v4 of a file")
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleDocument(&tb.Message{Sender: &tb.User{ID: telegramUserID},
		Document: &tb.Document{File: tb.File{FileID: "file-1", FileSize: 19}, FileName: "trend_following.txt"}})
	assert.Contains(t, server.last(t).Text, "✅ trend_following saved, v4")
	assert.Equal(t, "Setup: v4 of a file", prompts.prompts["trend_following"].Content)

	// given up, refused, unchanged
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleCancel(command("/cancel"))
	assert.Equal(t, "trend_following kept as it was.", server.last(t).Text)
	telegram.handleText(message("Setup: v5"))
	assert.Equal(t, 4, prompts.prompts["trend_following"].Version)
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleText(message("   "))
	assert.Equal(t, "⚠️ invalid: the prompt trend_following is empty", server.last(t).Text)
	telegram.handleEditPrompt(message("/editprompt trend_following\nSetup: v4 of a file"))
	assert.Equal(t, "trend_following unchanged: the same content (v4).", server.last(t).Text)
	telegram.handleEditPrompt(message("/editprompt nothing"))
	assert.Equal(t, "⚠️ not found: prompt nothing", server.last(t).Text)
	telegram.handleDocument(&tb.Message{Sender: &tb.User{ID: telegramUserID},
		Document: &tb.Document{File: tb.File{FileID: "file-1"}}})
	assert.Equal(t, 4, prompts.prompts["trend_following"].Version, "a file without an edit asked")
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
