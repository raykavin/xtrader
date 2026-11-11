package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/raykavin/xtrader/agentapi"
)

// The kinds of the events of the accounts of MT5, see MT5AccountEvent.
const (
	MT5Created     = "created"
	MT5Changed     = "changed"
	MT5Activated   = "activated"
	MT5Deactivated = "deactivated"
	MT5Deleted     = "deleted"
	MT5Imported    = "imported"
	MT5Linked      = "linked"
	MT5Unlinked    = "unlinked"
	MT5Tested      = "tested"
	MT5Session     = "session"
)

// The kinds of an account, as the bridge confirmed them (its trade_mode).
const (
	MT5KindDemo    = "demo"
	MT5KindReal    = "real"
	MT5KindContest = "contest"
)

// MT5Account is an account of MetaTrader 5 (mt5_accounts): its login, its
// server and its password, encrypted, sent to the terminal; the terminal
// serving it, through its bridge (BridgeURL, BridgeToken encrypted); whether
// the bridge may trade it real (AllowReal) or trade at all (ReadOnly), and
// whether it is the account of its terminal (Active); what the bridge
// confirmed of it (Kind, Currency, Company, CheckedAt, CheckError).
type MT5Account struct {
	ID          int64      `gorm:"primaryKey" json:"id"`
	Name        string     `json:"name"`
	Login       int64      `json:"login"`
	Server      string     `json:"server"`
	Password    []byte     `json:"-"` // encrypted
	BridgeURL   string     `gorm:"column:bridge_url" json:"bridge_url"`
	BridgeKey   string     `gorm:"column:bridge_key" json:"-"`
	BridgeToken []byte     `json:"-"` // encrypted
	AllowReal   bool       `json:"allow_real"`
	ReadOnly    bool       `json:"read_only"`
	Active      bool       `json:"active"`
	Kind        string     `json:"kind"`
	Currency    string     `json:"currency"`
	Company     string     `json:"company"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	CheckError  string     `json:"check_error"`
	UpdatedBy   string     `json:"updated_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (MT5Account) TableName() string { return "mt5_accounts" }

// recorded returns the account as its events record it: the password and the
// token only as set or not.
func (a MT5Account) recorded() map[string]any {
	return map[string]any{"name": a.Name, "login": a.Login, "server": a.Server, "bridge_url": a.BridgeURL,
		"allow_real": a.AllowReal, "read_only": a.ReadOnly, "active": a.Active,
		"password_set": len(a.Password) > 0, "token_set": len(a.BridgeToken) > 0}
}

// AgentMT5Account is the account of MT5 an agent trades (agent_mt5_accounts),
// the bridge and its token of it, see Agent.MT5Link.
type AgentMT5Account struct {
	AgentID   int64       `gorm:"primaryKey;autoIncrement:false" json:"-"`
	AccountID int64       `json:"account_id"`
	Account   *MT5Account `gorm:"foreignKey:AccountID" json:"-"`
	UpdatedBy string      `json:"updated_by"`
	UpdatedAt time.Time   `json:"updated_at"`
}

func (AgentMT5Account) TableName() string { return "agent_mt5_accounts" }

// MT5AccountEvent is an event of the accounts of MT5 (mt5_account_events),
// never holding a secret.
type MT5AccountEvent struct {
	ID          int64           `gorm:"primaryKey" json:"id"`
	At          time.Time       `json:"at"`
	AccountID   *int64          `json:"account_id,omitempty"`
	AgentID     *int64          `json:"agent_id,omitempty"`
	Kind        string          `json:"kind"`
	RequestedBy string          `json:"requested_by"`
	UserID      *int64          `json:"user_id,omitempty"`
	Reason      string          `json:"reason"`
	Previous    json.RawMessage `gorm:"type:jsonb" json:"previous,omitempty"`
	Next        json.RawMessage `gorm:"type:jsonb" json:"next,omitempty"`
}

func (MT5AccountEvent) TableName() string { return "mt5_account_events" }

// MT5Change is who changes the accounts of MT5 and why, for their events.
type MT5Change struct {
	By     string
	UserID *int64
	Reason string
}

// MT5AccountInput is an account of MT5 to create, or the change of one, of
// the fields set: the secrets as Secret (nil keeps, "" removes).
type MT5AccountInput struct {
	Name        *string
	Login       *int64
	Server      *string
	BridgeURL   *string
	AllowReal   *bool
	ReadOnly    *bool
	Password    Secret
	BridgeToken Secret
}

// The bounds of an account of MT5.
const (
	maxMT5Name     = 64
	maxMT5Server   = 128
	maxMT5Password = 256
)

// mt5PasswordOf and mt5TokenOf are the places of the password and of the token
// of the bridge of the account of MT5 of id.
func mt5PasswordOf(id int64) string { return placeOf("mt5_accounts", "password", id) }
func mt5TokenOf(id int64) string    { return placeOf("mt5_accounts", "bridge_token", id) }

// apply applies in to a, trimmed.
func (in MT5AccountInput) apply(a *MT5Account) {
	if in.Name != nil {
		a.Name = strings.TrimSpace(*in.Name)
	}
	if in.Login != nil {
		a.Login = *in.Login
	}
	if in.Server != nil {
		a.Server = strings.TrimSpace(*in.Server)
	}
	if in.BridgeURL != nil {
		a.BridgeURL = strings.TrimSpace(*in.BridgeURL)
		a.BridgeKey = agentapi.MT5BridgeKey(a.BridgeURL)
	}
	if in.AllowReal != nil {
		a.AllowReal = *in.AllowReal
	}
	if in.ReadOnly != nil {
		a.ReadOnly = *in.ReadOnly
	}
}

// validateMT5Account returns why a is refused, as the database checks it.
func validateMT5Account(a MT5Account, in MT5AccountInput) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: conta MT5: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	switch {
	case a.Name == "" || len([]rune(a.Name)) > maxMT5Name || !oneLine(a.Name):
		return invalid("o nome deve ter de 1 a %d caracteres, numa linha só", maxMT5Name)
	case a.Login <= 0:
		return invalid("o login deve ser um número maior que 0")
	case a.Server == "" || len(a.Server) > maxMT5Server || !oneLine(a.Server):
		return invalid("informe o servidor numa linha só, ex.: Exness-MT5Real11")
	case !httpURL(a.BridgeURL):
		return invalid("a bridge deve ser uma URL http(s) sem credenciais, ex.: http://mt5:18812")
	case in.Password != nil && len(*in.Password) > maxMT5Password:
		return invalid("a senha deve ter até %d caracteres", maxMT5Password)
	case in.BridgeToken != nil && !oneLine(*in.BridgeToken):
		return invalid("o token da bridge deve ficar numa linha só")
	}
	return nil
}

// addMT5Event records e.
func (s *Store) addMT5Event(ctx context.Context, e MT5AccountEvent) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return s.conn(ctx).Create(&e).Error
}

// MT5Accounts returns the accounts of MT5, by their names.
func (s *Store) MT5Accounts(ctx context.Context) ([]MT5Account, error) {
	listed, err := s.FindMT5Accounts(ctx, ListQuery{})
	return listed.Rows, err
}

// mt5AccountSearch is where a search of the accounts of MT5 looks: their
// names, logins, servers, bridges, kinds (demo, real, contest), currencies,
// companies and the agents trading them.
var mt5AccountSearch = []searchField{
	textField("mt5_accounts.name"),
	textField("CAST(mt5_accounts.login AS TEXT)"),
	textField("mt5_accounts.server"),
	textField("mt5_accounts.bridge_url"),
	textField("mt5_accounts.kind"),
	textField("mt5_accounts.currency"),
	textField("mt5_accounts.company"),
	relatedField(`EXISTS (SELECT 1 FROM agent_mt5_accounts l
		JOIN agents a ON a.id = l.agent_id AND a.deleted_at IS NULL
		WHERE l.account_id = mt5_accounts.id AND %s)`, "a.name"),
}

// FindMT5Accounts returns the accounts of MT5 of q, by their names.
func (s *Store) FindMT5Accounts(ctx context.Context, q ListQuery) (Listed[MT5Account], error) {
	db := s.conn(ctx).Model(&MT5Account{}).Scopes(searchScope(q.Search, mt5AccountSearch...))
	return list[MT5Account](db, q, byFields("lower(mt5_accounts.name)", "mt5_accounts.id"))
}

// MT5Account returns the account of MT5 of id, ErrNotFound without one.
func (s *Store) MT5Account(ctx context.Context, id int64) (MT5Account, error) {
	var account MT5Account
	err := s.conn(ctx).Take(&account, id).Error
	return account, notFound(err, fmt.Sprintf("a conta MT5 %d", id))
}

// MT5AccountSecrets returns the password and the token of the bridge of
// account, decrypted: "" for none. Only to send them to the bridge.
func (s *Store) MT5AccountSecrets(account MT5Account) (password, token string, err error) {
	if password, err = s.open(mt5PasswordOf(account.ID), account.Password); err != nil {
		return "", "", err
	}
	token, err = s.open(mt5TokenOf(account.ID), account.BridgeToken)
	return password, token, err
}

// sealMT5 seals the secrets of in into a, its ID set.
func (s *Store) sealMT5(a *MT5Account, in MT5AccountInput) error {
	if err := s.sealInto(&a.Password, mt5PasswordOf(a.ID), in.Password); err != nil {
		return err
	}
	return s.sealInto(&a.BridgeToken, mt5TokenOf(a.ID), in.BridgeToken)
}

// CreateMT5Account creates the account of MT5 of in, its name, login, server
// and bridge required, inactive unless active (then the account of its
// terminal, ErrConflict when another one is), recorded.
func (s *Store) CreateMT5Account(ctx context.Context, in MT5AccountInput, active bool, kind string,
	change MT5Change) (MT5Account, error) {
	var account MT5Account
	in.apply(&account)
	account.Active, account.UpdatedBy = active, change.By
	if kind == MT5KindDemo || kind == MT5KindReal || kind == MT5KindContest {
		account.Kind = kind
	}
	if err := validateMT5Account(account, in); err != nil {
		return MT5Account{}, err
	}
	event := MT5Created
	if change.By == ImportedBy {
		event = MT5Imported
	}
	err := s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.conn(ctx).Omit("Password", "BridgeToken").Create(&account).Error; err != nil {
			return notFound(err, "a conta MT5")
		}
		if err := s.sealMT5(&account, in); err != nil {
			return err
		}
		if err := s.conn(ctx).Model(&account).Updates(map[string]any{"password": account.Password,
			"bridge_token": account.BridgeToken}).Error; err != nil {
			return err
		}
		return s.addMT5Event(ctx, MT5AccountEvent{AccountID: &account.ID, Kind: event, RequestedBy: change.By,
			UserID: change.UserID, Reason: change.Reason, Next: jsonOf(account.recorded())})
	})
	return account, err
}

// ImportedBy is who the accounts imported from the bridges are created by.
const ImportedBy = "importação"

// UpdateMT5Account changes the account of MT5 of id as in, validated, the
// bridge of its agents with it, recorded, and returns it before and after.
func (s *Store) UpdateMT5Account(ctx context.Context, id int64, in MT5AccountInput,
	change MT5Change) (previous, next MT5Account, err error) {
	err = s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.conn(ctx).Clauses(lockForUpdate()).Take(&previous, id).Error; err != nil {
			return notFound(err, fmt.Sprintf("a conta MT5 %d", id))
		}
		next = previous
		in.apply(&next)
		if err := validateMT5Account(next, in); err != nil {
			return err
		}
		if err := s.sealMT5(&next, in); err != nil {
			return err
		}
		next.UpdatedBy, next.UpdatedAt = change.By, time.Now()
		if err := s.conn(ctx).Select("*").Save(&next).Error; err != nil {
			return notFound(err, "a conta MT5")
		}
		if next.BridgeURL != previous.BridgeURL {
			if err := s.syncBridgeOf(ctx, next); err != nil {
				return err
			}
		}
		return s.addMT5Event(ctx, MT5AccountEvent{AccountID: &id, Kind: MT5Changed, RequestedBy: change.By,
			UserID: change.UserID, Reason: change.Reason, Previous: jsonOf(previous.recorded()),
			Next: jsonOf(next.recorded())})
	})
	return previous, next, err
}

// syncBridgeOf sets the bridge of the agents of account to its own, the one
// every reader of the accounts of the agents takes (see Agent.MT5Book).
func (s *Store) syncBridgeOf(ctx context.Context, account MT5Account) error {
	return s.conn(ctx).Exec(`UPDATE agent_accounts SET bridge_url = ?, updated_at = now()
		WHERE agent_id IN (SELECT agent_id FROM agent_mt5_accounts WHERE account_id = ?)`,
		account.BridgeURL, account.ID).Error
}

// ActivateMT5Account makes the account of MT5 of id the one of its terminal
// (active), the one of it before, if any, deactivated; or deactivates it.
// Recorded; the accounts deactivated returned.
func (s *Store) ActivateMT5Account(ctx context.Context, id int64, active bool,
	change MT5Change) (deactivated []MT5Account, err error) {
	err = s.Transaction(ctx, func(ctx context.Context) error {
		var account MT5Account
		if err := s.conn(ctx).Clauses(lockForUpdate()).Take(&account, id).Error; err != nil {
			return notFound(err, fmt.Sprintf("a conta MT5 %d", id))
		}
		if account.Active == active {
			return nil
		}
		if active {
			if err := s.conn(ctx).Clauses(lockForUpdate()).Where("bridge_key = ? AND active AND id <> ?",
				account.BridgeKey, id).Find(&deactivated).Error; err != nil {
				return err
			}
			for _, other := range deactivated {
				if err := s.setActive(ctx, other, false, change); err != nil {
					return err
				}
			}
		}
		return s.setActive(ctx, account, active, change)
	})
	return deactivated, err
}

// setActive sets account active or not, recorded.
func (s *Store) setActive(ctx context.Context, account MT5Account, active bool, change MT5Change) error {
	if err := s.conn(ctx).Model(&MT5Account{}).Where("id = ?", account.ID).Updates(map[string]any{
		"active": active, "updated_by": change.By, "updated_at": time.Now()}).Error; err != nil {
		return notFound(err, "a conta MT5")
	}
	kind := MT5Deactivated
	if active {
		kind = MT5Activated
	}
	return s.addMT5Event(ctx, MT5AccountEvent{AccountID: &account.ID, Kind: kind, RequestedBy: change.By,
		UserID: change.UserID, Reason: change.Reason})
}

// DeleteMT5Account deletes the account of MT5 of id, inactive and without an
// agent, its events kept.
func (s *Store) DeleteMT5Account(ctx context.Context, id int64, change MT5Change) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		var account MT5Account
		if err := s.conn(ctx).Clauses(lockForUpdate()).Take(&account, id).Error; err != nil {
			return notFound(err, fmt.Sprintf("a conta MT5 %d", id))
		}
		if account.Active {
			return fmt.Errorf("%w: a conta MT5 %s está ativa no terminal: desative-a antes", ErrConflict,
				account.Name)
		}
		var agents int64
		if err := s.conn(ctx).Model(&AgentMT5Account{}).Where("account_id = ?", id).Count(&agents).Error; err != nil {
			return err
		}
		if agents > 0 {
			return fmt.Errorf("%w: a conta MT5 %s ainda tem agentes (%d): vincule-os a outra conta antes",
				ErrConflict, account.Name, agents)
		}
		if err := s.addMT5Event(ctx, MT5AccountEvent{AccountID: &id, Kind: MT5Deleted, RequestedBy: change.By,
			UserID: change.UserID, Reason: change.Reason, Previous: jsonOf(account.recorded())}); err != nil {
			return err
		}
		return s.conn(ctx).Delete(&MT5Account{}, id).Error
	})
}

// SetMT5AccountCheck records what the bridge confirmed of the account of MT5
// of id: its kind (of its trade_mode), currency and company, or why it
// couldn't (failure), at at; an event of it when tested by a user (change.By).
func (s *Store) SetMT5AccountCheck(ctx context.Context, id int64, kind, currency, company, failure string,
	at time.Time, change MT5Change) error {
	updates := map[string]any{"checked_at": at, "check_error": failure}
	if failure == "" {
		updates["kind"], updates["currency"], updates["company"] = kind, currency, company
	}
	return s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.conn(ctx).Model(&MT5Account{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		if change.By == "" {
			return nil
		}
		next := map[string]any{"kind": kind, "currency": currency, "company": company, "error": failure}
		return s.addMT5Event(ctx, MT5AccountEvent{AccountID: &id, Kind: MT5Tested, RequestedBy: change.By,
			UserID: change.UserID, Next: jsonOf(next)})
	})
}

// RecordMT5Session records the session of the account of MT5 of id given to
// its terminal, why and how it went.
func (s *Store) RecordMT5Session(ctx context.Context, id int64, reason, failure string) error {
	return s.addMT5Event(ctx, MT5AccountEvent{AccountID: &id, Kind: MT5Session, RequestedBy: "system",
		Reason: reason, Next: jsonOf(map[string]any{"error": failure})})
}

// MT5AccountEvents returns the last limit events of the account of MT5 of id,
// the newest first.
func (s *Store) MT5AccountEvents(ctx context.Context, id int64, limit int) ([]MT5AccountEvent, error) {
	var events []MT5AccountEvent
	err := s.conn(ctx).Where("account_id = ?", id).Order("id DESC").Limit(limit).Find(&events).Error
	return events, err
}

// MT5AccountAgents returns the names of the agents linked to each account of
// MT5, by its ID, the deleted ones left out.
func (s *Store) MT5AccountAgents(ctx context.Context) (map[int64][]string, error) {
	var rows []struct {
		AccountID int64
		Name      string
	}
	err := s.conn(ctx).Raw(`SELECT l.account_id, a.name FROM agent_mt5_accounts l
		JOIN agents a ON a.id = l.agent_id AND a.deleted_at IS NULL ORDER BY a.position, a.name`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	agents := make(map[int64][]string)
	for _, row := range rows {
		agents[row.AccountID] = append(agents[row.AccountID], row.Name)
	}
	return agents, nil
}

// SetAgentMT5Account links the agent of agentID to the account of MT5 of
// accountID, unlinks it with nil: its bridge then the one of the account (its
// account created when missing), recorded. The token of the bridge of the
// agent itself is left: unread while it is linked, see SecretsOf.
func (s *Store) SetAgentMT5Account(ctx context.Context, agentID int64, accountID *int64, change MT5Change) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.lockAgents(ctx); err != nil {
			return err
		}
		var current []AgentMT5Account
		if err := s.conn(ctx).Where("agent_id = ?", agentID).Find(&current).Error; err != nil {
			return err
		}
		if accountID == nil {
			if len(current) == 0 {
				return nil
			}
			if err := s.conn(ctx).Delete(&AgentMT5Account{}, agentID).Error; err != nil {
				return err
			}
			return s.addMT5Event(ctx, MT5AccountEvent{AccountID: &current[0].AccountID, AgentID: &agentID,
				Kind: MT5Unlinked, RequestedBy: change.By, UserID: change.UserID, Reason: change.Reason})
		}
		if len(current) > 0 && current[0].AccountID == *accountID {
			return nil
		}
		var account MT5Account
		if err := s.conn(ctx).Take(&account, *accountID).Error; err != nil {
			return notFound(err, fmt.Sprintf("a conta MT5 %d", *accountID))
		}
		link := AgentMT5Account{AgentID: agentID, AccountID: account.ID, UpdatedBy: change.By, UpdatedAt: time.Now()}
		if err := s.conn(ctx).Save(&link).Error; err != nil {
			return notFound(err, "o agente")
		}
		var accounts []Account
		if err := s.conn(ctx).Where("agent_id = ?", agentID).Find(&accounts).Error; err != nil {
			return err
		}
		if len(accounts) == 0 {
			if err := s.conn(ctx).Create(&Account{AgentID: agentID, Exchange: "mt5",
				BridgeURL: account.BridgeURL}).Error; err != nil {
				return notFound(err, "a conta do agente")
			}
		} else if err := s.conn(ctx).Model(&Account{}).Where("agent_id = ?", agentID).Updates(map[string]any{
			"bridge_url": account.BridgeURL, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		var previous any
		if len(current) > 0 {
			previous = map[string]any{"account_id": current[0].AccountID}
		}
		return s.addMT5Event(ctx, MT5AccountEvent{AccountID: &account.ID, AgentID: &agentID, Kind: MT5Linked,
			RequestedBy: change.By, UserID: change.UserID, Reason: change.Reason, Previous: jsonOf(previous)})
	})
}

// MT5AccountByLogin returns the account of MT5 of login on server, false
// without one.
func (s *Store) MT5AccountByLogin(ctx context.Context, login int64, server string) (MT5Account, bool, error) {
	var account MT5Account
	err := s.conn(ctx).Where("login = ? AND lower(server) = lower(?)", login, strings.TrimSpace(server)).
		Take(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return MT5Account{}, false, nil
	}
	return account, err == nil, err
}
