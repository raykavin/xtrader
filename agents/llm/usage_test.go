package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAI_ReasoningEffort(t *testing.T) {
	server, got := recording(t, openAIBody(longDecision))
	provider, err := NewOpenAI("gpt-test", testKey, WithBaseURL(server.URL), WithReasoningEffort(" Low "))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	require.Equal(t, "low", got.body["reasoning_effort"])

	// the default of the model: left out
	server, got = recording(t, openAIBody(longDecision))
	provider, err = NewOpenAI("gpt-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	require.NotContains(t, got.body, "reasoning_effort")

	_, err = NewOpenAI("gpt-test", testKey, WithReasoningEffort("extreme"))
	require.ErrorContains(t, err, `invalid reasoning effort "extreme": minimal, low, medium, high`)
}

func TestAnthropic_ReasoningEffortIgnored(t *testing.T) {
	server, got := recording(t, anthropicBody(longDecision))
	provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(server.URL), WithReasoningEffort("low"))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	require.NotContains(t, got.body, "reasoning_effort")
	require.NotContains(t, got.body, "thinking", "a forced tool call takes no extended thinking")
}

func TestUsage_OpenAI(t *testing.T) {
	answer := `{"choices": [{"message": {"content": ` + quoted(longDecision) + `}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 4800, "completion_tokens": 700,
		"prompt_tokens_details": {"cached_tokens": 1152}, "completion_tokens_details": {"reasoning_tokens": 600}}}`
	provider, err := NewOpenAI("gpt-test", testKey, WithBaseURL(answering(t, http.StatusOK, answer).URL))
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
		require.NoError(t, err)
	}
	usage, requests := provider.TodayUsage()
	require.Equal(t, 2, requests)
	require.Equal(t, Usage{Input: 9600, Cached: 2304, Output: 1400, Reasoning: 1200}, usage)
}

func TestUsage_CountedForInvalidAnswers(t *testing.T) {
	answer := `{"choices": [{"message": {"content": "{\"reason\": \"The"}, "finish_reason": "length"}],
		"usage": {"prompt_tokens": 4800, "completion_tokens": 4096}}`
	provider, err := NewOpenAI("gpt-test", testKey, WithBaseURL(answering(t, http.StatusOK, answer).URL))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.Error(t, err, "no decision")
	usage, requests := provider.TodayUsage()
	require.Equal(t, 1, requests, "billed anyway")
	require.Equal(t, 4096, usage.Output)
}

func TestUsage_Anthropic(t *testing.T) {
	answer := `{"content": [{"type": "tool_use", "name": "trading_decision", "input": ` + longDecision + `}],
		"stop_reason": "tool_use", "usage": {"input_tokens": 3000, "output_tokens": 90,
		"cache_read_input_tokens": 1200, "cache_creation_input_tokens": 0}}`
	provider, err := NewAnthropic("claude-test", testKey, WithBaseURL(answering(t, http.StatusOK, answer).URL))
	require.NoError(t, err)
	_, err = provider.Analyze(context.Background(), testInput(nil, flatActions...))
	require.NoError(t, err)
	usage, requests := provider.TodayUsage()
	require.Equal(t, 1, requests)
	require.Equal(t, Usage{Input: 4200, Cached: 1200, Output: 90}, usage, "the cached tokens are prompt tokens too")
}

func TestUsageMeter_Day(t *testing.T) {
	meter := newUsageMeter()
	now := time.Date(2026, 9, 26, 23, 59, 0, 0, time.UTC)
	meter.now = func() time.Time { return now }

	today, requests := meter.add(Usage{Input: 100, Output: 10})
	require.Equal(t, 1, requests)
	require.Equal(t, Usage{Input: 100, Output: 10}, today)
	meter.add(Usage{Input: 50, Output: 5})
	usage, requests := meter.Today()
	require.Equal(t, Usage{Input: 150, Output: 15}, usage)
	require.Equal(t, 2, requests)

	now = now.Add(2 * time.Minute) // the next day, in UTC
	usage, requests = meter.Today()
	require.Zero(t, requests)
	require.Equal(t, Usage{}, usage)
	today, requests = meter.add(Usage{Input: 7})
	require.Equal(t, 1, requests, "counted from zero")
	require.Equal(t, Usage{Input: 7}, today)
}

// quoted returns s as a JSON string, eg: the content of an answer of OpenAI.
func quoted(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
