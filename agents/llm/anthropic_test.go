package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	require.False(t, bad.auto.Load())
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
