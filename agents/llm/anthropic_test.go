package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnthropic_Request(t *testing.T) {
	server, got := recording(t, anthropicBody(longDecision))
	provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL), WithMaxTokens(1000))
	require.NoError(t, err)

	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)

	require.Equal(t, http.MethodPost, got.method)
	require.Equal(t, "/messages", got.path)
	require.Equal(t, testKey, got.header.Get("X-Api-Key"))
	require.Equal(t, "2023-06-01", got.header.Get("Anthropic-Version"))
	require.Empty(t, got.header.Get("Authorization"))

	// the answer is forced into the tool of the decision
	var request anthropicRequest
	require.NoError(t, json.Unmarshal(got.raw, &request))
	require.Equal(t, "claude-test", request.Model)
	require.Equal(t, 1000, request.MaxTokens)
	require.Equal(t, defaultSystemPrompt, request.System)
	require.Len(t, request.Tools, 1)
	require.Equal(t, "trading_decision", request.Tools[0].Name)
	require.JSONEq(t, string(decisionSchema), string(request.Tools[0].InputSchema))
	require.Equal(t, anthropicToolChoice{Type: "tool", Name: "trading_decision"}, request.ToolChoice)

	require.Len(t, request.Messages, 1)
	require.Equal(t, "user", request.Messages[0].Role)
	context, ok := strings.CutPrefix(request.Messages[0].Content, "Market context of the candle just closed:\n")
	require.True(t, ok)
	var in map[string]any
	require.NoError(t, json.Unmarshal([]byte(context), &in))
	require.Equal(t, "XAUUSD", in["symbol"])
}

// AnalyzeWith sends the instructions it is given, the provider keeping its own.
func TestAnthropic_AnalyzeWith(t *testing.T) {
	server, got := recording(t, anthropicBody(longDecision))
	provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)
	var _ SystemPromptProvider = provider

	system := systemPrompt("# x\n\nStrategy X.")
	_, err = provider.AnalyzeWith(context.Background(), system, testInput(nil, flatActions...))
	require.NoError(t, err)
	var request anthropicRequest
	require.NoError(t, json.Unmarshal(got.raw, &request))
	require.Equal(t, system, request.System)
	require.Equal(t, defaultSystemPrompt, provider.system)
}

// A model that refuses the forced call of the tool, eg: claude-opus-5-5: the
// request asked again with the tool left to it, asked to answer with it, as
// the next ones.
func TestAnthropic_ForcedToolRefused(t *testing.T) {
	var choices []anthropicToolChoice
	var messages []string
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		var request anthropicRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		choices = append(choices, request.ToolChoice)
		messages = append(messages, request.Messages[0].Content)
		w.Header().Set("Content-Type", "application/json")
		if request.ToolChoice.Type != "auto" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type": "error", "error": {"type": "invalid_request_error",
				"message": "tool_choice: type \"tool\" and \"any\" are not supported for this model."}}`))
			return
		}
		_, _ = w.Write([]byte(anthropicBody(longDecision)))
	})
	provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)

	d, err := provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	require.Equal(t, ActionEnterLong, d.Action)
	require.Equal(t, []anthropicToolChoice{{Type: "tool", Name: "trading_decision"}, {Type: "auto"}}, choices)
	require.False(t, strings.HasSuffix(messages[0], answerWithTheTool))
	require.True(t, strings.HasSuffix(messages[1], answerWithTheTool), "asked to answer with the tool")

	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	require.Len(t, choices, 3, "the next ones left to the model at once")
	require.Equal(t, anthropicToolChoice{Type: "auto"}, choices[2])

	// another bad request isn't asked again
	other := answering(t, http.StatusBadRequest, `{"type": "error", "error": {"type": "invalid_request_error",
		"message": "tools.0.input_schema: JSON schema is invalid"}}`)
	bad, err := NewAnthropic("claude-test", testKey, WithBaseURL(other.URL))
	require.NoError(t, err)
	_, err = bad.Analyze(context.Background(), testInput(nil, flatActions...))
	require.ErrorContains(t, err, "input_schema")
	require.Zero(t, bad.autoSince.Load())
}

func TestAnthropic_Answers(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   string
	}{
		{"not JSON", `<html>Bad gateway</html>`, "invalid character"},
		{"text instead of the tool", `{"content": [{"type": "text", "text": "I would buy gold now."}],
			"stop_reason": "end_turn"}`, `no call of the trading_decision tool (stop reason "end_turn")`},
		{"another tool", `{"content": [{"type": "tool_use", "name": "place_order", "input": {}}],
			"stop_reason": "tool_use"}`, "no call of the trading_decision tool"},
		{"cut at the max tokens", `{"content": [{"type": "tool_use", "name": "trading_decision",
			"input": {"reason": "The trend"}}], "stop_reason": "max_tokens"}`, "cut at the max tokens (4096)"},
		{"refused", `{"content": [], "stop_reason": "refusal"}`, "refused"},
		// a whole call of the tool, the answer cut all the same
		{"cut at the context window", anthropicStop(longDecision, "model_context_window_exceeded"),
			"cut at the context window of the model"},
		{"paused", anthropicStop(longDecision, "pause_turn"), "paused before the end of the answer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := analyze(t, testProviders[1], answering(t, http.StatusOK, tc.answer))
			require.ErrorIs(t, err, ErrInvalidResponse)
			require.ErrorContains(t, err, "anthropic: ")
			require.ErrorContains(t, err, tc.want)
			require.Zero(t, d)
		})
	}
}

func TestAnthropic_HTTPErrors(t *testing.T) {
	testHTTPErrors(t, testProviders[1], []httpErrorCase{
		{
			name:   "rate limit",
			status: http.StatusTooManyRequests,
			body: `{"type": "error", "error": {"type": "rate_limit_error",
				"message": "Number of request tokens has exceeded your per-minute rate limit."}}`,
			headers:    []string{"Retry-After", "15"},
			want:       ErrRateLimited,
			errType:    "rate_limit_error",
			message:    "per-minute rate limit",
			retryAfter: 15 * time.Second,
		},
		{
			name:    "overloaded",
			status:  529,
			body:    `{"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}}`,
			want:    ErrUnavailable,
			errType: "overloaded_error",
			message: "Overloaded",
		},
		{
			name:    "server error",
			status:  http.StatusInternalServerError,
			body:    `{"type": "error", "error": {"type": "api_error", "message": "Internal server error"}}`,
			want:    ErrUnavailable,
			errType: "api_error",
			message: "Internal server error",
		},
		{
			name:    "invalid API key",
			status:  http.StatusUnauthorized,
			body:    `{"type": "error", "error": {"type": "authentication_error", "message": "invalid x-api-key"}}`,
			errType: "authentication_error",
			message: "invalid x-api-key",
		},
		{
			name:   "bad request",
			status: http.StatusBadRequest,
			body: `{"type": "error", "error": {"type": "invalid_request_error",
				"message": "tools.0.input_schema: JSON schema is invalid"}}`,
			errType: "invalid_request_error",
			message: "input_schema",
		},
		{
			name:    "no permission",
			status:  http.StatusForbidden,
			body:    `{"type": "error", "error": {"type": "permission_error", "message": "not allowed"}}`,
			errType: "permission_error",
			message: "not allowed",
		},
		{
			name:   "gateway, without JSON",
			status: http.StatusBadGateway,
			body:   `<html>502 Bad Gateway</html>`,
			want:   ErrUnavailable,
		},
	})
}

// anthropicStop is the answer of anthropicBody with the stop reason stop.
func anthropicStop(decision, stop string) string {
	return strings.Replace(anthropicBody(decision), `"stop_reason": "tool_use"`, `"stop_reason": "`+stop+`"`, 1)
}

// forcedRefusal is the answer of the Messages API to the forced call of the
// tool by a model that refuses it.
const forcedRefusal = `{"type": "error", "error": {"type": "invalid_request_error",
	"message": "tool_choice: type \"tool\" and \"any\" are not supported for this model."}}`

// anthropicServer answers the requests of the decision, recording their
// tool_choice and max_tokens: refusing the forced call while refuse, the ones
// left to the model with answer.
type anthropicServer struct {
	refuse    atomic.Bool
	answer    atomic.Value // string
	choices   []anthropicToolChoice
	maxTokens []int
	mu        sync.Mutex
}

func newAnthropicServer(t *testing.T, refuse bool, answer string) (*anthropicServer, *httptest.Server) {
	s := &anthropicServer{}
	s.refuse.Store(refuse)
	s.answer.Store(answer)
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		var request anthropicRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		s.mu.Lock()
		s.choices = append(s.choices, request.ToolChoice)
		s.maxTokens = append(s.maxTokens, request.MaxTokens)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if request.ToolChoice.Type != "auto" {
			if s.refuse.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(forcedRefusal))
				return
			}
			_, _ = w.Write([]byte(anthropicBody(longDecision)))
			return
		}
		_, _ = w.Write([]byte(s.answer.Load().(string)))
	})
	return s, server
}

func (s *anthropicServer) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var types []string
	for _, choice := range s.choices {
		types = append(types, choice.Type)
	}
	return types
}

// A bad request about tool_choice that isn't the refusal of its forced call
// fails the request, the next ones still forced: the tool isn't left to the
// model for good.
func TestAnthropic_ForcedToolOtherErrorNotLeftToTheModel(t *testing.T) {
	for _, message := range []string{
		`tool_choice.name: Field required`,
		`tool_choice: tool 'trading_decision' not found in the tools`,
		`Thinking may not be enabled when tool_choice forces tool use.`,
	} {
		t.Run(message, func(t *testing.T) {
			var choices []string
			server := serve(t, func(w http.ResponseWriter, r *http.Request) {
				var request anthropicRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				choices = append(choices, request.ToolChoice.Type)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				body, _ := json.Marshal(map[string]any{"type": "error",
					"error": map[string]string{"type": "invalid_request_error", "message": message}})
				_, _ = w.Write(body)
			})
			provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL))
			require.NoError(t, err)

			_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
			require.ErrorContains(t, err, "HTTP 400")
			_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
			require.Error(t, err)
			require.Equal(t, []string{"tool", "tool"}, choices, "not asked again with the tool left to the model")
			require.Zero(t, provider.autoSince.Load())
		})
	}
	require.True(t, refusesForcedTool(&APIError{Status: http.StatusBadRequest,
		Message: `tool_choice: type "tool" and "any" are not supported for this model.`}))
	require.False(t, refusesForcedTool(&APIError{Status: http.StatusInternalServerError,
		Message: `tool_choice: type "tool" and "any" are not supported for this model.`}))
}

// The tool left to a model that refused its forced call is forced again an
// hour later: left to it again while it still refuses, forced from then on
// once it takes it.
func TestAnthropic_ForcedToolTriedAgain(t *testing.T) {
	s, server := newAnthropicServer(t, true, anthropicBody(longDecision))
	provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return now }
	analyze := func() {
		t.Helper()
		d, err := provider.Analyze(context.Background(), testInput(nil, flatActions...))
		require.NoError(t, err)
		require.Equal(t, ActionEnterLong, d.Action)
	}

	analyze()
	require.Equal(t, []string{"tool", "auto"}, s.types())
	now = now.Add(anthropicForcedRetry - time.Minute)
	analyze()
	require.Equal(t, []string{"tool", "auto", "auto"}, s.types(), "left to the model within the hour")

	now = now.Add(time.Minute)
	analyze()
	require.Equal(t, []string{"tool", "auto", "auto", "tool", "auto"}, s.types(), "forced again after the hour")
	now = now.Add(time.Minute)
	analyze()
	require.Equal(t, "auto", s.types()[5], "the hour counted again from the second refusal")

	s.refuse.Store(false)
	now = now.Add(anthropicForcedRetry)
	analyze()
	analyze()
	require.Equal(t, []string{"tool", "tool"}, s.types()[6:], "forced from then on")
	require.Zero(t, provider.autoSince.Load())
}

// Forced again after the hour, a single request tries it, the others still
// left to the model.
func TestAnthropic_ForcedToolTriedAgainOnce(t *testing.T) {
	provider, err := NewAnthropic("claude-test", testKey)
	require.NoError(t, err)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return now }
	provider.autoSince.Store(now.UnixNano())

	forced, _ := provider.forced()
	require.False(t, forced)
	now = now.Add(anthropicForcedRetry)
	var tries atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if forced, retried := provider.forced(); forced && retried {
				tries.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), tries.Load())
}

// Left the tool, a model that answers its decision in the text: the JSON read
// from it, through ParseDecision and Validate as the call of the tool.
func TestAnthropic_DecisionInTheText(t *testing.T) {
	text := func(texts ...string) string {
		content := []map[string]string{{"type": "thinking", "thinking": "A breakout {of the range}."}}
		for _, text := range texts {
			content = append(content, map[string]string{"type": "text", "text": text})
		}
		body, err := json.Marshal(map[string]any{"content": content, "stop_reason": "end_turn",
			"usage": map[string]int{"input_tokens": 1000, "output_tokens": 300}})
		require.NoError(t, err)
		return string(body)
	}
	invalid := `{"reason": "Below the entry.", "action": "ENTER_LONG", "confidence": 0.82, "entry": 100.2,
		"stop_loss": 101, "take_profit": 104}`
	tests := []struct {
		name    string
		answer  string
		action  Action
		err     string
		note    string
		refused bool
	}{
		{name: "in a block of code", answer: text("The trend {up} holds.\n\n```json\n" + longDecision + "\n```\nDone."),
			action: ActionEnterLong, note: "The trend {up} holds.\n\nDone."},
		{name: "alone", answer: text(longDecision), action: ActionEnterLong},
		{name: "refused by the rules", answer: text("```json\n" + invalid + "\n```"), action: ActionEnterLong,
			err: "stop loss", refused: true},
		{name: "not a decision", answer: text(`{"action": "BUY", "confidence": 1}`), err: "missing field"},
		{name: "partial", answer: text(`{"reason": "x", "action": "ENTER_LONG", "confidence"`),
			err: "no call of the trading_decision tool"},
		{name: "two decisions", answer: text(longDecision + "\nor\n" + longDecision),
			err: "2 decisions in the text"},
		{name: "no JSON", answer: text("I would buy gold now."), err: "no call of the trading_decision tool"},
		{name: "two decisions in two blocks", answer: text(`{"action": "WAIT"}`, longDecision),
			err: "2 decisions in the text"},
		{name: "cut", answer: strings.Replace(text(longDecision), "end_turn", "max_tokens", 1),
			err: "cut at the max tokens (16000)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, server := newAnthropicServer(t, true, tc.answer)
			provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL))
			require.NoError(t, err)

			d, err := provider.Analyze(context.Background(), testInput(nil, flatActions...))
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
			if tc.refused {
				require.NotErrorIs(t, err, ErrInvalidResponse, "a decision, refused by the rules")
			}
			require.Equal(t, tc.action, d.Action)
			if tc.action == "" {
				return
			}
			reason := "Breakout above the range with volume."
			if tc.refused {
				reason = "Below the entry."
			}
			require.Equal(t, reason, d.Reason)
			notes := []Note{{Kind: "reasoning", Text: "A breakout {of the range}."}}
			if tc.note != "" {
				notes = append([]Note{{Kind: "text", Text: tc.note}}, notes...)
			}
			require.Equal(t, notes, d.Notes)
		})
	}

	// forced, the text isn't read: the model was to call the tool
	server := answering(t, http.StatusOK, text(longDecision))
	provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.ErrorContains(t, err, "no call of the trading_decision tool")
}

// Left to the model, which thinks before it answers, the answer is capped at
// anthropicAutoMaxTokens, unless -llm-max-tokens sets it.
func TestAnthropic_MaxTokensOfTheThinking(t *testing.T) {
	s, server := newAnthropicServer(t, true, anthropicBody(longDecision))
	provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	require.Equal(t, []int{DefaultMaxTokens, anthropicAutoMaxTokens}, s.maxTokens)

	s, server = newAnthropicServer(t, true, anthropicBody(longDecision))
	provider, err = NewAnthropic("claude-test", testKey, WithBaseURL(server.URL), WithMaxTokens(2000))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	require.Equal(t, []int{2000, 2000}, s.maxTokens)
}
