package controller

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
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/ui"
)

// apiTimeout bounds a call to the API of an agent, whose orders may take the
// broker seconds to confirm.
const apiTimeout = 30 * time.Second

// apiError is an error answered by the API of an agent.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return e.Message
}

// offline is the error of an agent whose API doesn't answer.
func offline(a Agent) error {
	return fmt.Errorf("agent %s is offline at %s", a.Name, a.Address)
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Error(err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ui.ErrorResponse{Error: message})
}

// call calls the API of the agent at path, relative to its /api, with body as
// JSON when set, and decodes the answer into out when set, on behalf of the
// Telegram of the controller: its control panel must be enabled on it. An error
// answered is an *apiError.
func (d *Dashboard) call(ctx context.Context, a *agent, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	request, err := http.NewRequestWithContext(ctx, method, a.api.JoinPath(path).String(), reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set(ui.ChannelHeader, string(ui.ChannelTelegram))

	response, err := d.client.Do(request)
	if err != nil {
		log.Debugf("controller: agent %s: %v", a.Name, err)
		return offline(a.Agent)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("agent %s: %w", a.Name, err)
	}

	if response.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(data))
		var answer ui.ErrorResponse
		if json.Unmarshal(data, &answer) == nil && answer.Error != "" {
			message = answer.Error
		}
		return &apiError{Status: response.StatusCode, Message: message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("agent %s: invalid answer to %s %s: %w", a.Name, method, path, err)
	}
	return nil
}

// events follows the events of the agent until ctx is done or the stream ends,
// which returns nil: connected is called once the agent accepts it, then handle
// with the name and the data of each event.
func (d *Dashboard) events(ctx context.Context, a *agent, connected func(),
	handle func(name string, data []byte)) error {

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.api.JoinPath("events").String(), nil)
	if err != nil {
		return err
	}
	response, err := d.stream.Do(request)
	if err != nil {
		log.Debugf("controller: agent %s: %v", a.Name, err)
		return offline(a.Agent)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("agent %s: events: %s", a.Name, response.Status)
	}
	connected()

	scanner := bufio.NewScanner(response.Body)
	// a candle event carries the last point of every indicator
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var (
		name string
		data []byte
	)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: ")...)
		case line == "":
			if name != "" {
				handle(name, data)
			}
			name, data = "", nil
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		return fmt.Errorf("agent %s: events: %w", a.Name, err)
	}
	return nil
}
