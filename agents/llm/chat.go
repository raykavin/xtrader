package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/raykavin/xtrader/tools/log"
)

// Chat is a language model behind the API of a provider, answering a
// conversation with text or with calls of the tools it is offered: the
// supervisor of the agents of the controller, eg. It shares the requests of
// the providers of the decisions (see Provider): the same retries, errors
// (ErrTimeout, ErrRateLimited, ErrUnavailable, *APIError) and API keys left
// out of them.
type Chat interface {
	// Complete answers the conversation of messages with the instructions
	// system, the tools offered to the model, none for text only.
	Complete(ctx context.Context, system string, messages []ChatMessage, tools []ToolSpec) (ChatAnswer, error)
	// String names the provider and its model, eg: openai gpt-5.
	String() string
}

// The roles of the messages of a conversation of a Chat.
const (
	ChatUser      = "user"      // a message of the user
	ChatAssistant = "assistant" // an answer of the model, with its calls of tools
	ChatTool      = "tool"      // the result of a call of a tool, to the model
)

// ChatMessage is a message of a conversation: of the user, of the model with
// the calls of tools it answered, or the result of a call of a tool, of the
// call of ToolCallID.
type ChatMessage struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// ToolSpec is a tool offered to the model: Parameters is the JSON schema of
// the arguments of its calls, an object.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall is a call of a tool the model answered: Arguments, a JSON object,
// are the model's, to be validated before anything is done with them.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ChatAnswer is the answer of the model: its text and the calls of tools it
// asked for, the tokens of it, and whether it was cut at the max tokens.
type ChatAnswer struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
	Truncated bool
}

// NewChat creates the chat of the provider name, openai or anthropic,
// answering with model, authenticated with apiKey, with the options of the
// providers but WithStrategy, which it ignores: the instructions come with
// each conversation.
func NewChat(name, model, apiKey string, options ...ProviderOption) (Chat, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ProviderOpenAI:
		c, err := newClient(ProviderOpenAI, openAIBaseURL, OpenAIKeyEnv, model, apiKey, options)
		if err != nil {
			return nil, err
		}
		return &openAIChat{client: c}, nil
	case ProviderAnthropic:
		c, err := newClient(ProviderAnthropic, anthropicBaseURL, AnthropicKeyEnv, model, apiKey, options)
		if err != nil {
			return nil, err
		}
		return &anthropicChat{client: c}, nil
	}
	return nil, fmt.Errorf("llm: invalid provider %q: %s or %s", name, ProviderOpenAI, ProviderAnthropic)
}

// recordChat logs the usage of an answer of the chat, with the one of the day.
func (c client) recordChat(usage Usage) {
	if c.usage == nil {
		return
	}
	today, requests := c.usage.add(usage)
	log.Infof("[LLM] %s | chat | tokens: in %d (cached %d), cache written %d, out %d (reasoning %d) | today: "+
		"%d requests, in %d, out %d", c, usage.Input, usage.Cached, usage.CacheWritten, usage.Output,
		usage.Reasoning, requests, today.Input, today.Output)
}

// emptyObject is the schema of a tool without arguments.
var emptyObject = json.RawMessage(`{"type":"object","properties":{}}`)

// parameters returns the schema of the arguments of tool, an object.
func (t ToolSpec) parameters() json.RawMessage {
	if len(t.Parameters) == 0 {
		return emptyObject
	}
	return t.Parameters
}

// arguments returns the arguments of a call, {} without any.
func arguments(raw json.RawMessage) json.RawMessage {
	if strings.TrimSpace(string(raw)) == "" {
		return json.RawMessage(`{}`)
	}
	return raw
}

// openAIChat is the Chat of OpenAI, through the Chat Completions API and its
// function calling.
type openAIChat struct {
	client
}

type openAIChatMessage struct {
	Role       string               `json:"role"`
	Content    *string              `json:"content"`
	ToolCalls  []openAIChatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

type openAIChatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIChatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type openAIChatRequest struct {
	Model               string              `json:"model"`
	Messages            []openAIChatMessage `json:"messages"`
	Tools               []openAIChatTool    `json:"tools,omitempty"`
	ToolChoice          string              `json:"tool_choice,omitempty"`
	MaxCompletionTokens int                 `json:"max_completion_tokens"`
	ReasoningEffort     string              `json:"reasoning_effort,omitempty"`
}

type openAIChatAnswer struct {
	Choices []struct {
		Message struct {
			Content   *string              `json:"content"`
			Refusal   *string              `json:"refusal"`
			ToolCalls []openAIChatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

// Complete answers the conversation, see Chat.
func (o *openAIChat) Complete(ctx context.Context, system string, messages []ChatMessage,
	tools []ToolSpec) (ChatAnswer, error) {
	text := func(value string) *string { return &value }
	request := openAIChatRequest{Model: o.model, MaxCompletionTokens: o.maxTokens, ReasoningEffort: o.reasoning,
		Messages: []openAIChatMessage{{Role: "system", Content: text(system)}}}
	for _, m := range messages {
		message := openAIChatMessage{Role: m.Role, Content: text(m.Content), ToolCallID: m.ToolCallID}
		for _, call := range m.ToolCalls {
			c := openAIChatToolCall{ID: call.ID, Type: "function"}
			c.Function.Name, c.Function.Arguments = call.Name, string(arguments(call.Arguments))
			message.ToolCalls = append(message.ToolCalls, c)
		}
		if m.Role == ChatAssistant && m.Content == "" && len(m.ToolCalls) > 0 {
			message.Content = nil
		}
		request.Messages = append(request.Messages, message)
	}
	for _, tool := range tools {
		t := openAIChatTool{Type: "function"}
		t.Function.Name, t.Function.Description, t.Function.Parameters = tool.Name, tool.Description,
			tool.parameters()
		request.Tools = append(request.Tools, t)
	}
	if len(tools) > 0 {
		request.ToolChoice = "auto"
	}

	headers := http.Header{"Authorization": {"Bearer " + o.apiKey}}
	data, err := o.post(ctx, "/chat/completions", headers, request)
	if err != nil {
		return ChatAnswer{}, err
	}
	var answer openAIChatAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		var usage openAIUsage
		if usageIn(data, &usage) {
			o.recordChat(usage.usage())
		}
		return ChatAnswer{}, invalidAnswer(o.name, "%v", err)
	}
	usage := answer.Usage.usage()
	o.recordChat(usage)
	if len(answer.Choices) == 0 {
		return ChatAnswer{}, invalidAnswer(o.name, "no choice")
	}
	choice := answer.Choices[0]
	if choice.Message.Refusal != nil && *choice.Message.Refusal != "" {
		return ChatAnswer{}, invalidAnswer(o.name, "refused: %s", o.redact(*choice.Message.Refusal))
	}
	result := ChatAnswer{Usage: usage, Truncated: choice.FinishReason == "length"}
	if choice.Message.Content != nil {
		result.Content = strings.TrimSpace(*choice.Message.Content)
	}
	for _, call := range choice.Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name,
			Arguments: arguments(json.RawMessage(call.Function.Arguments))})
	}
	return result, nil
}

// anthropicChat is the Chat of Anthropic, through the Messages API and its
// tools.
type anthropicChat struct {
	client
}

type anthropicChatBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicChatMessage struct {
	Role    string               `json:"role"`
	Content []anthropicChatBlock `json:"content"`
}

type anthropicChatRequest struct {
	Model      string                 `json:"model"`
	MaxTokens  int                    `json:"max_tokens"`
	System     string                 `json:"system"`
	Messages   []anthropicChatMessage `json:"messages"`
	Tools      []anthropicTool        `json:"tools,omitempty"`
	ToolChoice *anthropicToolChoice   `json:"tool_choice,omitempty"`
}

type anthropicChatAnswer struct {
	Content    []anthropicChatBlock `json:"content"`
	StopReason string               `json:"stop_reason"`
	Usage      anthropicUsage       `json:"usage"`
}

// Complete answers the conversation, see Chat. The results of consecutive
// calls of tools go in one message of the user, as the API wants.
func (a *anthropicChat) Complete(ctx context.Context, system string, messages []ChatMessage,
	tools []ToolSpec) (ChatAnswer, error) {
	request := anthropicChatRequest{Model: a.model, MaxTokens: a.maxTokens, System: system}
	for _, m := range messages {
		switch m.Role {
		case ChatTool:
			block := anthropicChatBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content}
			if n := len(request.Messages); n > 0 && request.Messages[n-1].Role == ChatUser &&
				request.Messages[n-1].Content[0].Type == "tool_result" {
				request.Messages[n-1].Content = append(request.Messages[n-1].Content, block)
				continue
			}
			request.Messages = append(request.Messages, anthropicChatMessage{Role: ChatUser,
				Content: []anthropicChatBlock{block}})
		case ChatAssistant:
			message := anthropicChatMessage{Role: ChatAssistant}
			if m.Content != "" {
				message.Content = append(message.Content, anthropicChatBlock{Type: "text", Text: m.Content})
			}
			for _, call := range m.ToolCalls {
				message.Content = append(message.Content, anthropicChatBlock{Type: "tool_use", ID: call.ID,
					Name: call.Name, Input: arguments(call.Arguments)})
			}
			if len(message.Content) > 0 {
				request.Messages = append(request.Messages, message)
			}
		default:
			request.Messages = append(request.Messages, anthropicChatMessage{Role: ChatUser,
				Content: []anthropicChatBlock{{Type: "text", Text: m.Content}}})
		}
	}
	for _, tool := range tools {
		request.Tools = append(request.Tools, anthropicTool{Name: tool.Name, Description: tool.Description,
			InputSchema: tool.parameters()})
	}
	if len(tools) > 0 {
		request.ToolChoice = &anthropicToolChoice{Type: "auto"}
	}

	headers := http.Header{"X-Api-Key": {a.apiKey}, "Anthropic-Version": {anthropicVersion}}
	data, err := a.post(ctx, "/messages", headers, request)
	if err != nil {
		return ChatAnswer{}, err
	}
	var answer anthropicChatAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		var usage anthropicUsage
		if usageIn(data, &usage) {
			a.recordChat(usage.usage())
		}
		return ChatAnswer{}, invalidAnswer(a.name, "%v", err)
	}
	usage := answer.Usage.usage()
	a.recordChat(usage)
	switch answer.StopReason {
	case "refusal":
		return ChatAnswer{}, invalidAnswer(a.name, "refused")
	case "model_context_window_exceeded":
		return ChatAnswer{}, invalidAnswer(a.name, "cut at the context window of the model")
	}
	result := ChatAnswer{Usage: usage, Truncated: answer.StopReason == "max_tokens"}
	var texts []string
	for _, block := range answer.Content {
		switch block.Type {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				texts = append(texts, text)
			}
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: block.ID, Name: block.Name,
				Arguments: arguments(block.Input)})
		}
	}
	result.Content = strings.Join(texts, "\n\n")
	return result, nil
}
