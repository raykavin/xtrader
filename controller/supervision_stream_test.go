package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agents/llm"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// streamingChat is the model of the tests answering as a stream: the text of
// each answer of its script in pieces, a word each.
type streamingChat struct {
	*fakeChat
}

func (c streamingChat) Stream(ctx context.Context, system string, messages []llm.ChatMessage,
	tools []llm.ToolSpec, text func(string)) (llm.ChatAnswer, error) {
	answer, err := c.Complete(ctx, system, messages, tools)
	if err != nil {
		return answer, err
	}
	for _, word := range strings.SplitAfter(answer.Content, " ") {
		if word != "" {
			text(word)
		}
	}
	return answer, nil
}

// sseEvent is an event of a stream of the tests.
type sseEvent struct {
	name, data string
}

// sseEvents returns the events of body, the comments left out.
func sseEvents(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, block := range strings.Split(body, "\n\n") {
		var event sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				event.data = strings.TrimPrefix(line, "data: ")
			}
		}
		if event.name != "" {
			events = append(events, event)
		}
	}
	return events
}

// The answer streamed: the question kept first, the stages, a proposal as the
// model makes it, the text of the model in pieces, the text before a tool
// dropped by the next stage, then "done" with every message added, as the
// JSON answer has them; a message refused before the stream is JSON.
func TestSupervisionAPI_ChatStream(t *testing.T) {
	s := newSupervised(t)
	s.liveAgent(t, "fibonacci", 101, true)
	s.bridge.set(protected(1, 101))
	s.sup.newChat = func(store.SupervisorSettings, string) (llm.Chat, error) {
		return streamingChat{s.chat}, nil
	}
	s.chat.script(
		func(string, []llm.ChatMessage, []llm.ToolSpec) (llm.ChatAnswer, error) {
			return llm.ChatAnswer{Content: "Vou propor.", ToolCalls: []llm.ToolCall{tool("p1", "propose_action",
				map[string]any{"action": store.ActionBlockEntries, "agents": []string{"fibonacci"},
					"reason": "bloquear o fibonacci"})}}, nil
		},
		saying("Preparei a **proposta**: confira."))
	call, _ := settingsSession(t, s.managed)

	resp, body := call(http.MethodPost, "/api/supervisor/messages/stream",
		ui.SupervisorMessageRequest{Text: "bloqueie o fibonacci"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	events := sseEvents(t, body)

	var names []string
	var texts []string
	var stages []ui.SupervisorStreamStage
	var kept []ui.SupervisorMessageInfo
	var done ui.SupervisorMessagesResponse
	for _, event := range events {
		names = append(names, event.name)
		switch event.name {
		case ui.SupervisorEventStage:
			var stage ui.SupervisorStreamStage
			require.NoError(t, json.Unmarshal([]byte(event.data), &stage))
			stages = append(stages, stage)
		case ui.SupervisorEventText:
			var text ui.SupervisorStreamText
			require.NoError(t, json.Unmarshal([]byte(event.data), &text))
			texts = append(texts, text.Text)
		case ui.SupervisorEventMessage:
			var m ui.SupervisorMessageInfo
			require.NoError(t, json.Unmarshal([]byte(event.data), &m))
			kept = append(kept, m)
		case ui.SupervisorEventDone:
			require.NoError(t, json.Unmarshal([]byte(event.data), &done))
		}
	}
	require.NotEmpty(t, names)
	assert.Equal(t, ui.SupervisorEventMessage, names[0], "the question first")
	assert.Equal(t, ui.SupervisorEventDone, names[len(names)-1], "done last")
	assert.Equal(t, []ui.SupervisorStreamStage{{Stage: stageReading}, {Stage: stageThinking},
		{Stage: stageTool, Tool: "propose_action"}, {Stage: stageThinking}}, stages)
	assert.Equal(t, []string{"Vou ", "propor.", "Preparei ", "a ", "**proposta**: ", "confira."}, texts)
	require.Len(t, kept, 2)
	assert.Equal(t, store.MessageQuestion, kept[0].Kind)
	assert.Equal(t, store.MessageProposal, kept[1].Kind)
	require.NotNil(t, kept[1].Action)
	assert.Equal(t, store.ActionProposed, kept[1].Action.Status)
	require.Len(t, done.Messages, 3)
	assert.Equal(t, []string{store.MessageQuestion, store.MessageProposal, store.MessageAnswer},
		[]string{done.Messages[0].Kind, done.Messages[1].Kind, done.Messages[2].Kind})
	assert.Equal(t, "Preparei a **proposta**: confira.", done.Messages[2].Content, "the answer, not the text before")
	assert.Equal(t, kept[0].ID, done.Messages[0].ID)

	// the conversation kept as without the stream
	resp, body = call(http.MethodGet, "/api/supervisor/messages", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var conversation ui.SupervisorMessagesResponse
	require.NoError(t, json.Unmarshal([]byte(body), &conversation))
	assert.Len(t, conversation.Messages, 3)

	// the model failing: an error message kept, then done
	s.chat.script(failing(llm.ErrUnavailable))
	resp, body = call(http.MethodPost, "/api/supervisor/messages/stream",
		ui.SupervisorMessageRequest{Text: "e agora?"})
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	events = sseEvents(t, body)
	require.NoError(t, json.Unmarshal([]byte(events[len(events)-1].data), &done))
	require.Len(t, done.Messages, 2)
	assert.Equal(t, store.MessageError, done.Messages[1].Kind)
	assert.Contains(t, done.Messages[1].Content, "provedor indisponível")

	// refused before the stream
	resp, body = call(http.MethodPost, "/api/supervisor/messages/stream", ui.SupervisorMessageRequest{Text: "  "})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
}
