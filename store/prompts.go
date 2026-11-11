package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm/clause"
)

// promptNameRegexp is the rule of the names of the prompts, the one of the
// table.
var promptNameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// maxPrompt is the length of a prompt at most, in bytes.
const maxPrompt = 64 << 10

// PromptSummary is a prompt without its content, with the agents using it.
type PromptSummary struct {
	Prompt
	UsedBy []string `gorm:"-" json:"used_by"`
}

// PromptEdit is an edit of a prompt, of the fields set: Version, when set, is
// the version the edit was made on, refused when it changed since (ErrConflict).
type PromptEdit struct {
	Description *string
	Content     *string
	Version     int
}

// checkPrompt checks the name and the content of a prompt.
func checkPrompt(name, content string) error {
	switch {
	case !promptNameRegexp.MatchString(name):
		return fmt.Errorf("%w: nome de prompt %q inválido: use letras minúsculas, números e _ . -, até 64 caracteres",
			ErrInvalid, name)
	case !utf8.ValidString(content):
		return fmt.Errorf("%w: o prompt %s não é texto UTF-8", ErrInvalid, name)
	case strings.TrimSpace(content) == "":
		return fmt.Errorf("%w: o prompt %s está vazio", ErrInvalid, name)
	case len(content) > maxPrompt:
		return fmt.Errorf("%w: o prompt %s tem %d bytes, o máximo é %d", ErrInvalid, name, len(content), maxPrompt)
	}
	return nil
}

// Prompts returns the prompts without their content, by name, with the agents
// using each one.
func (s *Store) Prompts(ctx context.Context) ([]PromptSummary, error) {
	listed, err := s.FindPrompts(ctx, ListQuery{})
	return listed.Rows, err
}

// promptSearch is where a search of the prompts looks: their names, their
// descriptions and the agents using them.
var promptSearch = []searchField{
	textField("prompts.name"),
	textField("prompts.description"),
	relatedField(`EXISTS (SELECT 1 FROM agent_llm_settings l
		JOIN agents a ON a.id = l.agent_id AND a.deleted_at IS NULL
		WHERE l.prompt_id = prompts.id AND %s)`, "a.name"),
}

// FindPrompts returns the prompts of q, without their content, by name, with
// the agents using each one.
func (s *Store) FindPrompts(ctx context.Context, q ListQuery) (Listed[PromptSummary], error) {
	db := s.conn(ctx).Model(&Prompt{}).
		Select("prompts.id, prompts.name, prompts.description, prompts.version, prompts.created_at, prompts.updated_at").
		Scopes(searchScope(q.Search, promptSearch...))
	listed, err := list[PromptSummary](db, q, byFields("prompts.name", "prompts.id"))
	if err != nil || len(listed.Rows) == 0 {
		return listed, err
	}
	ids := make([]int64, 0, len(listed.Rows))
	for _, p := range listed.Rows {
		ids = append(ids, p.ID)
	}
	var uses []struct {
		PromptID int64
		Name     string
	}
	err = s.conn(ctx).Table("agent_llm_settings AS l").Select("l.prompt_id, a.name").
		Joins("JOIN agents a ON a.id = l.agent_id AND a.deleted_at IS NULL").
		Where("l.prompt_id IN ?", ids).Order("a.name").Scan(&uses).Error
	if err != nil {
		return Listed[PromptSummary]{}, err
	}
	byID := make(map[int64][]string)
	for _, use := range uses {
		byID[use.PromptID] = append(byID[use.PromptID], use.Name)
	}
	for i := range listed.Rows {
		listed.Rows[i].UsedBy = append([]string{}, byID[listed.Rows[i].ID]...)
	}
	return listed, nil
}

// Prompt returns the prompt of id.
func (s *Store) Prompt(ctx context.Context, id int64) (Prompt, error) {
	var prompt Prompt
	err := s.conn(ctx).First(&prompt, id).Error
	return prompt, notFound(err, fmt.Sprintf("o prompt %d", id))
}

// PromptByName returns the prompt of name.
func (s *Store) PromptByName(ctx context.Context, name string) (Prompt, error) {
	var prompt Prompt
	err := s.conn(ctx).Where("name = ?", strings.TrimSpace(name)).First(&prompt).Error
	return prompt, notFound(err, "o prompt "+name)
}

// PromptVersion returns the version of the prompt of id.
func (s *Store) PromptVersion(ctx context.Context, id int64, version int) (PromptVersion, error) {
	var v PromptVersion
	err := s.conn(ctx).Where("prompt_id = ? AND version = ?", id, version).First(&v).Error
	return v, notFound(err, fmt.Sprintf("a versão %d do prompt %d", version, id))
}

// CreatePrompt creates a prompt, its first version by who, eg: web:xtrader.
func (s *Store) CreatePrompt(ctx context.Context, name, description, content, by string) (Prompt, error) {
	name = strings.TrimSpace(name)
	if err := checkPrompt(name, content); err != nil {
		return Prompt{}, err
	}
	prompt := Prompt{Name: name, Description: strings.TrimSpace(description), Content: content, Version: 1}
	err := s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.conn(ctx).Create(&prompt).Error; err != nil {
			return notFound(err, "o prompt "+name)
		}
		return s.conn(ctx).Create(&PromptVersion{PromptID: prompt.ID, Version: 1, Content: content,
			CreatedBy: by}).Error
	})
	return prompt, err
}

// UpdatePrompt edits the prompt of id by who: a new content is a new version.
func (s *Store) UpdatePrompt(ctx context.Context, id int64, edit PromptEdit, by string) (Prompt, error) {
	var prompt Prompt
	err := s.Transaction(ctx, func(ctx context.Context) error {
		err := s.conn(ctx).Clauses(lockForUpdate()).First(&prompt, id).Error
		if err != nil {
			return notFound(err, fmt.Sprintf("o prompt %d", id))
		}
		if edit.Version > 0 && edit.Version != prompt.Version {
			return fmt.Errorf("%w: o prompt %s foi alterado enquanto você editava e agora está na versão %d",
				ErrConflict, prompt.Name, prompt.Version)
		}
		columns := map[string]interface{}{}
		if edit.Description != nil {
			prompt.Description = strings.TrimSpace(*edit.Description)
			columns["description"] = prompt.Description
		}
		if edit.Content != nil && *edit.Content != prompt.Content {
			if err := checkPrompt(prompt.Name, *edit.Content); err != nil {
				return err
			}
			prompt.Content, prompt.Version = *edit.Content, prompt.Version+1
			columns["content"], columns["version"] = prompt.Content, prompt.Version
			if err := s.conn(ctx).Create(&PromptVersion{PromptID: prompt.ID, Version: prompt.Version,
				Content: prompt.Content, CreatedBy: by}).Error; err != nil {
				return err
			}
		}
		if len(columns) == 0 {
			return nil
		}
		return s.conn(ctx).Model(&prompt).Updates(columns).Error
	})
	if err != nil {
		return Prompt{}, err
	}
	return s.Prompt(ctx, id)
}

// DeletePrompt deletes the prompt of id, unless an agent uses it: locked
// first, so that an agent given it meanwhile (see sharePrompt) is one of
// them, and one given it after waits and fails.
func (s *Store) DeletePrompt(ctx context.Context, id int64) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		var prompt Prompt
		if err := s.conn(ctx).Clauses(lockForUpdate()).Select("id").First(&prompt, id).Error; err != nil {
			return notFound(err, fmt.Sprintf("o prompt %d", id))
		}
		var users []string
		err := s.conn(ctx).Table("agent_llm_settings AS l").
			Joins("JOIN agents a ON a.id = l.agent_id AND a.deleted_at IS NULL").
			Where("l.prompt_id = ?", id).Pluck("a.name", &users).Error
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("%w: o prompt é usado por %s", ErrConflict, strings.Join(users, ", "))
		}
		result := s.conn(ctx).Delete(&Prompt{}, id)
		if result.Error == nil && result.RowsAffected == 0 {
			return fmt.Errorf("%w: o prompt %d não existe", ErrNotFound, id)
		}
		return result.Error
	})
}

// sharePrompt fails with ErrNotFound unless the prompt of id exists, not
// deleted, and keeps it so until the end of the transaction, for an agent
// given it: DeletePrompt waits for it, then sees the agent using it; one
// deleting it before is waited for.
func (s *Store) sharePrompt(ctx context.Context, id int64) error {
	var prompt Prompt
	err := s.conn(ctx).Clauses(clause.Locking{Strength: "SHARE"}).Select("id").First(&prompt, id).Error
	return notFound(err, fmt.Sprintf("o prompt %d", id))
}

// lockForUpdate locks the rows read until the end of the transaction.
func lockForUpdate() clause.Expression {
	return clause.Locking{Strength: "UPDATE"}
}
