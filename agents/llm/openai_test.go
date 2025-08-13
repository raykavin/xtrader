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

func TestOpenAI_Request(t *testing.T) {
	server, got := recording(t, openAIBody(longDecision))
	provider, err := NewOpenAI("gpt-test", testKey, WithBaseURL(server.URL), WithMaxTokens(1000))
	require.NoError(t, err)

	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)

	require.Equal(t, http.MethodPost, got.method)
	require.Equal(t, "/chat/completions", got.path)
	require.Equal(t, "Bearer "+testKey, got.header.Get("Authorization"))
	require.Equal(t, "application/json", got.header.Get("Content-Type"))

	require.Equal(t, "gpt-test", got.body["model"])
	require.Equal(t, 1000.0, got.body["max_completion_tokens"])
	require.NotContains(t, got.body, "max_tokens", "rejected by the models that reason")
	require.NotContains(t, got.body, "temperature", "rejected by the models that reason")

	// the answer is constrained to the strict schema of a decision
	var request struct {
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
		Messages []openAIMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(got.raw, &request))
	require.Equal(t, "json_schema", request.ResponseFormat.Type)
	require.Equal(t, "trading_decision", request.ResponseFormat.JSONSchema.Name)
	require.True(t, request.ResponseFormat.JSONSchema.Strict)
	require.JSONEq(t, string(decisionSchema), string(request.ResponseFormat.JSONSchema.Schema))

	// the instructions, then the context
	require.Len(t, request.Messages, 2)
	require.Equal(t, openAIMessage{Role: "system", Content: defaultSystemPrompt}, request.Messages[0])
	require.Equal(t, "user", request.Messages[1].Role)
	context, ok := strings.CutPrefix(request.Messages[1].Content, "Market context of the candle just closed:\n")
	require.True(t, ok)
	var in map[string]any
	require.NoError(t, json.Unmarshal([]byte(context), &in))
	require.Equal(t, "XAUUSD", in["symbol"])
	require.Equal(t, 100.0, in["price"])
}

// AnalyzeWith sends the instructions it is given, the provider keeping its own.
func TestOpenAI_AnalyzeWith(t *testing.T) {
	server, got := recording(t, openAIBody(longDecision))
	provider, err := NewOpenAI("gpt-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)
	var _ SystemPromptProvider = provider

	system := systemPrompt("# x\n\nStrategy X.")
	_, err = provider.AnalyzeWith(context.Background(), system, testInput(nil, flatActions...))
	require.NoError(t, err)
	var request struct {
		Messages []openAIMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(got.raw, &request))
	require.Equal(t, openAIMessage{Role: "system", Content: system}, request.Messages[0])
	require.Equal(t, defaultSystemPrompt, provider.system)
}

func TestOpenAI_Answers(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   string
	}{
		{"not JSON", `<html>Bad gateway</html>`, "invalid character"},
		{"no choice", `{"choices": []}`, "no choice"},
		{"refused", `{"choices": [{"message": {"content": null, "refusal": "I can't help with trading."},
			"finish_reason": "stop"}]}`, "refused: I can't help with trading."},
		{"cut at the max tokens", `{"choices": [{"message": {"content": "{\"reason\": \"The trend"},
			"finish_reason": "length"}]}`, "cut at the max tokens (4096)"},
		{"content filter", `{"choices": [{"message": {"content": ""}, "finish_reason": "content_filter"}]}`,
			"cut by the content filter"},
		{"no content", `{"choices": [{"message": {"content": null}, "finish_reason": "stop"}]}`, "empty content"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := analyze(t, testProviders[0], answering(t, http.StatusOK, tc.answer))
			require.ErrorIs(t, err, ErrInvalidResponse)
			require.ErrorContains(t, err, "openai: ")
			require.ErrorContains(t, err, tc.want)
			require.Zero(t, d)
		})
	}
}

func TestOpenAI_HTTPErrors(t *testing.T) {
	testHTTPErrors(t, testProviders[0], []httpErrorCase{
		{
			name:   "rate limit",
			status: http.StatusTooManyRequests,
			body: `{"error": {"message": "Rate limit reached for gpt-test on tokens per min (TPM): Limit 30000.",
				"type": "tokens", "param": null, "code": "rate_limit_exceeded"}}`,
			headers:    []string{"Retry-After", "20"},
			want:       ErrRateLimited,
			errType:    "rate_limit_exceeded",
			message:    "Rate limit reached",
			retryAfter: 20 * time.Second,
		},
		{
			name:   "quota spent",
			status: http.StatusTooManyRequests,
			body: `{"error": {"message": "You exceeded your current quota.", "type": "insufficient_quota",
				"param": null, "code": "insufficient_quota"}}`,
			want:    ErrRateLimited,
			errType: "insufficient_quota",
			message: "current quota",
		},
		{
			name:   "invalid API key",
			status: http.StatusUnauthorized,
			body: `{"error": {"message": "Incorrect API key provided: sk-test-****************cdef. You can find ` +
				`your API key at https://platform.openai.com/account/api-keys.", "type": "invalid_request_error",
				"param": null, "code": "invalid_api_key"}}`,
			errType: "invalid_api_key",
			message: "Incorrect API key provided: [redacted].",
		},
		{
			name:   "bad request",
			status: http.StatusBadRequest,
			body: `{"error": {"message": "Invalid schema for response_format 'trading_decision'.",
				"type": "invalid_request_error", "param": "response_format", "code": null}}`,
			errType: "invalid_request_error",
			message: "Invalid schema",
		},
		{
			name:    "unknown model",
			status:  http.StatusNotFound,
			body:    `{"error": {"message": "The model gpt-test does not exist.", "type": "invalid_request_error"}}`,
			errType: "invalid_request_error",
			message: "does not exist",
		},
		{
			name:    "server error",
			status:  http.StatusInternalServerError,
			body:    `{"error": {"message": "The server had an error.", "type": "server_error", "code": null}}`,
			want:    ErrUnavailable,
			errType: "server_error",
			message: "server had an error",
		},
		{
			name:   "unavailable, without JSON",
			status: http.StatusServiceUnavailable,
			body:   `upstream connect error`,
			want:   ErrUnavailable,
		},
	})
}
