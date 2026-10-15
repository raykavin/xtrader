package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// ChatStreamer is a Chat that answers as the model writes: Stream returns the
// answer Complete returns, calling text with the pieces of its text as they
// come, from the goroutine of the call. A stream cut halfway is an error,
// never sent again: its text was told already.
type ChatStreamer interface {
	Stream(ctx context.Context, system string, messages []ChatMessage, tools []ToolSpec,
		text func(piece string)) (ChatAnswer, error)
}

// maxEventLine bounds a line of the events of a stream: a piece of an answer
// is a few tokens.
const maxEventLine = 1 << 20

// readEvents reads the server-sent events of body, calling event with the
// name and the data of each one, its lines joined, until body ends or event
// returns errStreamDone or another error, returned.
func readEvents(body io.Reader, event func(name, data string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxEventLine)
	var name string
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				if err := event(name, strings.Join(data, "\n")); err != nil {
					if errors.Is(err, errStreamDone) {
						return nil
					}
					return err
				}
			}
			name, data = "", nil
		case strings.HasPrefix(line, ":"): // a comment, eg: a keep-alive
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(data) > 0 {
		if err := event(name, strings.Join(data, "\n")); err != nil && !errors.Is(err, errStreamDone) {
			return err
		}
	}
	return nil
}

// errStreamDone ends the reading of a stream, its answer whole.
var errStreamDone = errors.New("the stream ended")

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   *string `json:"content"`
			Refusal   *string `json:"refusal"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// streamedCall is a call of a tool as its pieces come.
type streamedCall struct {
	id, name  string
	arguments strings.Builder
}

// Stream answers the conversation as the model writes it, see ChatStreamer:
// the pieces of the content of the first choice, its calls of tools put
// together by their index, the tokens in the last event.
func (o *openAIChat) Stream(ctx context.Context, system string, messages []ChatMessage, tools []ToolSpec,
	text func(string)) (ChatAnswer, error) {
	request := o.request(system, messages, tools)
	request.Stream, request.StreamOptions = true, &openAIStreamOpts{IncludeUsage: true}
	headers := http.Header{"Authorization": {"Bearer " + o.apiKey}}
	body, err := o.postStream(ctx, "/chat/completions", headers, request)
	if err != nil {
		return ChatAnswer{}, err
	}
	defer func() { _ = body.Close() }()

	var content, refusal strings.Builder
	calls := map[int]*streamedCall{}
	var usage *openAIUsage
	finish := ""
	err = readEvents(body, func(_, data string) error {
		if data == "[DONE]" {
			return errStreamDone
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return invalidAnswer(o.name, "an event of the stream: %v", err)
		}
		if chunk.Error != nil {
			kind := chunk.Error.Type
			if chunk.Error.Code != "" {
				kind = chunk.Error.Code
			}
			return &APIError{Provider: o.name, Status: http.StatusOK, Type: kind,
				Message: o.redact(chunk.Error.Message)}
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != nil {
			finish = *choice.FinishReason
		}
		if piece := choice.Delta.Content; piece != nil && *piece != "" {
			content.WriteString(*piece)
			text(*piece)
		}
		if piece := choice.Delta.Refusal; piece != nil {
			refusal.WriteString(*piece)
		}
		for _, delta := range choice.Delta.ToolCalls {
			call := calls[delta.Index]
			if call == nil {
				call = &streamedCall{}
				calls[delta.Index] = call
			}
			if delta.ID != "" {
				call.id = delta.ID
			}
			if delta.Function.Name != "" {
				call.name = delta.Function.Name
			}
			call.arguments.WriteString(delta.Function.Arguments)
		}
		return nil
	})
	var answerUsage Usage
	if usage != nil {
		answerUsage = usage.usage()
		o.recordChat(answerUsage)
	}
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) || errors.Is(err, ErrInvalidResponse) {
			return ChatAnswer{}, err
		}
		return ChatAnswer{}, o.failure(ctx, err) // cut while read: a timeout, the provider gone
	}
	if refusal.Len() > 0 {
		return ChatAnswer{}, invalidAnswer(o.name, "refused: %s", o.redact(refusal.String()))
	}
	if finish == "" {
		return ChatAnswer{}, o.failure(ctx, fmt.Errorf("the stream ended before the answer: %w", io.ErrUnexpectedEOF))
	}
	result := ChatAnswer{Content: strings.TrimSpace(content.String()), Usage: answerUsage,
		Truncated: finish == "length"}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		call := calls[index]
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: call.id, Name: call.name,
			Arguments: arguments(json.RawMessage(call.arguments.String()))})
	}
	return result, nil
}

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// streamedBlock is a block of the content of an answer as its pieces come: a
// text or a call of a tool.
type streamedBlock struct {
	kind     string
	text     strings.Builder
	id, name string
}

// Stream answers the conversation as the model writes it, see ChatStreamer:
// the pieces of its blocks of text, apart by a blank line as Complete joins
// them, its calls of tools put together from their pieces of JSON.
func (a *anthropicChat) Stream(ctx context.Context, system string, messages []ChatMessage, tools []ToolSpec,
	text func(string)) (ChatAnswer, error) {
	request := a.request(system, messages, tools)
	request.Stream = true
	headers := http.Header{"X-Api-Key": {a.apiKey}, "Anthropic-Version": {anthropicVersion}}
	body, err := a.postStream(ctx, "/messages", headers, request)
	if err != nil {
		return ChatAnswer{}, err
	}
	defer func() { _ = body.Close() }()

	blocks := map[int]*streamedBlock{}
	var order []int
	var usage anthropicUsage
	stop := ""
	written := false // text told, the next block of text after a blank line
	ended := false
	err = readEvents(body, func(_, data string) error {
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return invalidAnswer(a.name, "an event of the stream: %v", err)
		}
		switch event.Type {
		case "message_start":
			usage = event.Message.Usage
		case "content_block_start":
			block := &streamedBlock{kind: event.ContentBlock.Type, id: event.ContentBlock.ID,
				name: event.ContentBlock.Name}
			blocks[event.Index] = block
			order = append(order, event.Index)
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				return invalidAnswer(a.name, "a piece of the block %d, never started", event.Index)
			}
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text == "" {
					return nil
				}
				if block.text.Len() == 0 && written {
					text("\n\n")
				}
				block.text.WriteString(event.Delta.Text)
				text(event.Delta.Text)
				written = true
			case "input_json_delta":
				block.text.WriteString(event.Delta.PartialJSON)
			}
		case "message_delta":
			if event.Delta.StopReason != "" {
				stop = event.Delta.StopReason
			}
			if event.Usage != nil {
				usage.OutputTokens = event.Usage.OutputTokens
			}
		case "message_stop":
			ended = true
			return errStreamDone
		case "error":
			if event.Error == nil {
				return invalidAnswer(a.name, "an error without detail")
			}
			return &APIError{Provider: a.name, Status: http.StatusOK, Type: event.Error.Type,
				Message: a.redact(event.Error.Message)}
		}
		return nil
	})
	answerUsage := usage.usage()
	if answerUsage != (Usage{}) {
		a.recordChat(answerUsage)
	}
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) || errors.Is(err, ErrInvalidResponse) {
			return ChatAnswer{}, err
		}
		return ChatAnswer{}, a.failure(ctx, err) // cut while read: a timeout, the provider gone
	}
	if !ended {
		return ChatAnswer{}, a.failure(ctx, fmt.Errorf("the stream ended before the answer: %w", io.ErrUnexpectedEOF))
	}
	if err := a.stopped(stop); err != nil {
		return ChatAnswer{}, err
	}
	result := ChatAnswer{Usage: answerUsage, Truncated: stop == "max_tokens"}
	var texts []string
	for _, index := range order {
		block := blocks[index]
		switch block.kind {
		case "text":
			if text := strings.TrimSpace(block.text.String()); text != "" {
				texts = append(texts, text)
			}
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: block.id, Name: block.name,
				Arguments: arguments(json.RawMessage(block.text.String()))})
		}
	}
	result.Content = strings.Join(texts, "\n\n")
	return result, nil
}
