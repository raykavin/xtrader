package controller

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/tucnak/telebot.v2"
)

// testBotToken is the token of the bot of the tests of the redaction, its
// secret after the colon.
const testBotToken = "4242424242:fake-token-of-the-redaction-tests-0042"

// logBuffer collects the lines logged, by any goroutine.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs collects the lines logged until the end of the test.
func captureLogs(t *testing.T) *logBuffer {
	t.Helper()
	logs := &logBuffer{}
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return logs
}

// assertNoToken asserts that text doesn't carry the secret of testBotToken.
func assertNoToken(t *testing.T, text string) {
	t.Helper()
	_, secret, _ := strings.Cut(testBotToken, ":")
	assert.NotContains(t, text, secret)
}

func TestRedactToken(t *testing.T) {
	for _, tc := range []struct {
		text, token, want string
	}{
		{`Post "https://api.telegram.org/bot` + testBotToken + `/getUpdates": EOF`, testBotToken,
			`Post "https://api.telegram.org/bot[redacted]/getUpdates": EOF`},
		{"telegram: Unauthorized (401)", testBotToken, "telegram: Unauthorized (401)"},
		{"", testBotToken, ""},
		// as it is, escaped as in a URL, quoted as in an error of net/url
		{"the token s3cr3t, again s3cr3t", "s3cr3t", "the token [redacted], again [redacted]"},
		{"the token 12%203:abc", "12 3:abc", "the token [redacted]"},
		{`the token 12\n3:abc`, "12\n3:abc", "the token [redacted]"},
		{"the token %2525", "%25", "the token [redacted]"}, // escaped, not the token and 25
		// whatever the token in a URL of the API, and a token anywhere
		{`Get "https://api.telegram.org/file/bot12#3%204/documents/file_1.txt": EOF`, "",
			`Get "https://api.telegram.org/file/bot[redacted]/documents/file_1.txt": EOF`},
		{"the token " + testBotToken + " refused", "", "the token [redacted] refused"},
		{"dial tcp 149.154.167.220:443: connect: connection refused", "",
			"dial tcp 149.154.167.220:443: connect: connection refused"},
	} {
		assert.Equal(t, tc.want, redactToken(tc.text, tc.token), tc.text)
	}
	assert.NoError(t, redactError(nil, testBotToken))
}

// The errors of the requests of telebot carry the token, whatever it is: in
// their URL, escaped, or quoted when the URL can't be parsed.
func TestRedactError_TheRequestsOfTelebot(t *testing.T) {
	offline := "http://" + offlineAddress(t)
	for _, token := range []string{testBotToken, "12 3:abc", "12\n3:abc", `12"3\x`, "12?3 4", "12#3 4", "ñ12:abc"} {
		bot, err := tb.NewBot(tb.Settings{URL: offline, Token: token, Offline: true})
		require.NoError(t, err)
		_, err = bot.Raw("getMe", nil)
		require.Error(t, err)
		if token == testBotToken {
			require.Contains(t, err.Error(), token, "the error of the request carries the token")
		}

		redacted := redactError(err, token).Error()
		assert.Contains(t, redacted, "/bot[redacted]/getMe", "%q: %s", token, err)
		quoted := strconv.Quote(token)
		for _, form := range []string{token, quoted[1 : len(quoted)-1], (&url.URL{Path: token}).EscapedPath()} {
			assert.NotContains(t, redacted, form, "%q: %s", token, err)
		}
	}
}

// The token of the bot is kept out of the logs when the bot can't start:
// refused with an answer carrying it, or the API out of reach.
func TestTelegramRunner_TokenNotLogged(t *testing.T) {
	server := newTelegramServer(t)
	logs := captureLogs(t)
	dashboard, err := NewDashboard(nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runner := NewTelegramRunner(ctx, dashboard)

	failures := 0
	fail := func(method, answer string) {
		t.Helper()
		server.answer(method, answer)
		runner.Apply(TelegramConfig{Enabled: true, Token: testBotToken, Users: []int64{telegramUserID}})
		failures++
		require.Eventually(t, func() bool {
			return strings.Count(logs.String(), "the controller runs without Telegram") == failures
		}, 5*time.Second, 10*time.Millisecond, "%s", logs)
		server.answer(method, "")
	}
	// the path of the request in the description, as a proxy may answer
	fail("getMe", fmt.Sprintf(`{"ok":false,"error_code":401,"description":"Unauthorized: /bot%s/getMe"}`,
		testBotToken))
	fail("getMe", hangUp)
	fail("setMyCommands", hangUp)
	assert.False(t, runner.Running())

	text := logs.String()
	assertNoToken(t, text)
	assert.Contains(t, text, "telegram unknown: Unauthorized: /bot[redacted]/getMe (401)")
	assert.Contains(t, text, server.URL+"/bot[redacted]/getMe")
	assert.Contains(t, text, server.URL+"/bot[redacted]/setMyCommands")
}

// The token of the bot is kept out of the logs when the API fails: at the
// poller, which waits before it polls again, at the greeting, the commands
// and the files of the prompts.
func TestTelegram_TokenNotLogged(t *testing.T) {
	server := newTelegramServer(t)
	logs := captureLogs(t)
	dashboard, err := NewDashboard([]Agent{{Name: "eth", Address: offlineAddress(t)}})
	require.NoError(t, err)
	telegram, err := NewTelegram(dashboard, testBotToken, []int64{telegramUserID},
		WithPrompts(newFakePrompts(), nil))
	require.NoError(t, err)

	for _, method := range []string{"getUpdates", "sendMessage", "getMyCommands", "sendDocument", "file-1"} {
		server.answer(method, hangUp)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	telegram.Start(ctx) // the poller and the greeting
	telegram.handleHelp(command("/help"))
	telegram.handlePrompt(command("/prompt scalp_1m")) // a file: too long for a message
	telegram.handleEditPrompt(message("/editprompt trend_following"))
	telegram.handleDocument(&tb.Message{Sender: &tb.User{ID: telegramUserID},
		Document: &tb.Document{File: tb.File{FileID: "file-1", FileSize: 9}, FileName: "trend_following.txt"}})

	paths := []string{"/bot[redacted]/getUpdates", "/bot[redacted]/sendMessage", "/bot[redacted]/getMyCommands",
		"/bot[redacted]/sendDocument", "/file/bot[redacted]/file-1"}
	require.Eventually(t, func() bool {
		text := logs.String()
		for _, path := range paths {
			if !strings.Contains(text, path) {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond, "every error logged: %s", logs)
	assert.Contains(t, logs.String(), "telegram: the updates: telebot: Post")
	assert.Contains(t, logs.String(), "telegram: the file of a prompt: telebot: Get")
	assert.Never(t, func() bool { return server.calls("getUpdates") > 1 }, 200*time.Millisecond,
		10*time.Millisecond, "polled again at once")
	assertNoToken(t, logs.String())
}
