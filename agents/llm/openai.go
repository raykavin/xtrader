package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

const openAIBaseURL = "https://api.openai.com/v1"

// OpenAI is the Provider of the models of OpenAI, eg: ChatGPT, through the
// Chat Completions API, its answer constrained to the schema of a Decision
// with a strict JSON schema (structured outputs).
type OpenAI struct {
	client
}

// NewOpenAI creates the provider of OpenAI answering with model, eg: the one
// of -llm-model, authenticated with apiKey, eg: from OPENAI_API_KEY.
func NewOpenAI(model, apiKey string, options ...ProviderOption) (*OpenAI, error) {
	c, err := newClient(ProviderOpenAI, openAIBaseURL, OpenAIKeyEnv, model, apiKey, options)
	if err != nil {
		return nil, err
	}
	return &OpenAI{client: c}, nil
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	ResponseFormat      openAIFormat    `json:"response_format"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
}

type openAIFormat struct {
	Type       string           `json:"type"`
	JSONSchema openAIJSONSchema `json:"json_schema"`
}

type openAIJSONSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Strict      bool            `json:"strict"`
	Schema      json.RawMessage `json:"schema"`
}

type openAIAnswer struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
			Refusal *string `json:"refusal"`

			// the annotations of the answer, eg: the citations of a web
			// search; the summary of the reasoning and the images, which some
			// APIs of the same shape answer, eg: OpenRouter
			Annotations []struct {
				Type        string `json:"type"`
				URLCitation struct {
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"url_citation"`
			} `json:"annotations"`
			Reasoning *string `json:"reasoning"`
			Images    []struct {
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"images"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u openAIUsage) usage() Usage {
	return Usage{Input: u.PromptTokens, Cached: u.PromptTokensDetails.CachedTokens, Output: u.CompletionTokens,
		Reasoning: u.CompletionTokensDetails.ReasoningTokens}
}

// Analyze asks the model for the decision of the context. The answer is the
// JSON of the decision in the content of the first choice.
func (o *OpenAI) Analyze(ctx context.Context, input Input) (Decision, error) {
	return o.AnalyzeWith(ctx, o.system, input)
}

// AnalyzeWith asks the model for the decision of the context with the
// instructions system, see SystemPromptProvider.
func (o *OpenAI) AnalyzeWith(ctx context.Context, system string, input Input) (Decision, error) {
	message, err := userMessage(input)
	if err != nil {
		return Decision{}, err
	}

	request := openAIRequest{
		Model: o.model,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: message},
		},
		ResponseFormat: openAIFormat{Type: "json_schema", JSONSchema: openAIJSONSchema{Name: decisionName,
			Description: decisionDescription, Strict: true, Schema: schemaOf(shapeOf(input))}},
		MaxCompletionTokens: o.maxTokens,
		ReasoningEffort:     o.reasoning,
	}
	headers := http.Header{"Authorization": {"Bearer " + o.apiKey}}
	data, err := o.post(ctx, "/chat/completions", headers, request)
	if err != nil {
		return Decision{}, err
	}

	// the tokens are billed whatever the answer, one that can't be read
	// included, when it tells them
	var answer openAIAnswer
	if err := json.Unmarshal(data, &answer); err != nil {
		var usage openAIUsage
		if usageIn(data, &usage) {
			o.record(input, usage.usage())
		}
		return Decision{}, invalidAnswer(o.name, "%v", err)
	}
	o.record(input, answer.Usage.usage())
	if len(answer.Choices) == 0 {
		return Decision{}, invalidAnswer(o.name, "no choice")
	}
	choice := answer.Choices[0]
	switch {
	case choice.Message.Refusal != nil && *choice.Message.Refusal != "":
		return Decision{}, invalidAnswer(o.name, "refused: %s", o.redact(*choice.Message.Refusal))
	case choice.FinishReason == "length":
		return Decision{}, invalidAnswer(o.name, "cut at the max tokens (%d)", o.maxTokens)
	case choice.FinishReason == "content_filter":
		return Decision{}, invalidAnswer(o.name, "cut by the content filter")
	case choice.Message.Content == nil || *choice.Message.Content == "":
		return Decision{}, invalidAnswer(o.name, "empty content")
	}
	decision, err := o.decide([]byte(*choice.Message.Content), input)
	if decision.Action == "" {
		return decision, err
	}

	// what the answer holds beside the decision, kept with it
	if reasoning := choice.Message.Reasoning; reasoning != nil && strings.TrimSpace(*reasoning) != "" {
		decision.Notes = append(decision.Notes, Note{Kind: "reasoning", Text: strings.TrimSpace(*reasoning)})
	}
	for _, annotation := range choice.Message.Annotations {
		if citation := annotation.URLCitation; annotation.Type == "url_citation" && citation.URL != "" {
			decision.Notes = append(decision.Notes, Note{Kind: "citation", Title: citation.Title, URL: citation.URL})
		}
	}
	for _, image := range choice.Message.Images {
		if attachment, ok := dataURL(image.ImageURL.URL); ok {
			decision.Attachments = append(decision.Attachments, attachment)
		} else if strings.HasPrefix(image.ImageURL.URL, "https://") {
			decision.Notes = append(decision.Notes, Note{Kind: "image", URL: image.ImageURL.URL})
		}
	}
	return decision, err
}

// dataURL returns the file of a data URL of base64, eg:
// data:image/png;base64,iVBOR..., false for another URL.
func dataURL(url string) (Attachment, bool) {
	header, data, found := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	if !found || !strings.HasPrefix(url, "data:") || !strings.HasSuffix(header, ";base64") {
		return Attachment{}, false
	}
	content, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(content) == 0 {
		return Attachment{}, false
	}
	return Attachment{MediaType: strings.TrimSuffix(header, ";base64"), Data: content}, true
}
