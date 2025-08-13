package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testKey is the API key of the tests, which must never leave in an error.
const testKey = "sk-test-0123456789abcdef"

// longDecision is a valid ENTER_LONG for testInput, at the price 100.
const longDecision = `{"reason": "Breakout above the range with volume.", "action": "ENTER_LONG",
	"confidence": 0.82, "entry": 100.2, "stop_loss": 98, "take_profit": 104}`

// testProvider is a provider under test: how to create it against a server,
// and the answer of its API holding the JSON of a decision.
type testProvider struct {
	name   string
	path   string
	create func(baseURL string, options ...ProviderOption) (Provider, error)
	answer func(decision string) string
}

var testProviders = []testProvider{
	{
		name: ProviderOpenAI,
		path: "/chat/completions",
		create: func(baseURL string, options ...ProviderOption) (Provider, error) {
			return NewOpenAI("gpt-test", testKey, append([]ProviderOption{WithBaseURL(baseURL)}, options...)...)
		},
		answer: openAIBody,
	},
	{
		name: ProviderAnthropic,
		path: "/messages",
		create: func(baseURL string, options ...ProviderOption) (Provider, error) {
			return NewAnthropic("claude-test", testKey, append([]ProviderOption{WithBaseURL(baseURL)}, options...)...)
		},
		answer: anthropicBody,
	},
}

// openAIBody is an answer of the Chat Completions API whose content is
// decision.
func openAIBody(decision string) string {
	content, _ := json.Marshal(decision)
	return `{"id": "chatcmpl-1", "object": "chat.completion", "model": "gpt-test", "choices": [{"index": 0,
		"message": {"role": "assistant", "content": ` + string(content) + `, "refusal": null},
		"finish_reason": "stop"}], "usage": {"prompt_tokens": 1200, "completion_tokens": 60}}`
}

// anthropicBody is an answer of the Messages API calling the tool of the
// decision with decision.
func anthropicBody(decision string) string {
	return `{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test", "content": [{
		"type": "tool_use", "id": "toolu_1", "name": "trading_decision", "input": ` + decision + `}],
		"stop_reason": "tool_use", "usage": {"input_tokens": 1200, "output_tokens": 60}}`
}

// serve serves handler for the test.
func serve(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// answering serves the same answer to every request.
func answering(t *testing.T, status int, body string, headers ...string) *httptest.Server {
	t.Helper()
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	})
}

// analyze asks the provider of tp served by server for the decision of
// testInput.
func analyze(t *testing.T, tp testProvider, server *httptest.Server, options ...ProviderOption) (Decision, error) {
	t.Helper()
	provider, err := tp.create(server.URL, options...)
	require.NoError(t, err)
	return provider.Analyze(context.Background(), testInput(nil, flatActions...))
}

// requireNoKey fails when err shows the API key.
func requireNoKey(t *testing.T, err error) {
	t.Helper()
	require.NotContains(t, err.Error(), testKey)
	require.NotContains(t, err.Error(), "0123456789")
}

func TestProviders_Decision(t *testing.T) {
	for _, tp := range testProviders {
		t.Run(tp.name, func(t *testing.T) {
			d, err := analyze(t, tp, answering(t, http.StatusOK, tp.answer(longDecision)))
			require.NoError(t, err)
			require.Equal(t, Decision{Action: ActionEnterLong, Confidence: 0.82, Entry: ptr(100.2), StopLoss: ptr(98),
				TakeProfit: ptr(104), Reason: "Breakout above the range with volume."}, d)
		})
	}
}

func TestProviders_InvalidAnswer(t *testing.T) {
	tests := []struct {
		name     string
		decision string
		want     error
		message  string
	}{
		{"not JSON", `"I would buy here."`, ErrInvalidResponse, ""},
		{"a field missing", `{"reason": "ok", "action": "WAIT", "confidence": 0.4, "entry": null,
			"stop_loss": null}`, ErrInvalidResponse, `missing field "take_profit"`},
		{"an unknown action", `{"reason": "ok", "action": "BUY", "confidence": 0.9, "entry": 100,
			"stop_loss": 98, "take_profit": 104}`, ErrInvalidResponse, `unknown action "BUY"`},
		{"a confidence out of range", `{"reason": "ok", "action": "WAIT", "confidence": 42, "entry": null,
			"stop_loss": null, "take_profit": null}`, ErrInvalidResponse, "out of [0, 1]"},
		{"a stop loss above the price of a long", `{"reason": "ok", "action": "ENTER_LONG", "confidence": 0.9,
			"entry": 100, "stop_loss": 101, "take_profit": 104}`, ErrInvalidDecision, "the stop loss 101 of a LONG"},
		{"a take profit below the price of a long", `{"reason": "ok", "action": "ENTER_LONG", "confidence": 0.9,
			"entry": 100, "stop_loss": 98, "take_profit": 99}`, ErrInvalidDecision, "the take profit 99 of a LONG"},
		{"an action not allowed", `{"reason": "ok", "action": "EXIT", "confidence": 0.9, "entry": null,
			"stop_loss": null, "take_profit": null}`, ErrInvalidDecision, "EXIT is not an allowed action"},
	}
	for _, tp := range testProviders {
		for _, tc := range tests {
			t.Run(tp.name+"/"+tc.name, func(t *testing.T) {
				decision := tc.decision
				if tp.name == ProviderAnthropic && !strings.HasPrefix(decision, "{") {
					// the input of a tool is an object: the text goes in a string
					decision = `{"text": ` + decision + `}`
				}
				d, err := analyze(t, tp, answering(t, http.StatusOK, tp.answer(decision)))
				require.ErrorIs(t, err, tc.want)
				require.ErrorContains(t, err, tc.message)
				require.ErrorContains(t, err, tp.name+": ")
				if tc.want == ErrInvalidDecision {
					require.Equal(t, "ok", d.Reason, "the decision refused, its justification kept")
				} else {
					require.Zero(t, d, "no decision without a valid answer")
				}
			})
		}
	}
}

func TestProviders_Timeout(t *testing.T) {
	// the server answers once the client is gone: it sees it go once the body
	// is read
	blocking := func(t *testing.T) *httptest.Server {
		return serve(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		})
	}

	for _, tp := range testProviders {
		t.Run(tp.name+"/deadline of the context", func(t *testing.T) {
			provider, err := tp.create(blocking(t).URL)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			start := time.Now()
			d, err := provider.Analyze(ctx, testInput(nil, flatActions...))
			require.ErrorIs(t, err, ErrTimeout)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NotErrorIs(t, err, ErrUnavailable)
			require.Less(t, time.Since(start), 5*time.Second)
			require.Zero(t, d)
			requireNoKey(t, err)
		})

		t.Run(tp.name+"/timeout of the HTTP client", func(t *testing.T) {
			_, err := analyze(t, tp, blocking(t), WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}))
			require.ErrorIs(t, err, ErrTimeout)
			requireNoKey(t, err)
		})
	}
}

func TestProviders_Unavailable(t *testing.T) {
	for _, tp := range testProviders {
		t.Run(tp.name, func(t *testing.T) {
			server := httptest.NewServer(http.NotFoundHandler())
			server.Close() // nothing listens there anymore

			d, err := analyze(t, tp, server)
			require.ErrorIs(t, err, ErrUnavailable)
			require.NotErrorIs(t, err, ErrTimeout)
			require.Zero(t, d)
			requireNoKey(t, err)
		})
	}
}

// httpErrorCase is an error answer of the API of a provider.
type httpErrorCase struct {
	name       string
	status     int
	body       string
	headers    []string
	want       error // ErrRateLimited or ErrUnavailable, none for an error of the request
	errType    string
	message    string // in the message of the error
	retryAfter time.Duration
}

// testHTTPErrors checks the errors of the API of tp.
func testHTTPErrors(t *testing.T, tp testProvider, tests []httpErrorCase) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := analyze(t, tp, answering(t, tc.status, tc.body, tc.headers...))
			require.Error(t, err)
			require.Zero(t, d, "no decision with an error")
			requireNoKey(t, err)

			var apiErr *APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, tp.name, apiErr.Provider)
			require.Equal(t, tc.status, apiErr.Status)
			require.Equal(t, tc.errType, apiErr.Type)
			require.Contains(t, apiErr.Message, tc.message)
			require.Equal(t, tc.retryAfter, apiErr.RetryAfter)
			require.ErrorContains(t, err, fmt.Sprintf("%s: HTTP %d", tp.name, tc.status))

			for _, kind := range []error{ErrRateLimited, ErrUnavailable, ErrTimeout} {
				if kind == tc.want {
					require.ErrorIs(t, err, kind)
				} else {
					require.NotErrorIs(t, err, kind)
				}
			}
		})
	}
}

func TestNewProvider(t *testing.T) {
	provider, err := NewProvider(" OpenAI ", "gpt-test", testKey)
	require.NoError(t, err)
	require.IsType(t, &OpenAI{}, provider)
	require.Equal(t, "openai gpt-test", fmt.Sprint(provider))

	provider, err = NewProvider("anthropic", "claude-test", testKey, WithMaxTokens(512))
	require.NoError(t, err)
	require.IsType(t, &Anthropic{}, provider)
	require.Equal(t, 512, provider.(*Anthropic).maxTokens)
	require.Equal(t, anthropicBaseURL, provider.(*Anthropic).baseURL)

	_, err = NewProvider("gemini", "gemini-test", testKey)
	require.ErrorContains(t, err, `invalid provider "gemini": openai or anthropic`)

	_, err = NewProvider("openai", " ", testKey)
	require.ErrorContains(t, err, "openai: the model is required")

	_, err = NewProvider("anthropic", "claude-test", "")
	require.ErrorContains(t, err, "anthropic: the API key is required, eg: from ANTHROPIC_API_KEY")

	_, err = NewProvider("openai", "gpt-test", "", WithMaxTokens(100))
	require.ErrorContains(t, err, "from OPENAI_API_KEY")

	_, err = NewProvider("openai", "gpt-test", testKey, WithMaxTokens(0))
	require.ErrorContains(t, err, "the max tokens must be positive")

	provider, err = NewProvider("openai", "gpt-test", testKey, WithBaseURL("http://localhost:8000/v1/"))
	require.NoError(t, err)
	require.Equal(t, "http://localhost:8000/v1", provider.(*OpenAI).baseURL)
}

func TestKeyEnv(t *testing.T) {
	require.Equal(t, "OPENAI_API_KEY", KeyEnv("openai"))
	require.Equal(t, "ANTHROPIC_API_KEY", KeyEnv("Anthropic"))
	require.Empty(t, KeyEnv("gemini"))
}

func TestRetryAfter(t *testing.T) {
	header := func(key, value string) http.Header {
		h := http.Header{}
		h.Set(key, value)
		return h
	}
	require.Equal(t, 7*time.Second, retryAfter(header("Retry-After", "7")))
	require.Equal(t, 1500*time.Millisecond, retryAfter(header("Retry-After-Ms", "1500")))
	require.Zero(t, retryAfter(header("Retry-After", "soon")))
	require.Zero(t, retryAfter(http.Header{}))

	at := retryAfter(header("Retry-After", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)))
	require.InDelta(t, time.Minute, at, float64(2*time.Second))
	require.Zero(t, retryAfter(header("Retry-After", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat))))
}

func TestClient_Redact(t *testing.T) {
	c := client{apiKey: testKey}
	require.Equal(t, "the key [redacted] is invalid", c.redact("the key "+testKey+" is invalid"))
	require.Equal(t, "Incorrect API key provided: [redacted]. See the docs.",
		c.redact("Incorrect API key provided: sk-proj-********************cdef. See the docs."))
	require.Equal(t, "no key here", c.redact("no key here"))

	long := c.redact(strings.Repeat("a", 1000))
	require.Len(t, long, maxMessage+len("..."))
}

func TestAPIError(t *testing.T) {
	err := &APIError{Provider: "openai", Status: 429, Type: "rate_limit_exceeded", Message: "slow down"}
	require.Equal(t, "openai: HTTP 429: rate_limit_exceeded: slow down", err.Error())
	require.True(t, errors.Is(err, ErrRateLimited))

	require.Equal(t, "anthropic: HTTP 502", (&APIError{Provider: "anthropic", Status: 502}).Error())
	require.True(t, errors.Is(&APIError{Status: 502}, ErrUnavailable))
	require.False(t, errors.Is(&APIError{Status: 400}, ErrUnavailable))
	require.False(t, errors.Is(&APIError{Status: 400}, ErrRateLimited))
}

// readRequest reads the headers and the JSON body of the requests the server
// receives.
type readRequest struct {
	header http.Header
	method string
	path   string
	body   map[string]any
	raw    []byte
}

func recording(t *testing.T, answer string) (*httptest.Server, *readRequest) {
	t.Helper()
	got := &readRequest{}
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got.header, got.method, got.path = r.Header.Clone(), r.Method, r.URL.Path
		got.raw, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(got.raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, answer)
	})
	return server, got
}

// The notes and the images of an answer beside the decision are kept with it.
func TestProviders_NotesAndImages(t *testing.T) {
	png := "iVBORw0KGgo=" // the base64 of the signature of a PNG
	content, _ := json.Marshal(longDecision)
	openAI := `{"choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant",
		"content": ` + string(content) + `, "refusal": null, "reasoning": " the range broke with volume ",
		"annotations": [{"type": "url_citation", "url_citation": {"url": "https://news.test/a", "title": "CPI"}}],
		"images": [{"type": "image_url", "image_url": {"url": "data:image/png;base64,` + png + `"}},
			{"type": "image_url", "image_url": {"url": "https://img.test/chart.png"}},
			{"type": "image_url", "image_url": {"url": "http://img.test/plain.png"}}]}}],
		"usage": {"prompt_tokens": 1200, "completion_tokens": 60}}`
	d, err := analyze(t, testProviders[0], answering(t, http.StatusOK, openAI))
	require.NoError(t, err)
	require.Equal(t, []Note{{Kind: "reasoning", Text: "the range broke with volume"},
		{Kind: "citation", Title: "CPI", URL: "https://news.test/a"},
		{Kind: "image", URL: "https://img.test/chart.png"}}, d.Notes, "an image of plain HTTP left out")
	require.Len(t, d.Attachments, 1)
	require.Equal(t, "image/png", d.Attachments[0].MediaType)
	require.Equal(t, []byte("\x89PNG\r\n\x1a\n"), d.Attachments[0].Data)

	anthropic := `{"content": [{"type": "thinking", "thinking": "a support twice"},
		{"type": "text", "text": "The range broke."},
		{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "` + png + `"}},
		{"type": "tool_use", "id": "toolu_1", "name": "trading_decision", "input": ` + longDecision + `}],
		"stop_reason": "tool_use", "usage": {"input_tokens": 1200, "output_tokens": 60}}`
	d, err = analyze(t, testProviders[1], answering(t, http.StatusOK, anthropic))
	require.NoError(t, err)
	require.Equal(t, []Note{{Kind: "reasoning", Text: "a support twice"}, {Kind: "text", Text: "The range broke."}},
		d.Notes)
	require.Len(t, d.Attachments, 1)

	// a decision refused keeps them too, for its record
	refused := strings.Replace(openAI, `\"stop_loss\": 98`, `\"stop_loss\": 101`, 1)
	d, err = analyze(t, testProviders[0], answering(t, http.StatusOK, refused))
	require.ErrorIs(t, err, ErrInvalidDecision)
	require.Len(t, d.Notes, 3)
}
