package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// streaming serves the events of a stream, a line each, flushed one at a time,
// recording the request.
func streaming(t *testing.T, events ...string) (string, *readRequest) {
	t.Helper()
	got := &readRequest{}
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got.header, got.method, got.path = r.Header.Clone(), r.Method, r.URL.Path
		got.raw, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(got.raw, &got.body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			_, _ = fmt.Fprint(w, event+"\n\n")
			w.(http.Flusher).Flush()
		}
	})
	return server.URL, got
}

// The events of a stream: their names and data, the lines of a data joined,
// the comments skipped, the last one without a blank line after it too.
func TestReadEvents(t *testing.T) {
	body := ": connected\n\nevent: delta\ndata: {\"a\":1}\n\ndata: one\ndata: two\n\n: ping\n\nevent: end\ndata: x"
	var got []string
	require.NoError(t, readEvents(strings.NewReader(body), func(name, data string) error {
		got = append(got, name+"|"+data)
		return nil
	}))
	require.Equal(t, []string{`delta|{"a":1}`, "|one\ntwo", "end|x"}, got)

	got = nil
	require.NoError(t, readEvents(strings.NewReader("data: 1\n\ndata: 2\n\n"), func(_, data string) error {
		got = append(got, data)
		return errStreamDone
	}))
	require.Equal(t, []string{"1"}, got, "read until done")
}

// OpenAI: the text told as it comes, the calls of tools put together from
// their pieces by index, the tokens of the last event; the request asks for
// the stream and its usage.
func TestChatStream_OpenAI(t *testing.T) {
	url, got := streaming(t,
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"Vou "},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"bloquear."},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function",`+
			`"function":{"name":"block_entries","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"agent\""}}]},`+
			`"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"orb\"}"}}]},`+
			`"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20}}`,
		`data: [DONE]`)
	chat, err := NewChat("openai", "gpt-test", testKey, WithBaseURL(url), WithMaxTokens(900))
	require.NoError(t, err)
	streamer, ok := chat.(ChatStreamer)
	require.True(t, ok)

	var pieces []string
	result, err := streamer.Stream(context.Background(), "You supervise.", conversation, supervisorTools,
		func(piece string) { pieces = append(pieces, piece) })
	require.NoError(t, err)
	require.Equal(t, []string{"Vou ", "bloquear."}, pieces)
	require.Equal(t, "Vou bloquear.", result.Content)
	require.Equal(t, []ToolCall{{ID: "call_2", Name: "block_entries", Arguments: json.RawMessage(`{"agent":"orb"}`)}},
		result.ToolCalls)
	require.Equal(t, Usage{Input: 100, Output: 20}, result.Usage)
	require.False(t, result.Truncated)

	require.Equal(t, "/chat/completions", got.path)
	require.Equal(t, "text/event-stream", got.header.Get("Accept"))
	require.Equal(t, true, got.body["stream"])
	require.Equal(t, map[string]any{"include_usage": true}, got.body["stream_options"])
	require.Equal(t, 900.0, got.body["max_completion_tokens"])
}

// OpenAI: a stream cut before its end, an error in it, or a refusal fail; an
// answer cut at the max tokens is told truncated.
func TestChatStream_OpenAIFailures(t *testing.T) {
	stream := func(t *testing.T, events ...string) (ChatAnswer, error) {
		url, _ := streaming(t, events...)
		chat, err := NewChat("openai", "gpt-test", testKey, WithBaseURL(url))
		require.NoError(t, err)
		return chat.(ChatStreamer).Stream(context.Background(), "s", conversation, nil, func(string) {})
	}

	_, err := stream(t, `data: {"choices":[{"index":0,"delta":{"content":"Meio"},"finish_reason":null}]}`)
	require.ErrorIs(t, err, ErrUnavailable, "cut before its end")

	_, err = stream(t, `data: {"error":{"type":"server_error","message":"overloaded"}}`)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "server_error", apiErr.Type)

	_, err = stream(t, `data: {"choices":[{"index":0,"delta":{"refusal":"não posso"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)
	require.ErrorIs(t, err, ErrInvalidResponse, "refused")

	result, err := stream(t, `data: {"choices":[{"index":0,"delta":{"content":"Longo"},"finish_reason":"length"}]}`,
		`data: [DONE]`)
	require.NoError(t, err)
	require.True(t, result.Truncated)
	require.Equal(t, "Longo", result.Content)
}

// A request refused before the stream (503) is sent again once, as the
// requests of Complete; the text told once.
func TestChatStream_SentAgainBeforeTheStream(t *testing.T) {
	shortRetry(t, 10*time.Millisecond, time.Second, 0)
	var requests atomic.Int32
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":{"type":"overloaded","message":"busy"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Ok\"},\"finish_reason\":\"stop\"}]}"+
			"\n\ndata: [DONE]\n\n")
	})
	chat, err := NewChat("openai", "gpt-test", testKey, WithBaseURL(server.URL))
	require.NoError(t, err)
	var pieces []string
	result, err := chat.(ChatStreamer).Stream(context.Background(), "s", conversation, nil,
		func(piece string) { pieces = append(pieces, piece) })
	require.NoError(t, err)
	require.Equal(t, "Ok", result.Content)
	require.Equal(t, []string{"Ok"}, pieces)
	require.EqualValues(t, 2, requests.Load())
}

// Anthropic: the pieces of the blocks of text, a blank line between two
// blocks, the input of a call put together from its pieces of JSON, the tokens
// of the start and of the end.
func TestChatStream_Anthropic(t *testing.T) {
	url, got := streaming(t,
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"usage":{"input_tokens":80,`+
			`"cache_read_input_tokens":20,"output_tokens":1}}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,`+
			`"content_block":{"type":"text","text":""}}`,
		`event: ping`+"\n"+`data: {"type":"ping"}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,`+
			`"delta":{"type":"text_delta","text":"Olhando"}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,`+
			`"delta":{"type":"text_delta","text":" o orb."}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":1,`+
			`"content_block":{"type":"tool_use","id":"toolu_2","name":"block_entries","input":{}}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,`+
			`"delta":{"type":"input_json_delta","partial_json":"{\"agent\":"}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,`+
			`"delta":{"type":"input_json_delta","partial_json":"\"orb\"}"}}`,
		`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":1}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":2,`+
			`"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":2,`+
			`"delta":{"type":"text_delta","text":"Feito."}}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},`+
			`"usage":{"output_tokens":30}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	chat, err := NewChat("anthropic", "claude-test", testKey, WithBaseURL(url))
	require.NoError(t, err)

	var pieces []string
	result, err := chat.(ChatStreamer).Stream(context.Background(), "You supervise.", conversation,
		supervisorTools, func(piece string) { pieces = append(pieces, piece) })
	require.NoError(t, err)
	require.Equal(t, []string{"Olhando", " o orb.", "\n\n", "Feito."}, pieces)
	require.Equal(t, "Olhando o orb.\n\nFeito.", result.Content)
	require.Equal(t, []ToolCall{{ID: "toolu_2", Name: "block_entries", Arguments: json.RawMessage(`{"agent":"orb"}`)}},
		result.ToolCalls)
	require.Equal(t, Usage{Input: 100, Cached: 20, Output: 30}, result.Usage)

	require.Equal(t, "/messages", got.path)
	require.Equal(t, true, got.body["stream"])
	require.Equal(t, "text/event-stream", got.header.Get("Accept"))
}

// Anthropic: an error event, a stream cut before message_stop and a refusal
// fail.
func TestChatStream_AnthropicFailures(t *testing.T) {
	stream := func(t *testing.T, events ...string) error {
		url, _ := streaming(t, events...)
		chat, err := NewChat("anthropic", "claude-test", testKey, WithBaseURL(url))
		require.NoError(t, err)
		_, err = chat.(ChatStreamer).Stream(context.Background(), "s", conversation, nil, func(string) {})
		return err
	}
	start := `data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}`

	err := stream(t, start, `event: error`+"\n"+`data: {"type":"error","error":{"type":"overloaded_error",`+
		`"message":"Overloaded"}}`)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "overloaded_error", apiErr.Type)

	err = stream(t, start, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Meio"}}`)
	require.ErrorIs(t, err, ErrUnavailable, "cut before message_stop")

	err = stream(t, start, `data: {"type":"message_delta","delta":{"stop_reason":"refusal"}}`,
		`data: {"type":"message_stop"}`)
	require.ErrorIs(t, err, ErrInvalidResponse)
}
