package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// supervisorTools are the tools of the tests of the chat.
var supervisorTools = []ToolSpec{
	{Name: "get_overview", Description: "the agents"},
	{Name: "block_entries", Description: "block the entries of an agent",
		Parameters: json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string"}},"required":["agent"]}`)},
}

// a conversation with a call of a tool answered.
var conversation = []ChatMessage{
	{Role: ChatUser, Content: "how is the gold?"},
	{Role: ChatAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "get_overview"}}},
	{Role: ChatTool, ToolCallID: "call_1", Content: `{"agents":[]}`},
}

// OpenAI: the instructions, the conversation and the tools as functions; the
// answer's text and calls, their arguments as the model wrote them.
func TestChat_OpenAI(t *testing.T) {
	answer := `{"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant", "content": "Blocking.",
		"tool_calls": [{"id": "call_2", "type": "function", "function": {"name": "block_entries",
		"arguments": "{\"agent\":\"orb\"}"}}]}}], "usage": {"prompt_tokens": 100, "completion_tokens": 20}}`
	server, got := recording(t, answer)
	chat, err := NewChat("openai", "gpt-test", testKey, WithBaseURL(server.URL), WithMaxTokens(900),
		WithReasoningEffort("low"))
	require.NoError(t, err)
	require.Equal(t, "openai gpt-test", chat.String())

	result, err := chat.Complete(context.Background(), "You supervise.", conversation, supervisorTools)
	require.NoError(t, err)
	require.Equal(t, "Blocking.", result.Content)
	require.Equal(t, []ToolCall{{ID: "call_2", Name: "block_entries", Arguments: json.RawMessage(`{"agent":"orb"}`)}},
		result.ToolCalls)
	require.Equal(t, Usage{Input: 100, Output: 20}, result.Usage)
	require.False(t, result.Truncated)

	require.Equal(t, "/chat/completions", got.path)
	require.Equal(t, "Bearer "+testKey, got.header.Get("Authorization"))
	require.Equal(t, 900.0, got.body["max_completion_tokens"])
	require.Equal(t, "low", got.body["reasoning_effort"])
	require.Equal(t, "auto", got.body["tool_choice"])
	var request openAIChatRequest
	require.NoError(t, json.Unmarshal(got.raw, &request))
	require.Len(t, request.Messages, 4)
	require.Equal(t, "system", request.Messages[0].Role)
	require.Equal(t, "You supervise.", *request.Messages[0].Content)
	require.Nil(t, request.Messages[2].Content, "an assistant message of calls only")
	require.Equal(t, "call_1", request.Messages[2].ToolCalls[0].ID)
	require.Equal(t, "{}", request.Messages[2].ToolCalls[0].Function.Arguments)
	require.Equal(t, "call_1", request.Messages[3].ToolCallID)
	require.Len(t, request.Tools, 2)
	require.JSONEq(t, string(emptyObject), string(request.Tools[0].Function.Parameters))
}

// Anthropic: the instructions apart, the calls as tool_use and their results as
// tool_result, consecutive ones in one message of the user.
func TestChat_Anthropic(t *testing.T) {
	answer := `{"content": [{"type": "text", "text": "Looking."},
		{"type": "tool_use", "id": "toolu_2", "name": "block_entries", "input": {"agent": "orb"}}],
		"stop_reason": "tool_use", "usage": {"input_tokens": 80, "output_tokens": 12}}`
	server, got := recording(t, answer)
	chat, err := NewChat("anthropic", "claude-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)

	messages := []ChatMessage{
		conversation[0],
		{Role: ChatAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "get_overview"}, {ID: "b", Name: "get_overview"}}},
		{Role: ChatTool, ToolCallID: "a", Content: "1"},
		{Role: ChatTool, ToolCallID: "b", Content: "2"},
	}
	result, err := chat.Complete(context.Background(), "You supervise.", messages, supervisorTools)
	require.NoError(t, err)
	require.Equal(t, "Looking.", result.Content)
	require.Equal(t, "block_entries", result.ToolCalls[0].Name)
	require.JSONEq(t, `{"agent":"orb"}`, string(result.ToolCalls[0].Arguments))

	require.Equal(t, "/messages", got.path)
	require.Equal(t, testKey, got.header.Get("X-Api-Key"))
	require.Equal(t, "You supervise.", got.body["system"])
	var request anthropicChatRequest
	require.NoError(t, json.Unmarshal(got.raw, &request))
	require.Len(t, request.Messages, 3, "the two results in one message")
	require.Equal(t, []string{"tool_result", "tool_result"},
		[]string{request.Messages[2].Content[0].Type, request.Messages[2].Content[1].Type})
	require.Equal(t, "tool_use", request.Messages[1].Content[0].Type)
	require.Equal(t, "auto", request.ToolChoice.Type)
	require.JSONEq(t, string(emptyObject), string(request.Tools[0].InputSchema))
}

// The failures of the API are the ones of the providers: unavailable, rate
// limited, timeouts, without the API key in their message.
func TestChat_Failures(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		server := answering(t, http.StatusServiceUnavailable,
			`{"error": {"type": "overloaded_error", "message": "overloaded `+testKey+`"}}`)
		chat, err := NewChat(provider, "model", testKey, WithBaseURL(server.URL))
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), retryRoom) // no room to send it again
		_, err = chat.Complete(ctx, "system", conversation[:1], nil)
		cancel()
		require.ErrorIs(t, err, ErrUnavailable, provider)
		require.False(t, strings.Contains(err.Error(), testKey), provider)

		server = answering(t, http.StatusOK, `{"not": "an answer"`)
		chat, err = NewChat(provider, "model", testKey, WithBaseURL(server.URL))
		require.NoError(t, err)
		_, err = chat.Complete(context.Background(), "system", conversation[:1], nil)
		require.Error(t, err, provider)
		require.True(t, errors.Is(err, ErrInvalidResponse), provider)
	}
	_, err := NewChat("google", "model", testKey)
	require.Error(t, err)
	_, err = NewChat("openai", "model", "")
	require.Error(t, err, "the key is required")
}
