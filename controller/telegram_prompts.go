package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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
	Agents(ctx context.Context) ([]store.Agent, error)
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

// promptRefused reports whether err refuses a change of the prompts for good:
// not found, invalid, or conflicting. Another may pass, eg: the database out.
func promptRefused(err error) bool {
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrConflict)
}

// promptError replies the error of a change of the prompts: the reason of the
// ones refused, the others logged.
func (t *Telegram) promptError(m *tb.Message, err error) {
	if promptRefused(err) {
		t.send(m.Sender, "⚠️ "+userMessage(err))
		return
	}
	log.Errorf("telegram: the prompts: %v", err)
	t.send(m.Sender, "⚠️ The prompts can't be read or changed now, try again.")
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
	lines = append(lines, "", "/prompt <name> shows one, /editprompt <name> edits it (then /confirm), "+
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

// promptEditTTL is how long an edit of a prompt from Telegram waits for its
// content, then for its confirmation: given up after it, nothing saved.
const promptEditTTL = 10 * time.Minute

// promptEdit is the edit of a prompt a user makes: the content of the prompt
// awaited, then, once shown, its confirmation (see handleConfirm).
type promptEdit struct {
	name    string
	version int     // of the prompt when the edit started: refused once edited meanwhile
	content *string // the new content, awaiting its confirmation; nil, awaited
	expires time.Time
}

// handleEditPrompt edits a prompt: its new content on the lines after the
// command, or in the next message of the user, or a .txt file, shown and
// saved once the user confirms it (see handleConfirm), within promptEditTTL.
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
	if !t.promptControlled(m, prompt) {
		return
	}
	edit := promptEdit{name: prompt.Name, version: prompt.Version}
	if strings.TrimSpace(content) != "" {
		t.showEdit(m, edit, content)
		return
	}
	t.mu.Lock()
	edit.expires = t.now().Add(promptEditTTL)
	t.editing[m.Sender.ID] = edit
	t.mu.Unlock()
	t.send(m.Sender, fmt.Sprintf("Send the new content of %s (now v%d) in your next message, or as a .txt file, "+
		"within %s: its whole text, the header lines included (/prompt %s shows it); a text starting with / "+
		"isn't taken. It is shown before you /confirm it. /cancel gives up.", prompt.Name, prompt.Version,
		minutes(promptEditTTL), prompt.Name))
}

// minutes formats d in minutes, eg: 10 minutes.
func minutes(d time.Duration) string {
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

// promptControlled reports whether the Telegram controls of every agent
// enabled using prompt are on, telling which ones are off otherwise: the
// switch of the dashboard guards the strategy of an agent as its orders.
func (t *Telegram) promptControlled(m *tb.Message, prompt store.Prompt) bool {
	off, err := t.controlsOff(prompt)
	if err != nil {
		t.promptError(m, err)
		return false
	}
	return t.allControlled(m, prompt, off)
}

// controlsOff returns the agents enabled using prompt whose Telegram controls
// are off.
func (t *Telegram) controlsOff(prompt store.Prompt) ([]string, error) {
	agents, err := t.prompts.Agents(t.ctx)
	if err != nil {
		return nil, err
	}
	var off []string
	for _, a := range agents {
		if a.Enabled && a.LLM != nil && a.LLM.PromptID != nil && *a.LLM.PromptID == prompt.ID && !a.TelegramControl {
			off = append(off, a.Name)
		}
	}
	return off, nil
}

// allControlled reports whether off, the agents using prompt with their
// Telegram controls off, is empty, telling which ones are off otherwise.
func (t *Telegram) allControlled(m *tb.Message, prompt store.Prompt, off []string) bool {
	if len(off) == 0 {
		return true
	}
	t.send(m.Sender, fmt.Sprintf("⚠️ %s is used by agents with their Telegram controls off: %s. Edit it on the "+
		"dashboard, or turn their controls on in their settings.", prompt.Name, strings.Join(off, ", ")))
	return false
}

// editOf returns the edit of a prompt of the user, if any: one expired is
// given up, told to the user, and reported as none.
func (t *Telegram) editOf(m *tb.Message) (edit promptEdit, ok, expired bool) {
	t.mu.Lock()
	edit, ok = t.editing[m.Sender.ID]
	expired = ok && !t.now().Before(edit.expires)
	if expired {
		delete(t.editing, m.Sender.ID)
	}
	t.mu.Unlock()
	if expired {
		t.send(m.Sender, fmt.Sprintf("The edit of %s expired after %s: nothing saved, /editprompt %s starts "+
			"again.", edit.name, minutes(promptEditTTL), edit.name), t.menu)
	}
	return edit, ok && !expired, expired
}

// awaitingContent returns the edit of the user awaiting the content of its
// prompt, telling the user when it awaits its confirmation instead.
func (t *Telegram) awaitingContent(m *tb.Message) (promptEdit, bool) {
	edit, ok, _ := t.editOf(m)
	if ok && edit.content != nil {
		t.send(m.Sender, fmt.Sprintf("⚠️ The new content of %s awaits your confirmation: /confirm saves the one "+
			"shown, /cancel gives up.", edit.name))
		return edit, false
	}
	return edit, ok
}

// handleText takes the new content of the prompt the user edits: a text
// starting with /, eg: a command mistyped, never is.
func (t *Telegram) handleText(m *tb.Message) {
	edit, ok := t.awaitingContent(m)
	if !ok {
		return
	}
	if strings.HasPrefix(strings.TrimSpace(m.Text), "/") {
		t.send(m.Sender, fmt.Sprintf("⚠️ %s isn't a command of the bot, nor taken for the content of %s: send "+
			"its content, not starting with /, or /cancel to give up.", strings.Fields(m.Text)[0], edit.name))
		return
	}
	t.showEdit(m, edit, m.Text)
}

// handleDocument takes the file of the new content of the prompt the user
// edits.
func (t *Telegram) handleDocument(m *tb.Message) {
	if m.Document == nil {
		return
	}
	edit, ok := t.awaitingContent(m)
	if !ok {
		return
	}
	if m.Document.FileSize > maxPromptFile {
		t.send(m.Sender, fmt.Sprintf("⚠️ The file takes %d bytes, %d at most: send another one, or /cancel.",
			m.Document.FileSize, maxPromptFile))
		return
	}
	reader, err := t.client.GetFile(&m.Document.File)
	if err != nil {
		log.Errorf("telegram: the file of a prompt: %v", t.redact(err)) // its URL carries the token
		t.send(m.Sender, "⚠️ The file can't be read, send it again, or /cancel.")
		return
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, maxPromptFile+1))
	if err != nil || len(data) > maxPromptFile {
		t.send(m.Sender, "⚠️ The file can't be read, send it again, or /cancel.")
		return
	}
	t.showEdit(m, edit, string(data))
}

// showEdit shows content, the new content of the prompt of edit, and keeps it
// for its confirmation, see handleConfirm: an empty one is awaited again, the
// same as now given up.
func (t *Telegram) showEdit(m *tb.Message, edit promptEdit, content string) {
	if strings.TrimSpace(content) == "" {
		t.keepEdit(m, edit)
		t.send(m.Sender, fmt.Sprintf("⚠️ The content is empty: send the one of %s, or /cancel.", edit.name))
		return
	}
	prompt, err := t.prompts.PromptByName(t.ctx, edit.name)
	if err != nil {
		t.dropEdit(m)
		t.promptError(m, err)
		return
	}
	if prompt.Version != edit.version {
		t.dropEdit(m)
		t.send(m.Sender, fmt.Sprintf("⚠️ %s was edited meanwhile, v%d now: nothing saved, /editprompt %s starts "+
			"again.", prompt.Name, prompt.Version, prompt.Name), t.menu)
		return
	}
	if content == prompt.Content {
		t.dropEdit(m)
		t.send(m.Sender, fmt.Sprintf("%s unchanged: the same content (v%d).", prompt.Name, prompt.Version), t.menu)
		return
	}
	edit.content = &content
	t.keepEdit(m, edit)
	start := content
	if lines := strings.SplitAfter(start, "\n"); len(lines) > 3 {
		start = strings.Join(lines[:3], "")
	}
	if len(start) > 300 {
		start = strings.ToValidUTF8(start[:300], "") + "…"
	}
	var users string
	if summary, err := t.usedBy(prompt.ID); err == nil && len(summary) > 0 {
		users = " (" + strings.Join(summary, ", ") + ")"
	}
	t.send(m.Sender, fmt.Sprintf("New content of %s v%d: %d lines, %d bytes (now %d lines, %d bytes), starting "+
		"with:\n\n%s\n\n/confirm saves it as a new version: the LLM agents using it%s switch to it on their next "+
		"analysis. /cancel gives up; unconfirmed in %s, nothing is saved.", prompt.Name, edit.version,
		lineCount(content), len(content), lineCount(prompt.Content), len(prompt.Content), strings.TrimRight(start, "\n"),
		users, minutes(promptEditTTL)))
}

// lineCount counts the lines of text.
func lineCount(text string) int {
	return strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
}

// usedBy returns the names of the agents using the prompt of id.
func (t *Telegram) usedBy(id int64) ([]string, error) {
	prompts, err := t.prompts.Prompts(t.ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range prompts {
		if p.ID == id {
			return p.UsedBy, nil
		}
	}
	return nil, nil
}

// keepEdit keeps edit as the one of the user, for promptEditTTL from now.
func (t *Telegram) keepEdit(m *tb.Message, edit promptEdit) {
	t.mu.Lock()
	defer t.mu.Unlock()
	edit.expires = t.now().Add(promptEditTTL)
	t.editing[m.Sender.ID] = edit
}

// dropEdit gives up the edit of the user, returning it.
func (t *Telegram) dropEdit(m *tb.Message) (promptEdit, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	edit, ok := t.editing[m.Sender.ID]
	delete(t.editing, m.Sender.ID)
	return edit, ok
}

// restoreEdit gives back to the user the edit taken by handleConfirm, whose
// save failed for now, unless the user started another one meanwhile: its
// content isn't lost, /confirm tries again until it expires.
func (t *Telegram) restoreEdit(m *tb.Message, edit promptEdit) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.editing[m.Sender.ID]; !ok {
		t.editing[m.Sender.ID] = edit
	}
}

// handleConfirm saves the new content of the prompt the user edits, once
// shown: refused when the prompt was edited meanwhile, or the Telegram
// controls of an agent using it were turned off. The edit is taken while it
// is saved (a /confirm meanwhile finds none) and given up once saved or
// refused for good; a failure for now, eg: the database out, gives it back.
func (t *Telegram) handleConfirm(m *tb.Message) {
	edit, ok, expired := t.editOf(m)
	switch {
	case expired:
		return
	case !ok:
		t.send(m.Sender, "Nothing to confirm.", t.menu)
		return
	case edit.content == nil:
		t.send(m.Sender, fmt.Sprintf("Send the new content of %s first, or /cancel.", edit.name))
		return
	}
	t.dropEdit(m)
	failed := func(err error) bool {
		if err == nil || promptRefused(err) {
			return false
		}
		t.restoreEdit(m, edit)
		log.Errorf("telegram: the prompts: %v", err)
		t.send(m.Sender, fmt.Sprintf("⚠️ The prompts can't be read or changed now: nothing saved. The new content "+
			"of %s is kept: /confirm tries again, /cancel gives up.", edit.name))
		return true
	}
	prompt, err := t.prompts.PromptByName(t.ctx, edit.name)
	if failed(err) {
		return
	}
	if err != nil {
		t.promptError(m, err)
		return
	}
	off, err := t.controlsOff(prompt)
	if failed(err) || !t.allControlled(m, prompt, off) {
		return
	}
	saved, err := t.prompts.UpdatePrompt(t.ctx, prompt.ID, store.PromptEdit{Content: edit.content,
		Version: edit.version}, by(m))
	if errors.Is(err, store.ErrConflict) {
		// the version now, not the one read before the save
		now := ""
		if current, err := t.prompts.PromptByName(t.ctx, edit.name); err == nil {
			now = fmt.Sprintf(", v%d now", current.Version)
		}
		t.send(m.Sender, fmt.Sprintf("⚠️ %s was edited meanwhile%s: nothing saved, /editprompt %s starts again.",
			prompt.Name, now, prompt.Name), t.menu)
		return
	}
	if failed(err) {
		return
	}
	if err != nil {
		t.promptError(m, err)
		return
	}
	log.Infof("telegram: the prompt %s edited by %d, version %d", saved.Name, m.Sender.ID, saved.Version)
	if t.onChange != nil {
		t.onChange()
	}
	if saved.Version == prompt.Version {
		t.send(m.Sender, fmt.Sprintf("%s unchanged: the same content (v%d).", saved.Name, saved.Version), t.menu)
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
	if !agent.TelegramControl {
		t.send(m.Sender, fmt.Sprintf("⚠️ [%s] The Telegram controls of the agent are off: turn them on in the "+
			"settings of the dashboard.", agent.Name))
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
	if edit, ok := t.dropEdit(m); ok {
		t.send(m.Sender, edit.name+" kept as it was.", t.menu)
		return
	}
	t.send(m.Sender, "Nothing to cancel.", t.menu)
}
