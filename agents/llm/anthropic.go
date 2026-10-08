package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/raykavin/xtrader/tools/log"
)

const (
	anthropicBaseURL = "https://api.anthropic.com/v1"
	anthropicVersion = "2023-06-01"
)

const (
	// anthropicForcedRetry is how long the tool is left to a model that
	// refused its forced call before the forced call is tried again, eg: the
	// refusal of a model since lifted.
	anthropicForcedRetry = time.Hour
	// anthropicAutoMaxTokens caps the tokens of an answer left to the model,
	// without -llm-max-tokens: the models that refuse the forced call think
	// before they answer, their thinking counted in the max tokens.
	anthropicAutoMaxTokens = 16000
)

// Anthropic is the Provider of the Claude models of Anthropic, through the
// Messages API. The answer is forced into a tool whose input schema is the one
// of a Decision: the model answers with the call of the tool, never with text.
// A model that refuses the forced call, eg: claude-opus-5-5, is left the tool,
// asked to answer with it, the forced call tried again each
// anthropicForcedRetry; its decision is then read from the text too when it
// answers without the tool, still through ParseDecision and Decision.Validate.
type Anthropic struct {
	client
	// autoSince is when the model refused the forced call of the tool, in
	// Unix nanoseconds: the tool left to it since; 0 while it takes it
	autoSince atomic.Int64
	now       func() time.Time
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
	return &Anthropic{client: c, now: time.Now}, nil
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
	Name string `json:"name,omitempty"`
}

type anthropicRequest struct {
	Model      string              `json:"model"`
	MaxTokens  int                 `json:"max_tokens"`
	System     string              `json:"system"`
	Messages   []anthropicMessage  `json:"messages"`
	Tools      []anthropicTool     `json:"tools"`
	ToolChoice anthropicToolChoice `json:"tool_choice"`
}

type anthropicBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// the other blocks of the answer, kept with the decision: its text, its
	// thinking and its images
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
	Source   *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

type anthropicAnswer struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Analyze asks the model for the decision of the context. The answer is the
// input of the call of the tool of the decision or, the tool left to the
// model, the JSON of the decision in its text.
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

	headers := http.Header{"X-Api-Key": {a.apiKey}, "Anthropic-Version": {anthropicVersion}}
	forced, retried := a.forced()
	request := a.request(system, message, forced)
	data, err := a.post(ctx, "/messages", headers, request)
	switch {
	case forced && refusesForcedTool(err):
		if since := a.autoSince.Swap(a.now().UnixNano()); since == 0 {
			log.Warnf("[LLM] anthropic: %s refuses the forced call of the %s tool: the tool is left to it, "+
				"asked to answer with it, the forced call tried again in %s", a.model, decisionName, anthropicForcedRetry)
		} else {
			log.Debugf("[LLM] anthropic: %s still refuses the forced call of the %s tool", a.model, decisionName)
		}
		request = a.request(system, message, false)
		data, err = a.post(ctx, "/messages", headers, request)
	case retried && err == nil:
		a.autoSince.Store(0)
		log.Infof("[LLM] anthropic: %s takes the forced call of the %s tool again", a.model, decisionName)
	}
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
		return Decision{}, invalidAnswer(a.name, "cut at the max tokens (%d)", request.MaxTokens)
	case "model_context_window_exceeded":
		return Decision{}, invalidAnswer(a.name, "cut at the context window of the model")
	case "pause_turn":
		return Decision{}, invalidAnswer(a.name, "paused before the end of the answer")
	case "refusal":
		return Decision{}, invalidAnswer(a.name, "refused")
	}
	for i, block := range answer.Content {
		if block.Type != "tool_use" || block.Name != decisionName {
			continue
		}
		decision, err := a.decide(block.Input, input)
		if decision.Action == "" {
			return decision, err
		}
		return a.keepBlocks(decision, answer.Content, i, ""), err
	}
	if request.ToolChoice.Type == "auto" {
		// left the tool, the model may answer the decision in its text: only
		// one in the whole answer, which tells the decision
		found, decided, raw, rest := 0, -1, []byte(nil), ""
		for i, block := range answer.Content {
			if block.Type != "text" {
				continue
			}
			if r, t, n := decisionInText(block.Text); n > 0 {
				if found == 0 {
					decided, raw, rest = i, r, t
				}
				found += n
			}
		}
		switch {
		case found > 1:
			return Decision{}, invalidAnswer(a.name, "no call of the %s tool, and %d decisions in the text",
				decisionName, found)
		case found == 1:
			decision, err := a.decide(raw, input)
			if decision.Action == "" {
				return decision, err
			}
			return a.keepBlocks(decision, answer.Content, decided, rest), err
		}
	}
	return Decision{}, invalidAnswer(a.name, "no call of the %s tool (stop reason %q)", decisionName,
		answer.StopReason)
}

// forced reports whether the request asks for the forced call of the tool:
// unless the model refused it, less than anthropicForcedRetry ago. retried
// reports a request trying it again after the refusal, only one at a time.
func (a *Anthropic) forced() (forced, retried bool) {
	since := a.autoSince.Load()
	if since == 0 {
		return true, false
	}
	now := a.now()
	if now.Sub(time.Unix(0, since)) < anthropicForcedRetry {
		return false, false
	}
	// the next requests stay left to the model while this one tries
	retried = a.autoSince.CompareAndSwap(since, now.UnixNano())
	return retried, retried
}

// keepBlocks keeps with decision the blocks of the answer beside the one of
// the decision, decided: its text, its thinking, its images. rest is what is
// left of the text of decided without the JSON of the decision.
func (a *Anthropic) keepBlocks(decision Decision, blocks []anthropicBlock, decided int, rest string) Decision {
	if rest = strings.TrimSpace(rest); rest != "" {
		decision.Notes = append(decision.Notes, Note{Kind: "text", Text: rest})
	}
	for i, other := range blocks {
		switch {
		case i == decided:
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
	return decision
}

// decisionInText finds the JSON of a decision in the text of an answer: an
// object with an action, eg: in a ```json block. It returns the JSON of the
// first one, the text without it (and its block) and how many there are: more
// than one, the text doesn't tell which is the decision.
func decisionInText(text string) (raw []byte, rest string, n int) {
	for i := 0; i < len(text); {
		at := strings.IndexByte(text[i:], '{')
		if at < 0 {
			break
		}
		at += i
		decoder := json.NewDecoder(strings.NewReader(text[at:]))
		var object map[string]json.RawMessage
		if decoder.Decode(&object) != nil {
			i = at + 1
			continue
		}
		end := at + int(decoder.InputOffset())
		if _, ok := object["action"]; ok {
			if n++; n == 1 {
				raw = []byte(text[at:end])
				rest = withoutBlock(text, at, end)
			}
		}
		i = end
	}
	return raw, rest, n
}

// The fences of a block of code, before and after its text.
var (
	fenceBefore = regexp.MustCompile("```[a-zA-Z]*\\s*$")
	fenceAfter  = regexp.MustCompile("^\\s*```")
)

// withoutBlock is text without its part [start, end), and the fences of a
// block of code around it.
func withoutBlock(text string, start, end int) string {
	before, after := text[:start], text[end:]
	if loc := fenceBefore.FindStringIndex(before); loc != nil {
		if fence := fenceAfter.FindStringIndex(after); fence != nil {
			before, after = before[:loc[0]], after[fence[1]:]
		}
	}
	return strings.TrimSpace(strings.TrimSpace(before) + "\n\n" + strings.TrimSpace(after))
}

// answerWithTheTool asks the model to answer with the tool of the decision,
// when left to it.
const answerWithTheTool = "\n\nAnswer only with a call of the " + decisionName + " tool."

// request is the request of the decision of message, with the instructions
// system: the tool of the decision forced, or left to the model, asked to
// answer with it.
func (a *Anthropic) request(system, message string, forced bool) anthropicRequest {
	choice := anthropicToolChoice{Type: "tool", Name: decisionName}
	maxTokens := a.maxTokens
	if !forced {
		choice = anthropicToolChoice{Type: "auto"}
		message += answerWithTheTool
		if !a.maxTokensSet {
			maxTokens = max(maxTokens, anthropicAutoMaxTokens) // the thinking of the model included
		}
	}
	return anthropicRequest{
		Model:     a.model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: message}},
		Tools: []anthropicTool{{Name: decisionName, Description: decisionDescription,
			InputSchema: decisionSchema}},
		ToolChoice: choice,
	}
}

// forcedToolRefusal matches the message of the refusal of the forced call of a
// tool, eg: tool_choice: type "tool" and "any" are not supported for this
// model; not the other errors of tool_choice, eg: one of its fields invalid.
var forcedToolRefusal = regexp.MustCompile(`(?i)tool_choice\b.*\b(?:tool|any)\b.*\b(?:not supported|unsupported|no longer supported|isn't supported)\b`)

// refusesForcedTool reports whether err is the refusal of the forced call of a
// tool by the model.
func refusesForcedTool(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest &&
		forcedToolRefusal.MatchString(apiErr.Message)
}
