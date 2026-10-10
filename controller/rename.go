package controller

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/tools/log"
)

// outboxDir is the directory of the outboxes of the agents in their working
// directory, as agents/internal/remote opens it: each one a file named after
// its agent and its ID, eg: llm-1.db.
var outboxDir = filepath.Join("data", "outbox")

// RenameOutbox renames the outbox of the agent of id, stopped, from its name
// from to its name to, with the records it holds not sent yet: the agent
// reads it under its new name. None to rename is no error. undo puts it back,
// eg: the rename of the agent failing after it.
func (m *Manager) RenameOutbox(id int64, from, to string) (undo func(), err error) {
	dir := filepath.Join(m.agents.Dir, outboxDir)
	old := filepath.Join(dir, fmt.Sprintf("%s-%d.db", from, id))
	renamed := filepath.Join(dir, fmt.Sprintf("%s-%d.db", to, id))
	undo = func() {}
	switch _, err := os.Stat(old); {
	case errors.Is(err, fs.ErrNotExist):
		return undo, nil
	case err != nil:
		return undo, fmt.Errorf("the outbox of the agent %s: %w", from, err)
	}
	switch _, err := os.Stat(renamed); {
	case err == nil:
		return undo, fmt.Errorf("%w: the outbox %s exists already", store.ErrConflict, renamed)
	case !errors.Is(err, fs.ErrNotExist):
		return undo, fmt.Errorf("the outbox of the agent %s: %w", to, err)
	}
	if err := os.Rename(old, renamed); err != nil {
		return undo, fmt.Errorf("the outbox of the agent %s: %w", from, err)
	}
	_ = os.Remove(old + ".lock") // the lock of the agent stopped, made again by it
	return func() {
		if err := os.Rename(renamed, old); err != nil {
			log.Errorf("controller: the outbox %s not put back: %v", renamed, err)
		}
	}, nil
}
