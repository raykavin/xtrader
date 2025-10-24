package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	log "github.com/sirupsen/logrus"
	tb "gopkg.in/tucnak/telebot.v2"

	"github.com/raykavin/xtrader/store"
)

// PromptStore is the prompts of the database the bot serves, see
// store.Store.
type PromptStore interface {
	Prompts(ctx context.Context) ([]store.PromptSummary, error)
	PromptByName(ctx context.Context, name string) (store.Prompt, error)
	UpdatePrompt(ctx context.Context, id int64, edit store.PromptEdit, by string) (store.Prompt, error)
	AgentByName(ctx context.Context, name string) (store.Agent, error)
	SetAgentPrompt(ctx context.Context, id int64, promptID *int64) error
}

const (
	// telegramTextMax is the length of a message at most, below the 4096
	// characters of Telegram: a longer prompt is sent as a file.
	telegramTextMax = 4000
	// maxPromptFile is the size of a file of a prompt at most.
	maxPromptFile = 64 << 10
)

// by tells who changes a prompt from Telegram, for its versions.
func by(m *tb.Message) string {
	return fmt.Sprintf("telegram:%d", m.Sender.ID)
}

// promptError replies the error of a change of the prompts: the reason of the
// ones refused, the others logged.
func (t *Telegram) promptError(m *tb.Message, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrConflict):
		t.send(m.Sender, "⚠️ "+strings.TrimPrefix(err.Error(), "store: "))
	default:
		log.Errorf("telegram: the prompts: %v", err)
		t.send(m.Sender, "⚠️ The prompts can't be read or changed now, try again.")
	}
}

// sendLong sends a text of any length, split at its lines in messages
// Telegram takes.
func (t *Telegram) sendLong(to tb.Recipient, text string) {
	var chunk strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if chunk.Len()+len(line) > telegramTextMax && chunk.Len() > 0 {
			t.send(to, chunk.String())
			chunk.Reset()
		}
		chunk.WriteString(line)
	}
	if chunk.Len() > 0 {
		t.send(to, chunk.String())
	}
}

// handlePrompts lists the prompts, with the agents using each one.
func (t *Telegram) handlePrompts(m *tb.Message) {
	prompts, err := t.prompts.Prompts(t.ctx)
	if err != nil {
		t.promptError(m, err)
		return
	}
	lines := []string{"Prompts:"}
	for _, p := range prompts {
		line := fmt.Sprintf("• %s v%d", p.Name, p.Version)
		if len(p.UsedBy) > 0 {
			line += " — used by " + strings.Join(p.UsedBy, ", ")
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", "/prompt <name> shows one, /editprompt <name> edits it, "+
		"/useprompt <agent> <name> switches the one of an agent.")
	t.sendLong(m.Sender, strings.Join(lines, "\n"))
}

// handlePrompt shows a prompt, as a file when too long for a message.
func (t *Telegram) handlePrompt(m *tb.Message) {
	name := strings.TrimSpace(m.Payload)
	if name == "" {
		t.send(m.Sender, "Name the prompt, eg: /prompt trend_following (/prompts lists them).")
		return
	}
	prompt, err := t.prompts.PromptByName(t.ctx, name)
	if err != nil {
		t.promptError(m, err)
		return
	}
	title := fmt.Sprintf("%s v%d", prompt.Name, prompt.Version)
	if len(title)+2+len(prompt.Content) <= telegramTextMax {
		t.send(m.Sender, title+"\n\n"+prompt.Content)
		return
	}
	document := &tb.Document{File: tb.FromReader(strings.NewReader(prompt.Content)), FileName: prompt.Name + ".txt",
		Caption: title}
	if _, err := t.client.Send(m.Sender, document); err != nil {
		log.Error(t.redact(err))
	}
}

// handleEditPrompt edits a prompt: its new content on the lines after the
// command, or in the next message of the user, or a .txt file.
func (t *Telegram) handleEditPrompt(m *tb.Message) {
	first, content, _ := strings.Cut(m.Text, "\n")
	fields := strings.Fields(first)
	if len(fields) < 2 {
		t.send(m.Sender, "Name the prompt, eg: /editprompt trend_following (/prompts lists them).")
		return
	}
	prompt, err := t.prompts.PromptByName(t.ctx, fields[1])
	if err != nil {
		t.promptError(m, err)
		return
	}
	if strings.TrimSpace(content) != "" {
		t.savePrompt(m, prompt.Name, content)
		return
	}
	t.mu.Lock()
	t.editing[m.Sender.ID] = prompt.Name
	t.mu.Unlock()
	t.send(m.Sender, fmt.Sprintf("Send the new content of %s (now v%d) in your next message, or as a .txt file: "+
		"its whole text, the header lines included (/prompt %s shows it). /cancel gives up.", prompt.Name,
		prompt.Version, prompt.Name))
}

// editingOf returns the prompt the user sends the content of, done with it.
func (t *Telegram) editingOf(m *tb.Message) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	name, ok := t.editing[m.Sender.ID]
	delete(t.editing, m.Sender.ID)
	return name, ok
}

// handleText takes the new content of the prompt the user edits.
func (t *Telegram) handleText(m *tb.Message) {
	if name, ok := t.editingOf(m); ok {
		t.savePrompt(m, name, m.Text)
	}
}

// handleDocument takes the file of the new content of the prompt the user
// edits.
func (t *Telegram) handleDocument(m *tb.Message) {
	name, ok := t.editingOf(m)
	if !ok || m.Document == nil {
		return
	}
	if m.Document.FileSize > maxPromptFile {
		t.send(m.Sender, fmt.Sprintf("⚠️ The file takes %d bytes, %d at most: %s kept as it was.",
			m.Document.FileSize, maxPromptFile, name))
		return
	}
	reader, err := t.client.GetFile(&m.Document.File)
	if err != nil {
		log.Errorf("telegram: the file of a prompt: %v", t.redact(err)) // its URL carries the token
		t.send(m.Sender, "⚠️ The file can't be read, send it again: /editprompt "+name)
		return
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, maxPromptFile+1))
	if err != nil || len(data) > maxPromptFile {
		t.send(m.Sender, "⚠️ The file can't be read, send it again: /editprompt "+name)
		return
	}
	t.savePrompt(m, name, string(data))
}

// savePrompt saves content as the new version of the prompt of name.
func (t *Telegram) savePrompt(m *tb.Message, name, content string) {
	prompt, err := t.prompts.PromptByName(t.ctx, name)
	if err != nil {
		t.promptError(m, err)
		return
	}
	saved, err := t.prompts.UpdatePrompt(t.ctx, prompt.ID, store.PromptEdit{Content: &content}, by(m))
	if err != nil {
		t.promptError(m, err)
		return
	}
	log.Infof("telegram: the prompt %s edited by %d, version %d", saved.Name, m.Sender.ID, saved.Version)
	if t.onChange != nil {
		t.onChange()
	}
	if saved.Version == prompt.Version {
		t.send(m.Sender, fmt.Sprintf("%s unchanged: the same content (v%d).", saved.Name, saved.Version))
		return
	}
	t.send(m.Sender, fmt.Sprintf("✅ %s saved, v%d: the LLM agents using it switch to it on their next analysis.",
		saved.Name, saved.Version), t.menu)
}

// handleUsePrompt switches the prompt of an LLM agent, from its next
// analysis: default for its own.
func (t *Telegram) handleUsePrompt(m *tb.Message) {
	fields := strings.Fields(m.Payload)
	if len(fields) != 2 {
		t.send(m.Sender, "Name the agent and the prompt, eg: /useprompt llm trend_following, or default for "+
			"the one of the agent.")
		return
	}
	agent, err := t.prompts.AgentByName(t.ctx, fields[0])
	if err != nil {
		t.promptError(m, err)
		return
	}
	if agent.LLM == nil {
		t.send(m.Sender, fmt.Sprintf("⚠️ %s doesn't trade with a language model: it has no prompt.", agent.Name))
		return
	}
	var promptID *int64
	name := "its default prompt"
	if !strings.EqualFold(fields[1], "default") {
		prompt, err := t.prompts.PromptByName(t.ctx, fields[1])
		if err != nil {
			t.promptError(m, err)
			return
		}
		promptID, name = &prompt.ID, fmt.Sprintf("%s v%d", prompt.Name, prompt.Version)
	}
	if err := t.prompts.SetAgentPrompt(t.ctx, agent.ID, promptID); err != nil {
		t.promptError(m, err)
		return
	}
	log.Infof("telegram: the prompt of the agent %s switched by %d", agent.Name, m.Sender.ID)
	if t.onChange != nil {
		t.onChange()
	}
	t.send(m.Sender, fmt.Sprintf("✅ [%s] switches to %s on its next analysis; the analysis in progress, if any, "+
		"ends with the previous one. The dashboard tells whether it applied it.", agent.Name, name), t.menu)
}

// handleCancel gives up the edit of a prompt.
func (t *Telegram) handleCancel(m *tb.Message) {
	if name, ok := t.editingOf(m); ok {
		t.send(m.Sender, name+" kept as it was.", t.menu)
		return
	}
	t.send(m.Sender, "Nothing to cancel.", t.menu)
}
