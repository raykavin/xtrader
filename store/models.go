package store

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Model is the ID and the timestamps of the records deleted softly: deleted,
// they stay in their table out of every query, their unique names free again.
type Model struct {
	ID        int64          `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"-"`
}

// User is a user of the dashboard.
type User struct {
	Model
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	Enabled      bool       `json:"enabled"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

// Prompt is a prompt of the LLM agents, the strategy the model trades.
type Prompt struct {
	Model
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Version     int    `json:"version"`
}

// PromptVersion is a version of a prompt, the content an edit left: kept for
// good, the decisions pointing at it (append-only, without DeletedAt).
type PromptVersion struct {
	PromptID  int64     `gorm:"primaryKey;autoIncrement:false" json:"prompt_id"`
	Version   int       `gorm:"primaryKey;autoIncrement:false" json:"version"`
	Content   string    `json:"content"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Agent is an agent of the controller, the bot of a binary of its Kind: its
// Params are its other settings by the name of their flag, only the ones set.
type Agent struct {
	Model
	Name            string       `json:"name"`
	Kind            string       `json:"kind"`
	Enabled         bool         `json:"enabled"`
	Mode            string       `json:"mode"`
	APIAddress      string       `gorm:"column:api_address" json:"api_address"`
	Launched        bool         `json:"launched"`
	Restart         bool         `json:"restart"`
	TradePanel      bool         `json:"trade_panel"`
	TelegramControl bool         `json:"telegram_control"`
	Params          Params       `gorm:"type:jsonb" json:"params"`
	Position        int          `json:"position"`
	OrderIDNext     int64        `gorm:"column:order_id_next" json:"-"`
	Account         *Account     `gorm:"foreignKey:AgentID" json:"account,omitempty"`
	LLM             *LLMSettings `gorm:"foreignKey:AgentID" json:"llm,omitempty"`
}

// Account is the trading account of an agent: the terminal of MT5 through its
// bridge, and what tells the orders of the agent apart there.
type Account struct {
	AgentID     int64     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Exchange    string    `json:"exchange"`
	BridgeURL   string    `gorm:"column:bridge_url" json:"bridge_url"`
	BridgeToken []byte    `json:"-"` // encrypted
	Magic       *int64    `json:"magic,omitempty"`
	Comment     *string   `json:"comment,omitempty"`
	RealAllowed bool      `json:"real_allowed"`
	CreatedAt   time.Time `json:"-"`
	UpdatedAt   time.Time `json:"-"`
}

func (Account) TableName() string { return "agent_accounts" }

// LLMSettings are the language model of an LLM agent: its Params are its
// other settings by the name of their flag, eg: llm-min-stop-atr.
type LLMSettings struct {
	AgentID         int64         `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Provider        string        `json:"provider"`
	Model           string        `json:"model"`
	APIKey          []byte        `gorm:"column:api_key" json:"-"` // encrypted
	BaseURL         *string       `gorm:"column:base_url" json:"base_url,omitempty"`
	ReasoningEffort *string       `json:"reasoning_effort,omitempty"`
	MaxTokens       *int          `json:"max_tokens,omitempty"`
	PromptID        *int64        `json:"prompt_id,omitempty"`
	PromptStatus    *PromptStatus `gorm:"type:jsonb" json:"prompt_status,omitempty"`
	CalendarURL     *string       `gorm:"column:calendar_url" json:"calendar_url,omitempty"`
	CalendarToken   []byte        `json:"-"` // encrypted
	Params          Params        `gorm:"type:jsonb" json:"params"`
	CreatedAt       time.Time     `json:"-"`
	UpdatedAt       time.Time     `json:"-"`
}

func (LLMSettings) TableName() string { return "agent_llm_settings" }

// TelegramSettings are the Telegram bot of the controller, a single record.
type TelegramSettings struct {
	ID        int16     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Enabled   bool      `json:"enabled"`
	Token     []byte    `json:"-"` // encrypted
	UpdatedAt time.Time `json:"updated_at"`
}

// TelegramUser is a Telegram user the bot answers.
type TelegramUser struct {
	Model
	TelegramID int64  `gorm:"column:telegram_id" json:"telegram_id"`
	Name       string `json:"name"`
}

// LegacyImport is a file of the previous version the migrator imported, or
// what it imported of its settings: an agent ("agent:<name>"), the user of the
// dashboard ("user:<username>") and Telegram ("env:TELEGRAM").
type LegacyImport struct {
	Source     string    `gorm:"primaryKey" json:"source"`
	Details    Params    `gorm:"type:jsonb" json:"details"`
	ImportedAt time.Time `gorm:"autoCreateTime" json:"imported_at"`
}

// Params are settings by name, a JSON object of strings.
type Params map[string]string

// Value writes the params as a JSON object, {} when nil.
func (p Params) Value() (driver.Value, error) {
	if p == nil {
		return "{}", nil
	}
	data, err := json.Marshal(map[string]string(p))
	return string(data), err
}

// Scan reads the params of a JSON object.
func (p *Params) Scan(src interface{}) error {
	var data []byte
	switch v := src.(type) {
	case nil:
		*p = Params{}
		return nil
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("store: params of %T", src)
	}
	params := Params{}
	if err := json.Unmarshal(data, &params); err != nil {
		return fmt.Errorf("store: params: %w", err)
	}
	*p = params
	return nil
}

// PromptStatus is the last prompt an agent was asked to switch to, and how it
// went: applied, or refused with Error, the previous one kept.
type PromptStatus struct {
	PromptID int64     `json:"prompt_id,omitempty"`
	Name     string    `json:"name"`
	Version  int       `json:"version"`
	Applied  bool      `json:"applied"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

// Value writes the status as JSON, NULL when nil.
func (s *PromptStatus) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	data, err := json.Marshal(s)
	return string(data), err
}

// Scan reads the status of its JSON.
func (s *PromptStatus) Scan(src interface{}) error {
	switch v := src.(type) {
	case nil:
		*s = PromptStatus{}
		return nil
	case []byte:
		return json.Unmarshal(v, s)
	case string:
		return json.Unmarshal([]byte(v), s)
	}
	return fmt.Errorf("store: prompt status of %T", src)
}
