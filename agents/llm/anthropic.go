package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/raykavin/xtrader/tools/log"
)

const (
	anthropicBaseURL = "https://api.anthropic.com/v1"
	anthropicVersion = "2023-06-01"
)

// Anthropic is the Provider of the Claude models of Anthropic, through the
// Messages API. The answer is forced into a tool whose input schema is the one
// of a Decision: the model answers with the call of the tool, never with text.
type Anthropic struct {
	client
}

// NewAnthropic creates the provider of Anthropic answering with model, eg: the
// one of -llm-model, authenticated with apiKey, eg: from ANTHROPIC_API_KEY.
func NewAnthropic(model, apiKey string, options ...ProviderOption) (*Anthropic, error) {
	c, err := newClient(ProviderAnthropic, anthropicBaseURL, AnthropicKeyEnv, model, apiKey, options)
	if err != nil {
		return nil, err
	}
	if c.reasoning != "" {
		log.Warnf("[LLM] anthropic: the reasoning effort %q is ignored, the forced tool call of the decision "+
			"doesn't take extended thinking", c.reasoning)
	}
	return &Anthropic{client: c}, nil
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type anthropicRequest struct {
	Model      string              `json:"model"`
	MaxTokens  int                 `json:"max_tokens"`
	System     string              `json:"system"`
	Messages   []anthropicMessage  `json:"messages"`
	Tools      []anthropicTool     `json:"tools"`
	ToolChoice anthropicToolChoice `json:"tool_choice"`
}

type anthropicAnswer struct {
	Content []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`

		// the other blocks of the answer, kept with the decision: its text,
		// its thinking and its images
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
		Source   *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Analyze asks the model for the decision of the context. The answer is the
// input of the call of the tool of the decision.
func (a *Anthropic) Analyze(ctx context.Context, input Input) (Decision, error) {
	return a.AnalyzeWith(ctx, a.system, input)
}

// AnalyzeWith asks the model for the decision of the context with the
// instructions system, see SystemPromptProvider.
func (a *Anthropic) AnalyzeWith(ctx context.Context, system string, input Input) (Decision, error) {
	message, err := userMessage(input)
	if err != nil {
		return Decision{}, err
	}

	request := anthropicRequest{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: message}},
		Tools: []anthropicTool{{Name: decisionName, Description: decisionDescription,
			InputSchema: decisionSchema}},
		ToolChoice: anthropicToolChoice{Type: "tool", Name: decisionName},
	}
	headers := http.Header{"X-Api-Key": {a.apiKey}, "Anthropic-Version": {anthropicVersion}}
	data, err := a.post(ctx, "/messages", headers, request)
	if err != nil {
		return Decision{}, err
	}

	var answer anthropicAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		return Decision{}, invalidAnswer(a.name, "%v", err)
	}
	// the tokens are billed whatever the answer; input_tokens leaves out the
	// ones read from and written to the cache
	a.record(input, Usage{Input: answer.Usage.InputTokens + answer.Usage.CacheReadInputTokens +
		answer.Usage.CacheCreationInputTokens, Cached: answer.Usage.CacheReadInputTokens,
		Output: answer.Usage.OutputTokens})
	switch answer.StopReason {
	case "max_tokens":
		return Decision{}, invalidAnswer(a.name, "cut at the max tokens (%d)", a.maxTokens)
	case "refusal":
		return Decision{}, invalidAnswer(a.name, "refused")
	}
	for _, block := range answer.Content {
		if block.Type != "tool_use" || block.Name != decisionName {
			continue
		}
		decision, err := a.decide(block.Input, input)
		if decision.Action == "" {
			return decision, err
		}
		// the blocks beside the call of the decision, kept with it
		for _, other := range answer.Content {
			switch {
			case other.Type == "text" && strings.TrimSpace(other.Text) != "":
				decision.Notes = append(decision.Notes, Note{Kind: "text", Text: strings.TrimSpace(other.Text)})
			case other.Type == "thinking" && strings.TrimSpace(other.Thinking) != "":
				decision.Notes = append(decision.Notes, Note{Kind: "reasoning", Text: strings.TrimSpace(other.Thinking)})
			case other.Type == "image" && other.Source != nil && other.Source.Type == "base64":
				if data, err := base64.StdEncoding.DecodeString(other.Source.Data); err == nil && len(data) > 0 {
					decision.Attachments = append(decision.Attachments,
						Attachment{MediaType: other.Source.MediaType, Data: data})
				}
			}
		}
		return decision, err
	}
	return Decision{}, invalidAnswer(a.name, "no call of the %s tool (stop reason %q)", decisionName,
		answer.StopReason)
}
