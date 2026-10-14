package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

// ErrUnauthorized is the answer of the controller to a token it didn't give,
// or gave to a start of the agent that ended: the agent isn't the one it runs,
// eg: left running by a controller before, and must stop.
var ErrUnauthorized = errors.New("remote: the controller refused the token: the agent isn't the one it started")

// errNotFound is the answer of the controller to a record it doesn't have.
var errNotFound = errors.New("remote: not found")

// StatusError is an answer of the controller other than a success.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("remote: the controller answered HTTP %d: %s", e.Status, e.Message)
}

// RefusedError is the answer of the controller to a batch of the outbox whose
// record at Index it refuses for good, none of them written.
type RefusedError struct {
	Index   int
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("remote: the controller refused the record %d: %s", e.Index, e.Message)
}

// The timeouts of the requests to the controller: one on the loopback answers
// at once, but for GET /runtime, which waits for a change.
const (
	requestTimeout = 20 * time.Second
	runtimeWait    = 25 * time.Second
)

// Client calls the internal API of the controller for an agent, with the
// token of its start (see agentapi).
type Client struct {
	base  string // the URL of the API, with its prefix
	token string
	http  *http.Client
}

// NewClient returns the client of the internal API at baseURL, eg:
// http://127.0.0.1:8090, authenticated with token.
func NewClient(baseURL, token string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("remote: invalid URL of the controller %q", baseURL)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("remote: the token of the agent is empty")
	}
	return &Client{base: strings.TrimRight(parsed.String(), "/") + agentapi.Prefix, token: strings.TrimSpace(token),
		http: &http.Client{}}, nil
}

// do sends a request of method to path with the JSON of body, if any, and
// decodes the JSON of the answer into answer, if any. It returns the status
// of the answer.
func (c *Client) do(ctx context.Context, method, path string, header http.Header, body, answer any) (int,
	error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("remote: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, fmt.Errorf("remote: %w", err)
	}
	for key, values := range header {
		request.Header[key] = values
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		return 0, fmt.Errorf("remote: the controller: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return response.StatusCode, ErrUnauthorized
	case response.StatusCode == http.StatusNotModified:
		return response.StatusCode, nil
	case response.StatusCode == http.StatusNotFound:
		return response.StatusCode, errNotFound
	case response.StatusCode == http.StatusUnprocessableEntity:
		var refused agentapi.OutboxError
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&refused); err != nil {
			return response.StatusCode, &StatusError{Status: response.StatusCode, Message: err.Error()}
		}
		return response.StatusCode, &RefusedError{Index: refused.Index, Message: refused.Error}
	case response.StatusCode < 200 || response.StatusCode > 299:
		var failure ui.ErrorResponse
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&failure)
		return response.StatusCode, &StatusError{Status: response.StatusCode, Message: failure.Error}
	}
	if answer == nil {
		return response.StatusCode, nil
	}
	if raw, ok := answer.(*rawAnswer); ok {
		raw.mediaType = response.Header.Get("Content-Type")
		raw.data, err = io.ReadAll(io.LimitReader(response.Body, agentapi.MaxBody))
		return response.StatusCode, err
	}
	if err := json.NewDecoder(response.Body).Decode(answer); err != nil {
		return response.StatusCode, fmt.Errorf("remote: the answer of the controller: %w", err)
	}
	return response.StatusCode, nil
}

// rawAnswer is an answer read as it is, eg: an attachment.
type rawAnswer struct {
	mediaType string
	data      []byte
}

// get gets path into answer, bounded by the timeout of a request.
func (c *Client) get(ctx context.Context, path string, answer any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_, err := c.do(ctx, http.MethodGet, path, nil, nil, answer)
	return err
}

// Config returns the settings of the agent, its secrets included.
func (c *Client) Config(ctx context.Context) (agentapi.Config, error) {
	var cfg agentapi.Config
	return cfg, c.get(ctx, "/config", &cfg)
}

// Runtime returns what the agent applies while it runs: with version, the
// one it applies, it waits up to wait for a change, changed false without
// one.
func (c *Client) Runtime(ctx context.Context, version int64, wait time.Duration) (agentapi.Runtime, bool,
	error) {
	ctx, cancel := context.WithTimeout(ctx, wait+requestTimeout)
	defer cancel()
	path, header := "/runtime", http.Header{}
	if version > 0 {
		path += "?wait=" + url.QueryEscape(wait.String())
		header.Set("If-None-Match", `"`+strconv.FormatInt(version, 10)+`"`)
	}
	var runtime agentapi.Runtime
	status, err := c.do(ctx, http.MethodGet, path, header, nil, &runtime)
	if err != nil {
		return agentapi.Runtime{}, false, err
	}
	if status == http.StatusNotModified {
		return agentapi.Runtime{}, false, nil
	}
	return runtime, true, nil
}

// Prompt returns the prompt selected for the agent, false for its default
// one.
func (c *Client) Prompt(ctx context.Context) (agentapi.Prompt, bool, error) {
	var prompt agentapi.Prompt
	err := c.get(ctx, "/prompt", &prompt)
	if errors.Is(err, errNotFound) {
		return agentapi.Prompt{}, false, nil
	}
	return prompt, err == nil, err
}

// LeaseOrderIDs leases count order IDs.
func (c *Client) LeaseOrderIDs(ctx context.Context, count int64) (agentapi.OrderIDs, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var ids agentapi.OrderIDs
	_, err := c.do(ctx, http.MethodPost, "/order-ids", nil, agentapi.OrderIDsRequest{Count: count}, &ids)
	return ids, err
}

// Orders returns the orders of the agent after the ID after, by ID, limit at
// most.
func (c *Client) Orders(ctx context.Context, after int64, limit int) ([]model.Order, error) {
	var orders []model.Order
	return orders, c.get(ctx, fmt.Sprintf("/orders?after=%d&limit=%d", after, limit), &orders)
}

// Discards returns the positions the order controller of the agent discarded,
// by the fill after which each one was.
func (c *Client) Discards(ctx context.Context) ([]model.Discard, error) {
	var discards []model.Discard
	return discards, c.get(ctx, "/discards", &discards)
}

// Decisions returns the last decisions of pair, of every pair when empty,
// newest first, limit at most.
func (c *Client) Decisions(ctx context.Context, pair string, limit int) ([]ui.Decision, error) {
	var decisions []ui.Decision
	return decisions, c.get(ctx, fmt.Sprintf("/decisions?pair=%s&limit=%d", url.QueryEscape(pair), limit),
		&decisions)
}

// DecisionsPerPair returns the last decisions of each pair, perPair of them.
func (c *Client) DecisionsPerPair(ctx context.Context, perPair int) ([]ui.Decision, error) {
	var decisions []ui.Decision
	return decisions, c.get(ctx, fmt.Sprintf("/decisions?per_pair=%d", perPair), &decisions)
}

// Decision returns the decision of id, false when unknown.
func (c *Client) Decision(ctx context.Context, id string) (ui.Decision, bool, error) {
	var d ui.Decision
	err := c.get(ctx, "/decisions/"+url.PathEscape(id), &d)
	if errors.Is(err, errNotFound) {
		return ui.Decision{}, false, nil
	}
	return d, err == nil, err
}

// Attachment returns the attachment index of the decision id and its media
// type, ui.ErrUnknownDecision when there is none.
func (c *Client) Attachment(ctx context.Context, id string, index int) (string, []byte, error) {
	var raw rawAnswer
	err := c.get(ctx, fmt.Sprintf("/decisions/%s/attachments/%d", url.PathEscape(id), index), &raw)
	if errors.Is(err, errNotFound) {
		return "", nil, ui.ErrUnknownDecision
	}
	return raw.mediaType, raw.data, err
}

// DecisionLinks returns the decision that placed each order of the agent.
func (c *Client) DecisionLinks(ctx context.Context) (map[int64]string, error) {
	var links []agentapi.DecisionLink
	if err := c.get(ctx, "/decision-links", &links); err != nil {
		return nil, err
	}
	placed := make(map[int64]string, len(links))
	for _, link := range links {
		placed[link.OrderID] = link.DecisionID
	}
	return placed, nil
}

// JournalMemory returns the last decisions of each pair of the journal of the
// agent, perPair of them.
func (c *Client) JournalMemory(ctx context.Context, perPair int) ([]agentapi.Remembered, error) {
	var remembered []agentapi.Remembered
	return remembered, c.get(ctx, fmt.Sprintf("/journal/memory?per_pair=%d", perPair), &remembered)
}

// Outbox writes the records of request, and returns the Seq of the last one
// written: a *RefusedError names the record refused, none written.
func (c *Client) Outbox(ctx context.Context, request agentapi.OutboxRequest) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*requestTimeout)
	defer cancel()
	var answer agentapi.OutboxResponse
	_, err := c.do(ctx, http.MethodPost, "/outbox", nil, request, &answer)
	return answer.Acked, err
}

// CheckOutbox asks the controller whether it writes the records, none written:
// nil when it does, a *RefusedError naming the one it refuses for good.
func (c *Client) CheckOutbox(ctx context.Context, records []agentapi.Record) error {
	ctx, cancel := context.WithTimeout(ctx, 2*requestTimeout)
	defer cancel()
	_, err := c.do(ctx, http.MethodPost, "/outbox/check", nil, agentapi.OutboxRequest{Records: records}, nil)
	return err
}
