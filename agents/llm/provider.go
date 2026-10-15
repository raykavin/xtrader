package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/raykavin/xtrader/tools/log"
)

// Provider is the analyst of the Trader: a language model behind an API, which
// answers the market context of a candle with a decision. Analyze returns an
// error whenever the answer is not a valid decision for the context: an error
// of the API, a timeout, or an answer that fails ParseDecision, without a
// decision, or one that fails Decision.Validate, with the decision refused,
// kept with its justification but never applied.
type Provider interface {
	Analyze(ctx context.Context, input Input) (Decision, error)
}

// SystemPromptProvider is a Provider taking the instructions of the model with
// each request, eg: of the prompt the Trader switches to while it runs (see
// SetPromptSource): AnalyzeWith analyzes as Analyze, with system, the whole
// instructions (the part the code relies on and the strategy), rather than
// its own.
type SystemPromptProvider interface {
	Provider
	AnalyzeWith(ctx context.Context, system string, input Input) (Decision, error)
}

// The providers of NewProvider.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
)

// The environment variables of the API keys of the providers.
const (
	OpenAIKeyEnv    = "OPENAI_API_KEY"
	AnthropicKeyEnv = "ANTHROPIC_API_KEY"
)

// DefaultMaxTokens caps the tokens of an answer without WithMaxTokens, the
// reasoning of the models that reason included: a decision takes a few
// hundred, a model that reasons may spend them all thinking on a longer task.
const DefaultMaxTokens = 4096

var (
	// ErrTimeout is returned when the answer doesn't come in time: the
	// deadline of the context or the timeout of the HTTP client.
	ErrTimeout = errors.New("llm: timeout")
	// ErrRateLimited is returned for an answer HTTP 429: too many requests or
	// tokens, or the quota of the account spent.
	ErrRateLimited = errors.New("llm: rate limited")
	// ErrUnavailable is returned when the provider can't be reached, or
	// answers HTTP 5xx, eg: 529 when Anthropic is overloaded.
	ErrUnavailable = errors.New("llm: provider unavailable")
)

// APIError is an error answer of the API of a provider. It wraps
// ErrRateLimited for HTTP 429 and ErrUnavailable for HTTP 5xx; the other 4xx,
// eg: 401 for an invalid API key, are errors of the request.
type APIError struct {
	Provider   string        // openai or anthropic
	Status     int           // HTTP status
	Type       string        // of the error, eg: rate_limit_error
	Param      string        // the param of the request refused, eg: reasoning_effort
	Message    string        // without the API key
	RetryAfter time.Duration // the Retry-After of the answer, 0 without it
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("%s: HTTP %d", e.Provider, e.Status)
	if e.Type != "" {
		message += ": " + e.Type
	}
	if e.Param != "" {
		message += ": the param " + e.Param
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

func (e *APIError) Unwrap() error {
	switch {
	case e.Status == http.StatusTooManyRequests:
		return ErrRateLimited
	case e.Status >= 500:
		return ErrUnavailable
	}
	return nil
}

// ProviderOption configures a provider.
type ProviderOption func(*client)

// WithBaseURL sets the base URL of the API, eg: a proxy, the one of the
// provider by default.
func WithBaseURL(baseURL string) ProviderOption {
	return func(c *client) {
		c.baseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithMaxTokens caps the tokens of an answer, the thinking of the model
// included: 4096 by default, 16000 for an answer of Anthropic left to the
// model (see Anthropic).
func WithMaxTokens(tokens int) ProviderOption {
	return func(c *client) {
		c.maxTokens, c.maxTokensSet = tokens, true
	}
}

// The efforts of WithReasoningEffort, the ones of the models of OpenAI: each
// model takes some of them, eg: none and xhigh only the newer ones, minimal
// only the older ones, the API refusing the others (see APIError.Param).
var reasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh"}

// WithReasoningEffort sets how much the models that reason think before they
// answer: none, minimal, low, medium, high or xhigh, as the model takes them,
// the default of the model when empty. Less effort spends fewer output
// tokens, billed as the answer. It applies to OpenAI (reasoning_effort);
// Anthropic answers through a forced tool call, which extended thinking
// doesn't take, so it is ignored there.
func WithReasoningEffort(effort string) ProviderOption {
	return func(c *client) {
		c.reasoning = strings.ToLower(strings.TrimSpace(effort))
	}
}

// WithStrategy sets the strategy of the instructions of the model, eg: from
// ReadStrategy, the one of prompts/default.txt by default. It follows the part
// the code relies on (the actions, the rules, the context), which it can't
// change.
func WithStrategy(strategy string) ProviderOption {
	return func(c *client) {
		c.system = ""
		if stripComments(strategy) != "" {
			c.system = systemPrompt(strategy)
		}
	}
}

// WithHTTPClient sets the HTTP client of the requests, http.DefaultClient by
// default. The Trader bounds each request with its Timeout.
func WithHTTPClient(httpClient *http.Client) ProviderOption {
	return func(c *client) {
		c.http = httpClient
	}
}

// NewProvider creates the provider name, openai or anthropic, answering with
// model, authenticated with apiKey, see NewOpenAI and NewAnthropic.
func NewProvider(name, model, apiKey string, options ...ProviderOption) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ProviderOpenAI:
		return NewOpenAI(model, apiKey, options...)
	case ProviderAnthropic:
		return NewAnthropic(model, apiKey, options...)
	}
	return nil, fmt.Errorf("llm: invalid provider %q: %s or %s", name, ProviderOpenAI, ProviderAnthropic)
}

// KeyEnv returns the environment variable of the API key of the provider
// name, empty for an unknown one.
func KeyEnv(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ProviderOpenAI:
		return OpenAIKeyEnv
	case ProviderAnthropic:
		return AnthropicKeyEnv
	}
	return ""
}

// client sends the requests of a provider to its API.
type client struct {
	name      string // of the provider, eg: openai
	model     string
	apiKey    string
	baseURL   string
	maxTokens int
	reasoning string // effort of the models that reason, see WithReasoningEffort
	system    string // the instructions, see WithStrategy
	http      *http.Client
	usage     *usageMeter // tokens of the answers of the day

	maxTokensSet bool // maxTokens is the one of WithMaxTokens, not the default
}

func newClient(name, baseURL, keyEnv, model, apiKey string, options []ProviderOption) (client, error) {
	c := client{name: name, model: strings.TrimSpace(model), apiKey: strings.TrimSpace(apiKey), baseURL: baseURL,
		maxTokens: DefaultMaxTokens, http: http.DefaultClient, usage: newUsageMeter(), system: defaultSystemPrompt}
	for _, option := range options {
		option(&c)
	}

	switch {
	case c.model == "":
		return client{}, fmt.Errorf("%s: the model is required", name)
	case c.apiKey == "":
		return client{}, fmt.Errorf("%s: the API key is required, eg: from %s", name, keyEnv)
	case c.maxTokens < 1:
		return client{}, fmt.Errorf("%s: the max tokens must be positive", name)
	case c.http == nil:
		return client{}, fmt.Errorf("%s: no HTTP client", name)
	case c.system == "":
		return client{}, fmt.Errorf("%s: the strategy of the prompt is empty", name)
	case c.reasoning != "" && !slices.Contains(reasoningEfforts, c.reasoning):
		return client{}, fmt.Errorf("%s: invalid reasoning effort %q: %s", name, c.reasoning,
			strings.Join(reasoningEfforts, ", "))
	}
	return c, nil
}

// String names the provider and its model for the logs, eg: anthropic
// claude-sonnet-5.
func (c client) String() string {
	return c.name + " " + c.model
}

// A request the API answers without running it, so without billing it (see
// retriable), is sent again once, after its Retry-After or retryWait, when
// that is retryMaxWait at most and leaves retryRoom before the deadline of the
// request: the decision of the candle not lost to a moment of overload.
var (
	retryWait    = time.Second
	retryMaxWait = 10 * time.Second
	retryRoom    = 5 * time.Second
)

// retriable returns the answer of err when the API didn't run the request,
// which is worth sending again: HTTP 429 (but the quota of the account spent),
// 503 and 529 (Anthropic overloaded).
func retriable(err error) (*APIError, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return nil, false
	}
	switch apiErr.Status {
	case http.StatusTooManyRequests:
		return apiErr, apiErr.Type != "insufficient_quota"
	case http.StatusServiceUnavailable, 529:
		return apiErr, true
	}
	return nil, false
}

// post sends body as JSON to path with headers, the credentials among them,
// and returns the body of the answer, sent again once when the API didn't run
// it, see retriable. The failures are turned into ErrTimeout, ErrUnavailable
// or an *APIError.
func (c client) post(ctx context.Context, path string, headers http.Header, body any) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	return retrying(ctx, c, func() ([]byte, error) { return c.send(ctx, path, headers, data) })
}

// postStream posts body, JSON, to path with headers as post does, asking for
// the answer as a stream of server-sent events, and returns its body open for
// the caller to read and close. It is sent again as post sends it, only on an
// error of the request, before a byte of the answer is read.
func (c client) postStream(ctx context.Context, path string, headers http.Header, body any) (io.ReadCloser, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	return retrying(ctx, c, func() (io.ReadCloser, error) { return c.open(ctx, path, headers, data) })
}

// retrying returns what send returns, called again once after a wait when it
// fails with an error worth it (see retriable) and the wait fits the deadline
// of ctx.
func retrying[T any](ctx context.Context, c client, send func() (T, error)) (T, error) {
	var none T
	answer, err := send()
	apiErr, ok := retriable(err)
	if !ok {
		return answer, err
	}
	wait := apiErr.RetryAfter
	if wait <= 0 {
		wait = retryWait
	}
	if deadline, ok := ctx.Deadline(); wait > retryMaxWait || ok && time.Until(deadline) < wait+retryRoom {
		return none, err // the Trader pauses for its Retry-After
	}
	log.Warnf("[LLM] %s: %v: sent again in %s", c, err, wait)
	select {
	case <-ctx.Done():
		return none, err
	case <-time.After(wait):
	}
	return send()
}

// send sends data, JSON, to path with headers, see post.
func (c client) send(ctx context.Context, path string, headers http.Header, data []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, c.failure(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()

	answer, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, c.failure(ctx, err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, c.apiError(response, answer)
	}
	return answer, nil
}

// open sends data, JSON, to path with headers, see postStream: the body of a
// successful answer is returned unread, the one of an error read as send
// reads it.
func (c client) open(ctx context.Context, path string, headers http.Header, data []byte) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, c.failure(ctx, err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		defer func() { _ = response.Body.Close() }()
		answer, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		if err != nil {
			return nil, c.failure(ctx, err)
		}
		return nil, c.apiError(response, answer)
	}
	return response.Body, nil
}

// failure classifies an error of a request without answer: a timeout, or the
// provider unreachable.
func (c client) failure(ctx context.Context, err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%s: %w: %w", c.name, ErrTimeout, err)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	return fmt.Errorf("%s: %w: %w", c.name, ErrUnavailable, err)
}

// apiError reads the error answer of the API, the same shape on both
// providers: {"error": {"type": ..., "code": ..., "message": ...}}.
func (c client) apiError(response *http.Response, body []byte) *APIError {
	apiErr := &APIError{Provider: c.name, Status: response.StatusCode, RetryAfter: retryAfter(response.Header)}
	var answer struct {
		Error struct {
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Param   string          `json:"param"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &answer) == nil {
		apiErr.Type = answer.Error.Type
		if code := strings.Trim(string(answer.Error.Code), `"`); code != "" && code != "null" {
			apiErr.Type = code // the code of OpenAI is the more precise, eg: insufficient_quota
		}
		apiErr.Param = c.redact(answer.Error.Param)
		apiErr.Message = c.redact(answer.Error.Message)
	}
	return apiErr
}

// keyPattern matches the API keys of the providers, masked or not, eg: the
// sk-proj-****abcd of an answer of OpenAI to an invalid key.
var keyPattern = regexp.MustCompile(`sk-[A-Za-z0-9_\-*]{6,}`)

// maxMessage caps the length of the messages of the API kept in the errors.
const maxMessage = 300

// redact removes the API key from a message of the API, which is logged, and
// caps its length.
func (c client) redact(message string) string {
	if c.apiKey != "" {
		message = strings.ReplaceAll(message, c.apiKey, "[redacted]")
	}
	message = keyPattern.ReplaceAllString(message, "[redacted]")
	if runes := []rune(message); len(runes) > maxMessage {
		message = string(runes[:maxMessage]) + "..."
	}
	return message
}

// retryAfter reads how long to wait before the next request from the headers
// of an answer: retry-after-ms, or Retry-After in seconds or as a date. It
// returns 0 without them.
func retryAfter(header http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(header.Get("Retry-After-Ms"), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	value := header.Get("Retry-After")
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}

// decide decodes the decision the model of c answered, raw, and validates it
// against its context.
func (c client) decide(raw []byte, in Input) (Decision, error) {
	log.Debugf("[LLM] %s %s | %s | answer: %s", in.Symbol, in.Timeframe, c, raw)
	decision, err := parseDecision(raw, shapeOf(in))
	if err != nil {
		return Decision{}, fmt.Errorf("%s: %w", c.name, err)
	}
	if err := decision.Validate(in); err != nil {
		return decision, fmt.Errorf("%s: %w", c.name, err) // refused, its justification kept
	}
	return decision, nil
}

// invalidAnswer is an answer of the provider name without a decision.
func invalidAnswer(name, format string, args ...any) error {
	return fmt.Errorf("%s: %w", name, invalidResponse(format, args...))
}
