package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// mcpProtocolVersion is the version of the Model Context Protocol spoken with
// the server of the MetaTrader 5 terminal.
const mcpProtocolVersion = "2025-06-18"

// mcpAllowedTools are the only tools of the server the client calls: the
// read-only ones of the economic calendar. The server of the terminal also
// reads, writes and deletes files, sends web requests and places orders, and
// the client refuses those before any request, whatever its caller asks.
var mcpAllowedTools = map[string]bool{
	"economic_calendar_list_events_by_currency": true,
	"economic_calendar_list_values":             true,
}

// ErrToolNotAllowed is returned by the MCP client for a tool out of
// mcpAllowedTools.
var ErrToolNotAllowed = errors.New("llm: MCP tool not allowed")

// mcpClient calls the tools of an MCP server over its streamable HTTP
// transport: JSON-RPC 2.0 requests posted to one endpoint, answered in JSON or
// in a server-sent event, within a session opened by initialize and opened
// again once when the server forgets it.
type mcpClient struct {
	url    string
	token  string
	client *http.Client

	mtx     sync.Mutex // guards the session and the ids
	session string
	ready   bool
	id      int64
}

func newMCPClient(url, token string, client *http.Client) *mcpClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &mcpClient{url: strings.TrimSuffix(url, "/"), token: token, client: client}
}

// mcpError is a JSON-RPC error answer of the server.
type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *mcpError) Error() string {
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// mcpToolError is a tool that ran and failed, eg: an argument it refused.
type mcpToolError struct {
	Tool    string
	Message string
}

func (e *mcpToolError) Error() string {
	return fmt.Sprintf("llm: MCP tool %s failed: %s", e.Tool, e.Message)
}

// mcpHTTPError is an HTTP error status of the server.
type mcpHTTPError struct {
	Status int
	Body   string
}

func (e *mcpHTTPError) Error() string {
	return fmt.Sprintf("MCP server: HTTP %d %s", e.Status, strings.TrimSpace(e.Body))
}

// callTool calls a tool of mcpAllowedTools and decodes the JSON text of its
// result into out. A result flagged isError is an error with its text.
func (c *mcpClient) callTool(ctx context.Context, name string, args map[string]any, out any) error {
	if !mcpAllowedTools[name] {
		return fmt.Errorf("%w: %s", ErrToolNotAllowed, name)
	}
	if args == nil {
		args = map[string]any{}
	}

	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result); err != nil {
		return fmt.Errorf("llm: MCP tool %s: %w", name, err)
	}

	var text strings.Builder
	for _, content := range result.Content {
		if content.Type == "text" {
			text.WriteString(content.Text)
		}
	}
	if result.IsError {
		return &mcpToolError{Tool: name, Message: strings.TrimSpace(text.String())}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		return fmt.Errorf("llm: MCP tool %s: invalid result: %w", name, err)
	}
	return nil
}

// request sends a request within the session, opening it first, and once more
// after the server answers 404 to a session it forgot.
func (c *mcpClient) request(ctx context.Context, method string, params, out any) error {
	for attempt := 0; ; attempt++ {
		if err := c.open(ctx); err != nil {
			return err
		}
		err := c.post(ctx, method, params, out, false)
		var status *mcpHTTPError
		if attempt == 0 && errors.As(err, &status) && status.Status == http.StatusNotFound {
			c.mtx.Lock()
			c.ready, c.session = false, ""
			c.mtx.Unlock()
			continue
		}
		return err
	}
}

// open initializes the session once: initialize, then the initialized
// notification.
func (c *mcpClient) open(ctx context.Context) error {
	c.mtx.Lock()
	ready := c.ready
	c.mtx.Unlock()
	if ready {
		return nil
	}

	params := map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "xtrader-llm", "version": "1.0"},
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.post(ctx, "initialize", params, &result, false); err != nil {
		return fmt.Errorf("llm: MCP initialize: %w", err)
	}
	if err := c.post(ctx, "notifications/initialized", nil, nil, true); err != nil {
		return fmt.Errorf("llm: MCP initialized: %w", err)
	}

	c.mtx.Lock()
	c.ready = true
	c.mtx.Unlock()
	return nil
}

// post posts one JSON-RPC message and decodes the result of its answer, none
// for a notification.
func (c *mcpClient) post(ctx context.Context, method string, params, out any, notification bool) error {
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		message["params"] = params
	}
	var id int64
	if !notification {
		c.mtx.Lock()
		c.id++
		id = c.id
		c.mtx.Unlock()
		message["id"] = id
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.mtx.Lock()
	if c.session != "" {
		request.Header.Set("Mcp-Session-Id", c.session)
	}
	c.mtx.Unlock()

	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return err
	}
	if session := response.Header.Get("Mcp-Session-Id"); session != "" {
		c.mtx.Lock()
		c.session = session
		c.mtx.Unlock()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return &mcpHTTPError{Status: response.StatusCode, Body: string(data)}
	}
	if notification {
		return nil
	}

	payload, err := mcpPayload(response.Header.Get("Content-Type"), data, id)
	if err != nil {
		return err
	}
	var answer struct {
		ID     int64           `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *mcpError       `json:"error"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		return fmt.Errorf("invalid answer: %w", err)
	}
	if answer.Error != nil {
		return answer.Error
	}
	if out == nil || len(answer.Result) == 0 {
		return nil
	}
	return json.Unmarshal(answer.Result, out)
}

// mcpPayload returns the JSON-RPC answer of id from a body in JSON, or in
// server-sent events (the data of the event answering id).
func mcpPayload(contentType string, data []byte, id int64) ([]byte, error) {
	if !strings.HasPrefix(strings.TrimSpace(contentType), "text/event-stream") {
		return data, nil
	}

	var event bytes.Buffer
	flush := func() ([]byte, bool) {
		defer event.Reset()
		var probe struct {
			ID *int64 `json:"id"`
		}
		if event.Len() == 0 || json.Unmarshal(event.Bytes(), &probe) != nil || probe.ID == nil || *probe.ID != id {
			return nil, false
		}
		return append([]byte(nil), event.Bytes()...), true
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if payload, ok := flush(); ok {
				return payload, nil
			}
		case strings.HasPrefix(line, "data:"):
			if event.Len() > 0 {
				event.WriteByte('\n') // the lines of the data of an event, joined as the spec says
			}
			event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if payload, ok := flush(); ok {
		return payload, nil
	}
	return nil, fmt.Errorf("no answer to the request %d in the event stream", id)
}
