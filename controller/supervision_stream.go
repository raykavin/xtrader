package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// streamKeepAlive is how often the stream of an answer writes a comment while
// nothing else comes, eg: the model thinking: a proxy on the way, as the
// tunnel of the dashboard, keeps an idle connection open that long only.
var streamKeepAlive = 15 * time.Second

// answerStream writes the events of an answer of the conversation to the
// browser as server-sent events, see ui.SupervisorEventMessage: the messages
// kept, the stages, the pieces of the text. Writes come from the answer and
// from the keep-alive, one at a time; once one fails (the browser gone), the
// next ones are skipped and the answer goes on, kept for the conversation.
type answerStream struct {
	ctx    context.Context
	s      *Supervision
	w      http.ResponseWriter
	rc     *http.ResponseController
	mu     sync.Mutex
	broken bool
}

// send writes the event name with the JSON of data.
func (a *answerStream) send(name string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		log.Errorf("supervisor: the event %s of an answer: %v", name, err)
		return
	}
	a.write(fmt.Sprintf("event: %s\ndata: %s\n\n", name, payload))
}

// write writes chunk and flushes it, unless a write failed before.
func (a *answerStream) write(chunk string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.broken {
		return
	}
	if _, err := fmt.Fprint(a.w, chunk); err != nil {
		a.broken = true
		return
	}
	if err := a.rc.Flush(); err != nil {
		a.broken = true
	}
}

func (a *answerStream) message(m store.SupervisorMessage) {
	a.send(ui.SupervisorEventMessage, a.s.messageInfo(a.ctx, m))
}

func (a *answerStream) stage(stage, tool string) {
	a.send(ui.SupervisorEventStage, ui.SupervisorStreamStage{Stage: stage, Tool: tool})
}

func (a *answerStream) text(piece string) {
	a.send(ui.SupervisorEventText, ui.SupervisorStreamText{Text: piece})
}

// handleAskStream answers a message of the user as handleAsk does, streaming
// the answer as it goes: server-sent events (see ui.SupervisorEventMessage),
// "done" last with the messages added. A request refused (an empty message,
// no user) is answered with JSON before the stream; a failure once it began
// is the event "error". The answer is kept whatever the browser does.
func (s *Supervision) handleAskStream(w http.ResponseWriter, r *http.Request) {
	var request ui.SupervisorMessageRequest
	if !decode(w, r, &request) {
		return
	}
	userID := userIDOf(r.Context())
	if userID == 0 {
		writeJSONError(w, http.StatusUnauthorized, "Sessão expirada: entre de novo.")
		return
	}
	if _, err := checkMessage(request.Text); err != nil {
		s.fail(w, err)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	stream := &answerStream{ctx: ctx, s: s, w: w, rc: http.NewResponseController(w)}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	stream.write(": connected\n\n")

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(streamKeepAlive)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				stream.write(": ping\n\n")
			}
		}
	}()

	by := userOf(r.Context())
	added, err := s.AskFollowed(ctx, userID, by, request.Text, stream)
	if err != nil {
		log.Errorf("supervisor: the conversation of %s: %v", by, err)
		if len(added) == 0 {
			stream.send(ui.SupervisorEventError, ui.SupervisorStreamError{Error: supervisorDown})
			return
		}
	}
	stream.send(ui.SupervisorEventDone, s.messagesResponse(ctx, added, false))
}
