package remote

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

const testToken = "xta_1_secret"

func init() {
	retryDelay, retryMaxDelay = 5*time.Millisecond, 20*time.Millisecond
	poisonAfter, poisonFor = 3, 50*time.Millisecond
	holdAfter, heldRetry, heldRetryMax = 200*time.Millisecond, 50*time.Millisecond, 200*time.Millisecond
}

// fakeController answers the internal API of a controller as the test sets
// it, recording what the agent sends.
type fakeController struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	down      bool // answers 503
	refuse    bool // answers 401
	cfg       agentapi.Config
	runtime   agentapi.Runtime
	changed   chan struct{} // closed as the runtime changes
	prompt    *agentapi.Prompt
	orders    []model.Order
	discards  []model.Discard     // nil: 404, as a controller of a version before
	leases    []agentapi.OrderIDs // answered in turn, the last one again
	leased    int
	decisions []ui.Decision
	links     map[int64]string
	memory    []agentapi.Remembered
	records   []agentapi.Record
	requests  []string
	// outboxAnswer answers POST /outbox and /outbox/check in place of the
	// controller when set and other than 200, nothing written: eg: 503
	outboxAnswer func(request agentapi.OutboxRequest) (int, any)
	outboxes     []agentapi.OutboxRequest // the requests of POST /outbox
	checks       []agentapi.OutboxRequest // the ones of POST /outbox/check
}

func newFakeController(t *testing.T) *fakeController {
	t.Helper()
	f := &fakeController{t: t, changed: make(chan struct{}), links: map[int64]string{},
		leases: []agentapi.OrderIDs{{First: 1, Last: 1000}}, discards: []model.Discard{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// client returns a client of the controller.
func (f *fakeController) client() *Client {
	f.t.Helper()
	c, err := NewClient(f.srv.URL, testToken)
	require.NoError(f.t, err)
	return c
}

// setRuntime changes the runtime, waking the requests waiting for it.
func (f *fakeController) setRuntime(runtime agentapi.Runtime) {
	f.mu.Lock()
	defer f.mu.Unlock()
	runtime.Version = f.runtime.Version + 1
	f.runtime = runtime
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *fakeController) set(apply func(f *fakeController)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	apply(f)
}

// written returns the records written.
func (f *fakeController) written() []agentapi.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentapi.Record(nil), f.records...)
}

func (f *fakeController) serve(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	down, refuse := f.down, f.refuse
	f.mu.Unlock()
	switch {
	case refuse || r.Header.Get("Authorization") != "Bearer "+testToken:
		reply(http.StatusUnauthorized, ui.ErrorResponse{Error: "invalid token"})
		return
	case down:
		reply(http.StatusServiceUnavailable, ui.ErrorResponse{Error: "down"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, agentapi.Prefix)
	query := r.URL.Query()

	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case path == "/config":
		reply(http.StatusOK, f.cfg)
	case path == "/runtime":
		if r.Header.Get("If-None-Match") == `"`+strconv.FormatInt(f.runtime.Version, 10)+`"` {
			changed := f.changed
			f.mu.Unlock()
			wait, _ := time.ParseDuration(query.Get("wait"))
			select {
			case <-changed:
			case <-time.After(min(wait, time.Second)):
				f.mu.Lock()
				w.WriteHeader(http.StatusNotModified)
				return
			case <-r.Context().Done():
				f.mu.Lock()
				return
			}
			f.mu.Lock()
		}
		reply(http.StatusOK, f.runtime)
	case path == "/prompt":
		if f.prompt == nil {
			reply(http.StatusNotFound, ui.ErrorResponse{Error: "default"})
			return
		}
		reply(http.StatusOK, f.prompt)
	case path == "/order-ids":
		lease := f.leases[min(f.leased, len(f.leases)-1)]
		f.leased++
		reply(http.StatusOK, lease)
	case path == "/orders":
		after, _ := strconv.ParseInt(query.Get("after"), 10, 64)
		limit, _ := strconv.Atoi(query.Get("limit"))
		var page []model.Order
		for _, order := range f.orders {
			if order.ID > after && len(page) < limit {
				page = append(page, order)
			}
		}
		reply(http.StatusOK, page)
	case path == "/discards" && f.discards != nil:
		reply(http.StatusOK, f.discards)
	case path == "/decisions":
		pair := query.Get("pair")
		limit, _ := strconv.Atoi(query.Get("limit"))
		if query.Get("per_pair") != "" {
			limit = len(f.decisions)
		}
		list := []ui.Decision{}
		for _, d := range f.decisions {
			if (pair == "" || d.Pair == pair) && len(list) < limit {
				list = append(list, d)
			}
		}
		reply(http.StatusOK, list)
	case strings.HasPrefix(path, "/decisions/") && strings.Contains(path, "/attachments/"):
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("from the controller"))
	case strings.HasPrefix(path, "/decisions/"):
		id := strings.TrimPrefix(path, "/decisions/")
		for _, d := range f.decisions {
			if d.ID == id {
				reply(http.StatusOK, d)
				return
			}
		}
		reply(http.StatusNotFound, ui.ErrorResponse{Error: "not found"})
	case path == "/decision-links":
		links := []agentapi.DecisionLink{}
		for order, id := range f.links {
			links = append(links, agentapi.DecisionLink{OrderID: order, DecisionID: id})
		}
		reply(http.StatusOK, links)
	case path == "/journal/memory":
		reply(http.StatusOK, f.memory)
	case path == "/outbox", path == "/outbox/check":
		var request agentapi.OutboxRequest
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&request))
		if path == "/outbox" {
			f.outboxes = append(f.outboxes, request)
		} else {
			f.checks = append(f.checks, request)
		}
		if f.outboxAnswer != nil {
			if status, body := f.outboxAnswer(request); status != http.StatusOK {
				reply(status, body)
				return
			}
		}
		if path == "/outbox" {
			f.records = append(f.records, request.Records...)
		}
		reply(http.StatusOK, agentapi.OutboxResponse{Acked: request.Records[len(request.Records)-1].Seq})
	default:
		reply(http.StatusNotFound, ui.ErrorResponse{Error: "no route " + path})
	}
}
