package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/ui"
)

// agentNameRegexp is the rule of the names of the agents, the one of the table
// and of the routes of the dashboard.
var agentNameRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// paramNameRegexp is the rule of the names of the params, the ones of their
// flags, eg: llm-min-stop-atr.
var paramNameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// maxParamValue is the length of the value of a param at most, in bytes.
const maxParamValue = 1024

// maxComment is the length of the comment of the orders of an agent at most,
// the longest MT5 keeps, see exchange.WithMT5Comment.
const maxComment = 31

// providers and reasoningEfforts are the providers of the language models and
// their reasoning efforts, the ones of the table.
var (
	providers        = []string{"openai", "anthropic"}
	reasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh"}
)

// llmParamPrefixes are the prefixes of the params of the language model of an
// LLM agent, kept with it (see LLMSettings), as cmd/migrator splits them.
var llmParamPrefixes = []string{"llm-", "calendar-", "news-"}

// columnFlags are the flags the fields of an agent, of its account and of its
// language model set, and the ones of the files the database replaces: never
// params, which would override them on the command line of the agent (see
// remote.Agent.Args), as cmd/migrator leaves them out.
var columnFlags = map[string]bool{"mode": true, "exchange": true, "api": true, "trade-panel": true,
	"telegram-control": true, "reanalysis": true, "mt5-bridge": true, "mt5-token": true, "mt5-magic": true,
	"mt5-comment": true, "mt5-live": true, "llm-provider": true, "llm-model": true, "llm-base-url": true,
	"llm-reasoning": true, "llm-max-tokens": true, "prompt-path": true, "calendar-mcp": true, "db": true,
	"llm-decisions": true, "llm-journal": true}

// ColumnFlag reports whether the flag name of an agent is one of its fields,
// of its account or of its language model, or of a file the database
// replaces, see columnFlags: never a param, eg: of an import (cmd/migrator).
func ColumnFlag(name string) bool {
	return columnFlags[name]
}

// LLMParam reports whether the flag name is one of the language model of an
// LLM agent, kept with it rather than with the params of the agent, see
// llmParamPrefixes.
func LLMParam(name string) bool {
	return llmParam(name)
}

// AgentSecrets are the secrets of an agent to write, of the ones set, see
// Secret: the key of its language model and the token of the bridge of its
// account. The token of the MCP server of the calendar is the one of the news
// of all the agents, see NewsSettings.
type AgentSecrets struct {
	LLMAPIKey   Secret
	BridgeToken Secret
}

// AgentSecretValues are the secrets of an agent, decrypted: "" for none.
type AgentSecretValues struct {
	LLMAPIKey   string
	BridgeToken string
}

// AgentUpdate is a change of an agent, of the fields set: its name and its
// kind never change, the name naming its outbox and its routes on the
// dashboard. Reanalysis switches the reanalysis of an LLM agent (see
// AgentReanalysis): on refused for another kind, off nothing for it.
// Params, when not nil, replace the params of the agent and of its
// language model, merged (see Agent.AllParams). Account and LLM change its
// account and its language model, created when it has none.
type AgentUpdate struct {
	Enabled         *bool
	TradePanel      *bool
	TelegramControl *bool
	Reanalysis      *bool
	Mode            *string
	APIAddress      *string
	Launched        *bool
	Restart         *bool
	Params          Params
	Account         *AccountUpdate
	LLM             *LLMUpdate
}

// AccountUpdate is a change of the account of an agent, of the fields set: a
// Magic of 0 and an empty Comment remove them, their defaults applying (a
// hash of the comment, and the comment of the kind of the agent).
type AccountUpdate struct {
	BridgeURL   *string
	Magic       *int64
	Comment     *string
	RealAllowed *bool
}

// LLMUpdate is a change of the language model of an LLM agent, of the fields
// set: an empty BaseURL or ReasoningEffort and a MaxTokens of 0
// remove them, the defaults of the agent applying.
type LLMUpdate struct {
	Provider        *string
	Model           *string
	BaseURL         *string
	ReasoningEffort *string
	MaxTokens       *int
}

// switchesOnly reports whether the update changes the switches of the agent
// only, which apply while it runs.
func (u AgentUpdate) switchesOnly() bool {
	return u.Mode == nil && u.APIAddress == nil && u.Launched == nil && u.Restart == nil && u.Params == nil &&
		u.Account == nil && u.LLM == nil
}

// touchesBook reports whether the update may make the agent trade on MT5 (see
// Agent.trades) or change what it trades there (see Agent.MT5Book).
func (u AgentUpdate) touchesBook() bool {
	return u.Enabled != nil || u.Mode != nil || u.Params != nil || u.Account != nil
}

// agentChange is an update applied to an agent: the agent changed, and the
// columns to write of each of its tables, nil for none.
type agentChange struct {
	agent        Agent
	columns      map[string]interface{} // of agents
	account, llm map[string]interface{} // of agent_accounts and agent_llm_settings
	reanalysis   *AgentReanalysis       // of agent_reanalysis, of an LLM agent
}

// apply returns the change of the agent a the update makes.
func (u AgentUpdate) apply(a Agent) agentChange {
	c := agentChange{agent: normalized(a), columns: map[string]interface{}{}}
	agent := &c.agent
	flag := func(column string, field, value *bool) {
		if value != nil {
			*field = *value
			c.columns[column] = *value
		}
	}
	flag("enabled", &agent.Enabled, u.Enabled)
	flag("trade_panel", &agent.TradePanel, u.TradePanel)
	flag("telegram_control", &agent.TelegramControl, u.TelegramControl)
	flag("launched", &agent.Launched, u.Launched)
	flag("restart", &agent.Restart, u.Restart)
	if agent.Kind != "llm" {
		agent.Reanalysis = nil // of an LLM agent only, a row of another kind never written so
	}
	if u.Reanalysis != nil {
		agent.Reanalysis = &AgentReanalysis{AgentID: agent.ID, Allowed: *u.Reanalysis}
		if agent.Kind == "llm" {
			c.reanalysis = agent.Reanalysis
		}
	}
	if u.Mode != nil {
		agent.Mode = strings.TrimSpace(*u.Mode)
		c.columns["mode"] = agent.Mode
	}
	if u.APIAddress != nil {
		agent.APIAddress = strings.TrimSpace(*u.APIAddress)
		c.columns["api_address"] = agent.APIAddress
	}
	if u.Account != nil {
		c.account = u.Account.apply(agent)
	}
	if u.LLM != nil {
		c.llm = u.LLM.apply(agent)
	}
	if u.Params != nil {
		agent.setParams(u.Params)
		c.columns["params"] = agent.Params
		if agent.LLM != nil {
			if c.llm == nil {
				c.llm = map[string]interface{}{}
			}
			c.llm["params"] = agent.LLM.Params
		}
	}
	return c
}

// apply applies the update to the account of the agent a, created when it
// has none, and returns the columns it changes.
func (u AccountUpdate) apply(a *Agent) map[string]interface{} {
	account := Account{AgentID: a.ID, Exchange: "mt5"}
	if a.Account != nil {
		account = *a.Account
	}
	columns := map[string]interface{}{}
	if u.BridgeURL != nil {
		account.BridgeURL = strings.TrimSpace(*u.BridgeURL)
		columns["bridge_url"] = account.BridgeURL
	}
	if u.Magic != nil {
		account.Magic = nonZero(u.Magic)
		columns["magic"] = nullable(account.Magic)
	}
	if u.Comment != nil {
		account.Comment = optional(u.Comment)
		columns["comment"] = nullable(account.Comment)
	}
	if u.RealAllowed != nil {
		account.RealAllowed = *u.RealAllowed
		columns["real_allowed"] = account.RealAllowed
	}
	a.Account = &account
	return columns
}

// apply applies the update to the language model of the agent a, created
// when it has none, and returns the columns it changes.
func (u LLMUpdate) apply(a *Agent) map[string]interface{} {
	llm := LLMSettings{AgentID: a.ID, Params: Params{}}
	if a.LLM != nil {
		llm = *a.LLM
	}
	columns := map[string]interface{}{}
	if u.Provider != nil {
		llm.Provider = strings.TrimSpace(*u.Provider)
		columns["provider"] = llm.Provider
	}
	if u.Model != nil {
		llm.Model = strings.TrimSpace(*u.Model)
		columns["model"] = llm.Model
	}
	if u.BaseURL != nil {
		llm.BaseURL = optional(u.BaseURL)
		columns["base_url"] = nullable(llm.BaseURL)
	}
	if u.ReasoningEffort != nil {
		llm.ReasoningEffort = optional(u.ReasoningEffort)
		columns["reasoning_effort"] = nullable(llm.ReasoningEffort)
	}
	if u.MaxTokens != nil {
		llm.MaxTokens = nonZero(u.MaxTokens)
		columns["max_tokens"] = nullable(llm.MaxTokens)
	}
	a.LLM = &llm
	return columns
}

// AllParams returns the params of the agent and of its language model,
// merged: the flags of its settings that no field holds, by name.
func (a Agent) AllParams() Params {
	params := Params{}
	maps.Copy(params, a.Params)
	if a.LLM != nil {
		maps.Copy(params, a.LLM.Params)
	}
	return params
}

// ReanalysisAllowed reports whether the language model of the agent a may be
// asked again between its closes, see AgentReanalysis: of an LLM agent only.
func (a Agent) ReanalysisAllowed() bool {
	return a.Kind == "llm" && a.Reanalysis != nil && a.Reanalysis.Allowed
}

// trades reports whether the agent a trades its account on MT5: enabled in
// live mode, the paper mode reading the market data only (see
// exchange.WithMT5DataOnly), whether the controller launches it or it runs
// apart (Launched false), which trades all the same.
func (a Agent) trades() bool {
	return a.Enabled && a.Mode == "live"
}

// MT5Book returns what the agent a trades on MT5, as it computes it from its
// settings: its bridge, its magic number and its pairs, see
// agentapi.NewMT5Book.
func (a Agent) MT5Book() agentapi.MT5Book {
	var account *agentapi.Account
	if acc := a.Account; acc != nil {
		account = &agentapi.Account{BridgeURL: acc.BridgeURL, Magic: acc.Magic, Comment: acc.Comment}
	}
	return agentapi.NewMT5Book(a.Kind, a.AllParams(), account)
}

// setParams sets the params of the agent a, merged: the ones of its language
// model kept with it (see llmParam) when it has one, else left with the
// others, for checkParams to refuse them. Its language model is a copy.
func (a *Agent) setParams(params Params) {
	a.Params = Params{}
	if a.LLM == nil {
		maps.Copy(a.Params, params)
		return
	}
	a.LLM.Params = Params{}
	for name, value := range params {
		if llmParam(name) {
			a.LLM.Params[name] = value
		} else {
			a.Params[name] = value
		}
	}
}

// llmParam reports whether the param of name is one of the language model of
// an LLM agent, eg: llm-min-stop-atr or calendar-importance.
func llmParam(name string) bool {
	return slices.ContainsFunc(llmParamPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

// normalized returns a copy of the agent a, its account and its language model
// copied too, their text trimmed and their optional fields set to "" or 0
// removed, see AccountUpdate and LLMUpdate.
func normalized(a Agent) Agent {
	a.Name, a.Mode = strings.TrimSpace(a.Name), strings.TrimSpace(a.Mode)
	a.APIAddress = strings.TrimSpace(a.APIAddress)
	if a.Account != nil {
		account := *a.Account
		account.BridgeURL = strings.TrimSpace(account.BridgeURL)
		account.Magic, account.Comment = nonZero(account.Magic), optional(account.Comment)
		if account.Exchange == "" {
			account.Exchange = "mt5"
		}
		a.Account = &account
	}
	if a.LLM != nil {
		llm := *a.LLM
		llm.Provider, llm.Model = strings.TrimSpace(llm.Provider), strings.TrimSpace(llm.Model)
		llm.BaseURL, llm.ReasoningEffort = optional(llm.BaseURL), optional(llm.ReasoningEffort)
		llm.MaxTokens = nonZero(llm.MaxTokens)
		a.LLM = &llm
	}
	if a.Reanalysis != nil {
		reanalysis := *a.Reanalysis
		a.Reanalysis = &reanalysis
	}
	return a
}

// optional returns text trimmed, nil for none or "".
func optional(text *string) *string {
	if text == nil {
		return nil
	}
	value := strings.TrimSpace(*text)
	if value == "" {
		return nil
	}
	return &value
}

// nonZero returns a copy of number, nil for none or 0.
func nonZero[T int | int64](number *T) *T {
	if number == nil || *number == 0 {
		return nil
	}
	value := *number
	return &value
}

// nullable returns the value of p for a column, nil (NULL) for none.
func nullable[T any](p *T) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// checkAgent checks an agent with its account and its language model, as
// CreateAgent and UpdateAgent take it, normalized: that no other agent has the
// address of its API is checked apart, see checkAddressFree.
func checkAgent(a Agent) error {
	switch {
	case !agentNameRegexp.MatchString(a.Name):
		return fmt.Errorf("%w: nome de agente %q inválido: use letras, números e . _ -, até 64 caracteres", ErrInvalid,
			a.Name)
	case !slices.Contains(config.Kinds, a.Kind):
		return fmt.Errorf("%w: o tipo %q do agente %s não existe, use um destes: %s", ErrInvalid, a.Kind, a.Name,
			strings.Join(config.Kinds, ", "))
	case a.Mode != "live" && a.Mode != "paper":
		return fmt.Errorf("%w: o modo %q do agente %s não existe, use live ou paper", ErrInvalid, a.Mode, a.Name)
	case a.APIAddress == "":
		return fmt.Errorf("%w: informe o endereço da API do agente %s", ErrInvalid, a.Name)
	case !validAddress(a.APIAddress):
		return fmt.Errorf("%w: o endereço %q do agente %s deve ser host:porta, com a porta de 1 a 65535 e o host "+
			"no loopback (localhost, 127.0.0.0/8 ou [::1]): o controller só acessa a API dos agentes na própria "+
			"máquina, sem autenticação", ErrInvalid, a.APIAddress, a.Name)
	case a.LLM != nil && a.Kind != "llm":
		return fmt.Errorf("%w: o agente %s é do tipo %s, que não usa LLM: só os do tipo llm usam", ErrInvalid, a.Name,
			a.Kind)
	case a.Kind == "llm" && a.Launched && a.LLM == nil:
		return fmt.Errorf("%w: configure o LLM do agente %s: um agente LLM precisa dele", ErrInvalid, a.Name)
	}
	if err := checkReanalysis(a); err != nil {
		return err
	}
	if err := checkAccount(a.Name, a.Account); err != nil {
		return err
	}
	if err := checkLLM(a.Name, a.LLM); err != nil {
		return err
	}
	return checkParams(a)
}

// checkReanalysis checks the switch of the reanalysis of the agent a, see
// AgentReanalysis: on for an LLM agent only, the language model of another
// kind there is none to ask.
func checkReanalysis(a Agent) error {
	if a.Reanalysis != nil && a.Reanalysis.Allowed && a.Kind != "llm" {
		return fmt.Errorf("%w: o agente %s é do tipo %s e não tem LLM: a reanálise, que pergunta de novo ao LLM, "+
			"é só para agentes LLM", ErrInvalid, a.Name, a.Kind)
	}
	return nil
}

// checkAccount checks the account of the agent name, if any: the URL of its
// bridge, and the magic number and the comment of its orders, as the exchange
// takes them (see exchange.WithMT5Magic and exchange.WithMT5Comment).
func checkAccount(name string, account *Account) error {
	if account == nil {
		return nil
	}
	switch {
	case account.BridgeURL == "":
		return fmt.Errorf("%w: informe a URL da bridge do agente %s", ErrInvalid, name)
	case !httpURL(account.BridgeURL):
		return fmt.Errorf("%w: a URL da bridge do agente %s deve ser http(s), sem credenciais", ErrInvalid, name)
	case account.Magic != nil && *account.Magic < 1:
		return fmt.Errorf("%w: o magic number %d do agente %s deve ser maior que 0", ErrInvalid, *account.Magic, name)
	case account.Comment != nil && !validComment(*account.Comment):
		return fmt.Errorf("%w: o comentário %q do agente %s deve ter de 1 a %d caracteres ASCII imprimíveis, sem "+
			"começar com [", ErrInvalid, *account.Comment, name, maxComment)
	}
	return nil
}

// checkLLM checks the language model of the agent name, if any.
func checkLLM(name string, llm *LLMSettings) error {
	if llm == nil {
		return nil
	}
	switch {
	case !slices.Contains(providers, llm.Provider):
		return fmt.Errorf("%w: o provedor %q do agente %s não existe, use openai ou anthropic", ErrInvalid, llm.Provider,
			name)
	case llm.Model == "":
		return fmt.Errorf("%w: informe o modelo do LLM do agente %s, ex.: gpt-5", ErrInvalid, name)
	case !oneLine(llm.Model):
		return fmt.Errorf("%w: o modelo do LLM do agente %s deve ficar numa linha só", ErrInvalid, name)
	case llm.BaseURL != nil && !httpURL(*llm.BaseURL):
		return fmt.Errorf("%w: a URL base do LLM do agente %s deve ser http(s), sem credenciais", ErrInvalid, name)
	case llm.ReasoningEffort != nil && !slices.Contains(reasoningEfforts, *llm.ReasoningEffort):
		return fmt.Errorf("%w: o esforço de raciocínio %q do agente %s não existe, use minimal, low, medium ou high",
			ErrInvalid, *llm.ReasoningEffort, name)
	case llm.MaxTokens != nil && (*llm.MaxTokens < 1 || *llm.MaxTokens > math.MaxInt32):
		return fmt.Errorf("%w: o máximo de tokens %d do agente %s deve ficar entre 1 e %d", ErrInvalid,
			*llm.MaxTokens, name, math.MaxInt32) // an INTEGER column
	}
	return nil
}

// checkParams checks the params of the agent a, merged (see AllParams): their
// names, the ones of flags, none of a field (see columnFlags); their values, a
// line of text of maxParamValue bytes at most; and the ones of a language
// model (see llmParam), for an LLM agent with its language model only.
func checkParams(a Agent) error {
	params := a.AllParams()
	for _, name := range slices.Sorted(maps.Keys(params)) {
		value := params[name]
		if err := checkParam(name, value, " do agente "+a.Name); err != nil {
			return err
		}
		switch {
		case llmParam(name) && a.Kind != "llm":
			return fmt.Errorf("%w: o parâmetro %s do agente %s é só para agentes LLM", ErrInvalid, name, a.Name)
		case llmParam(name) && a.LLM == nil:
			return fmt.Errorf("%w: o parâmetro %s do agente %s é do LLM, que o agente não tem", ErrInvalid, name,
				a.Name)
		}
	}
	return nil
}

// checkParam checks the param name of value, of an agent when of tells it
// (eg: " do agente orb"): the name of a flag, none of a field (see
// columnFlags), the value a line of text of maxParamValue bytes at most, not
// the line of the param pasted whole (see repeatsName).
func checkParam(name, value, of string) error {
	switch {
	case !paramNameRegexp.MatchString(name):
		return fmt.Errorf("%w: o nome do parâmetro %q%s deve ser o de uma flag: letras minúsculas, números e -",
			ErrInvalid, name, of)
	case columnFlags[name]:
		return fmt.Errorf("%w: o parâmetro %s%s tem campo próprio: altere-o lá, não nos parâmetros", ErrInvalid,
			name, of)
	case len(value) > maxParamValue || !oneLine(value):
		return fmt.Errorf("%w: o parâmetro %s%s deve ficar numa linha só, com até %d bytes", ErrInvalid, name, of,
			maxParamValue)
	case repeatsName(name, value):
		return fmt.Errorf("%w: o valor do parâmetro %s%s repete o nome: informe só o que vem depois do =",
			ErrInvalid, name, of)
	}
	return nil
}

// repeatsName reports whether the value of the param name starts with its name
// and =, eg: "lot = 0.01" for lot: the line of the param pasted whole.
func repeatsName(name, value string) bool {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(strings.TrimSpace(value), "-"), name)
	return ok && strings.HasPrefix(strings.TrimSpace(rest), "=")
}

// checkSecrets fails on a secret of the agent a without the record keeping
// it: the token of the bridge without an account, the key of the language
// model without a language model.
func checkSecrets(a Agent, secrets AgentSecrets) error {
	set := func(secret Secret) bool { return secret != nil && strings.TrimSpace(*secret) != "" }
	switch {
	case set(secrets.BridgeToken) && a.Account == nil:
		return fmt.Errorf("%w: o agente %s não tem conta para receber o token da bridge", ErrInvalid, a.Name)
	case set(secrets.LLMAPIKey) && a.LLM == nil:
		return fmt.Errorf("%w: o agente %s não tem LLM para receber a chave da API", ErrInvalid, a.Name)
	}
	return nil
}

// validAddress reports whether address is host:port, its port a number of 1
// to 65535, as the API of an agent listens, on the loopback (see
// ui.LoopbackHost): another host, or the unspecified address, would serve it
// to the network, and have the controller send its requests there.
func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !ui.LoopbackHost(host) {
		return false
	}
	number, err := strconv.ParseUint(port, 10, 16)
	return err == nil && number > 0
}

// httpURL reports whether text is an http(s) URL with a host and without
// credentials, which the settings would show: the tokens go apart.
func httpURL(text string) bool {
	u, err := url.Parse(text)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

// validComment reports whether comment is one of the orders of an agent on
// MT5 as the exchange takes it: 1 to maxComment printable ASCII characters,
// not starting with [, as the comments of the exits by the levels.
func validComment(comment string) bool {
	if comment == "" || len(comment) > maxComment || strings.HasPrefix(comment, "[") {
		return false
	}
	return !strings.ContainsFunc(comment, func(r rune) bool { return r < 0x20 || r > 0x7e })
}

// oneLine reports whether text is a line of UTF-8 text, without control
// characters: a value of the command line of an agent.
func oneLine(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsFunc(text, unicode.IsControl)
}

// Agents returns the agents with their accounts and language models, in the
// order of the dashboard.
func (s *Store) Agents(ctx context.Context) ([]Agent, error) {
	var agents []Agent
	err := s.conn(ctx).Preload("Account").Preload("MT5Link.Account").Preload("LLM").Preload("Reanalysis").Preload("Supervision").Preload("Protection").Preload("DailyLimit").Preload("BotControl").Order("position, name").
		Find(&agents).Error
	return agents, err
}

// Agent returns the agent of id.
func (s *Store) Agent(ctx context.Context, id int64) (Agent, error) {
	var agent Agent
	err := s.conn(ctx).Preload("Account").Preload("MT5Link.Account").Preload("LLM").Preload("Reanalysis").Preload("Supervision").Preload("Protection").Preload("DailyLimit").Preload("BotControl").First(&agent, id).Error
	return agent, notFound(err, fmt.Sprintf("o agente %d", id))
}

// AgentByName returns the agent of name.
func (s *Store) AgentByName(ctx context.Context, name string) (Agent, error) {
	var agent Agent
	err := s.conn(ctx).Preload("Account").Preload("MT5Link.Account").Preload("LLM").Preload("Reanalysis").Preload("Supervision").Preload("Protection").Preload("DailyLimit").Preload("BotControl").Where("name = ?", name).
		First(&agent).Error
	return agent, notFound(err, "o agente "+name)
}

// CreateAgent creates an agent with its account and language model, if any,
// their secrets encrypted: checked (see checkAgent), its API at an address no
// other agent has and, trading, positions no other agent trading trades
// (ErrConflict otherwise, see checkBookFree), its params given merged or apart
// (see Agent.AllParams), its prompt one of the database, not deleted, nor
// meanwhile (see sharePrompt). An agent without a position, 0, goes after the
// others on the dashboard. An LLM agent gets the switch of its reanalysis, off
// without its Reanalysis (see AgentReanalysis), one of another kind none.
func (s *Store) CreateAgent(ctx context.Context, agent Agent, secrets AgentSecrets) (Agent, error) {
	agent = normalized(agent)
	agent.ID = 0
	if agent.OrderIDNext < 1 {
		agent.OrderIDNext = 1
	}
	agent.setParams(agent.AllParams())
	if err := checkAgent(agent); err != nil {
		return Agent{}, err
	}
	if err := checkSecrets(agent, secrets); err != nil {
		return Agent{}, err
	}
	whole := agent
	account, llm := agent.Account, agent.LLM
	var reanalysis *AgentReanalysis
	if agent.Kind == "llm" {
		reanalysis = &AgentReanalysis{Allowed: agent.Reanalysis != nil && agent.Reanalysis.Allowed}
	}
	agent.Account, agent.LLM, agent.Reanalysis = nil, nil, nil
	err := s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.checkAddressFree(ctx, 0, agent.APIAddress); err != nil {
			return err
		}
		if err := s.checkBookFree(ctx, 0, whole); err != nil {
			return err
		}
		if llm != nil && llm.PromptID != nil {
			err := s.sharePrompt(ctx, *llm.PromptID)
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("%w: o prompt %d do agente %s não existe", ErrInvalid, *llm.PromptID, agent.Name)
			}
			if err != nil {
				return err
			}
		}
		if agent.Position == 0 {
			err := s.conn(ctx).Raw("SELECT COALESCE(MAX(position) + 1, 0) FROM agents WHERE deleted_at IS NULL").
				Scan(&agent.Position).Error
			if err != nil {
				return err
			}
		}
		if err := s.conn(ctx).Create(&agent).Error; err != nil {
			return notFound(err, "o agente "+agent.Name)
		}
		if account != nil {
			account.AgentID = agent.ID
			if err := s.sealInto(&account.BridgeToken, bridgeTokenOf(agent.ID), secrets.BridgeToken); err != nil {
				return err
			}
			if err := s.conn(ctx).Create(account).Error; err != nil {
				return notFound(err, "a conta do agente "+agent.Name)
			}
		}
		if llm != nil {
			llm.AgentID = agent.ID
			if err := s.sealInto(&llm.APIKey, llmAPIKeyOf(agent.ID), secrets.LLMAPIKey); err != nil {
				return err
			}
			if err := s.conn(ctx).Create(llm).Error; err != nil {
				return notFound(err, "o LLM do agente "+agent.Name)
			}
		}
		if reanalysis != nil {
			reanalysis.AgentID = agent.ID
			if err := s.conn(ctx).Create(reanalysis).Error; err != nil {
				return notFound(err, "a reanálise do agente "+agent.Name)
			}
		}
		return nil
	})
	if err != nil {
		return Agent{}, err
	}
	return s.Agent(ctx, agent.ID)
}

// checkAddressFree fails with ErrConflict when an agent other than the one of
// id, not deleted, serves its API at address, or at the same port of the
// machine under another name (see sameListener). In a transaction: the
// addresses of the agents are then checked one at a time until its end, so
// that two agents can't take the same one.
func (s *Store) checkAddressFree(ctx context.Context, id int64, address string) error {
	if err := s.lockAgents(ctx); err != nil {
		return err
	}
	var others []Agent
	err := s.conn(ctx).Select("id", "name", "api_address").Where("id <> ?", id).Order("name").Find(&others).Error
	if err != nil {
		return err
	}
	for _, other := range others {
		if sameListener(address, other.APIAddress) {
			return fmt.Errorf("%w: o endereço %s já é do agente %s", ErrConflict, address, other.Name)
		}
	}
	return nil
}

// lockAgents locks the agents until the end of the transaction, for the checks
// of what they share with the others to apply one at a time, see
// checkAddressFree and checkBookFree.
func (s *Store) lockAgents(ctx context.Context) error {
	return s.conn(ctx).Exec("SELECT pg_advisory_xact_lock(hashtext('xtrader.agents'))").Error
}

// checkBookFree fails with ErrConflict when the agent a, of id, trades on MT5
// (see Agent.trades) the positions of another agent trading, not deleted: the
// same bridge, the same magic number and a pair in common (see
// agentapi.MT5Book.Overlaps), as two processes of the same magic number on a
// pair close, move and net the positions of each other. The agents running
// apart count as the others, by their settings: the controller doesn't see
// them run (see controller.Manager, which never runs two agents trading them
// at once). The agents that don't trade, disabled or paper ones, may share
// them. In a transaction, see lockAgents.
func (s *Store) checkBookFree(ctx context.Context, id int64, a Agent) error {
	if !a.trades() {
		return nil
	}
	if err := s.lockAgents(ctx); err != nil {
		return err
	}
	var others []Agent
	err := s.conn(ctx).Preload("Account").Preload("MT5Link.Account").Preload("LLM").
		Where("id <> ? AND enabled AND mode = ?", id, "live").Order("position, name").Find(&others).Error
	if err != nil {
		return err
	}
	book := a.MT5Book()
	for _, other := range others {
		if theirs := other.MT5Book(); book.Overlaps(theirs) {
			return fmt.Errorf("%w: o agente %s, ligado no modo live, já opera %s com o magic number %d (comentário "+
				"%q) na bridge %s: dê a ele ou ao agente %s um comentário ou um magic number próprio, ou desligue um "+
				"dos dois", ErrConflict, other.Name, strings.Join(book.Common(theirs), ";"), book.Magic, book.Comment,
				book.Bridge, a.Name)
		}
	}
	return nil
}

// sameListener reports whether the APIs of two agents at the addresses a and
// b would listen at the same port: the same host, case aside, or two names of
// the machine the controller launches them on, the loopback and the
// unspecified addresses, eg: localhost:5580 and 127.0.0.1:5580.
func sameListener(a, b string) bool {
	hostA, portA, errA := net.SplitHostPort(a)
	hostB, portB, errB := net.SplitHostPort(b)
	if errA != nil || errB != nil {
		return strings.EqualFold(a, b)
	}
	return portA == portB && (strings.EqualFold(hostA, hostB) || localHost(hostA) && localHost(hostB))
}

// localHost reports whether host names the machine itself: localhost, a
// loopback address or the unspecified one.
func localHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// sealInto encrypts secret into column, for its place (see seal), when set.
func (s *Store) sealInto(column *[]byte, place string, secret Secret) error {
	if secret == nil {
		return nil
	}
	sealed, err := s.seal(place, *secret)
	if err != nil {
		return err
	}
	*column = sealed
	return nil
}

// UpdateAgent changes the agent of id, see AgentUpdate: a change of more than
// its switches (Enabled, TradePanel, TelegramControl, Reanalysis) is checked
// with the whole agent, as CreateAgent checks a new one, and a new address
// with the ones of the others; the switches alone always apply, eg: to disable
// it, but for enabling an agent trading the positions of another one, as a
// change of what it trades (ErrConflict, see checkBookFree), and for the
// reanalysis of an agent of another kind than llm (ErrInvalid).
func (s *Store) UpdateAgent(ctx context.Context, id int64, update AgentUpdate) (Agent, error) {
	err := s.Transaction(ctx, func(ctx context.Context) error {
		agent, err := s.lockedAgent(ctx, id)
		if err != nil {
			return err
		}
		return s.updateLocked(ctx, agent, update)
	})
	if err != nil {
		return Agent{}, err
	}
	return s.Agent(ctx, id)
}

// lockedAgent returns the agent of id, its row locked until the end of the
// transaction of ctx, with all it has.
func (s *Store) lockedAgent(ctx context.Context, id int64) (Agent, error) {
	var agent Agent
	err := s.conn(ctx).Clauses(lockForUpdate()).Preload("Account").Preload("MT5Link.Account").Preload("LLM").Preload("Reanalysis").Preload("Supervision").Preload("Protection").Preload("DailyLimit").Preload("BotControl").
		First(&agent, id).Error
	return agent, notFound(err, fmt.Sprintf("o agente %d", id))
}

// updateLocked applies update to the agent, its row locked (see lockedAgent),
// checked as UpdateAgent checks it.
func (s *Store) updateLocked(ctx context.Context, agent Agent, update AgentUpdate) error {
	change := update.apply(agent)
	if err := checkReanalysis(change.agent); err != nil {
		return err
	}
	if !update.switchesOnly() {
		if err := checkAgent(change.agent); err != nil {
			return err
		}
	}
	if update.APIAddress != nil && !strings.EqualFold(change.agent.APIAddress, agent.APIAddress) {
		if err := s.checkAddressFree(ctx, agent.ID, change.agent.APIAddress); err != nil {
			return err
		}
	}
	if update.touchesBook() {
		if err := s.checkBookFree(ctx, agent.ID, change.agent); err != nil {
			return err
		}
	}
	return s.saveChange(ctx, agent, change)
}

// saveChange writes the change c of the agent a, its account, its language
// model and the switch of its reanalysis created when it had none.
func (s *Store) saveChange(ctx context.Context, a Agent, c agentChange) error {
	if len(c.columns) > 0 {
		if err := s.conn(ctx).Model(&Agent{}).Where("id = ?", a.ID).Updates(c.columns).Error; err != nil {
			return notFound(err, "o agente "+a.Name)
		}
	}
	if c.reanalysis != nil {
		err := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"allowed"})}).Create(c.reanalysis).Error
		if err != nil {
			return notFound(err, "a reanálise do agente "+a.Name)
		}
	}
	switch {
	case c.account != nil && a.Account == nil:
		if err := s.conn(ctx).Create(c.agent.Account).Error; err != nil {
			return notFound(err, "a conta do agente "+a.Name)
		}
	case len(c.account) > 0:
		if err := s.conn(ctx).Model(&Account{}).Where("agent_id = ?", a.ID).Updates(c.account).Error; err != nil {
			return notFound(err, "a conta do agente "+a.Name)
		}
	}
	switch {
	case c.llm != nil && a.LLM == nil:
		if err := s.conn(ctx).Create(c.agent.LLM).Error; err != nil {
			return notFound(err, "o LLM do agente "+a.Name)
		}
	case len(c.llm) > 0:
		if err := s.conn(ctx).Model(&LLMSettings{}).Where("agent_id = ?", a.ID).Updates(c.llm).Error; err != nil {
			return notFound(err, "o LLM do agente "+a.Name)
		}
	}
	return nil
}

// DeleteAgent deletes the agent of id, softly: it stays in its table out of
// every query, with its account and its language model, their secrets
// removed, and with its orders, its decisions and its journal; a new agent
// may take its name. An agent enabled isn't deleted (ErrConflict): disabled,
// it is stopped.
func (s *Store) DeleteAgent(ctx context.Context, id int64) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		var agent Agent
		if err := s.conn(ctx).Clauses(lockForUpdate()).First(&agent, id).Error; err != nil {
			return notFound(err, fmt.Sprintf("o agente %d", id))
		}
		if agent.Enabled {
			return fmt.Errorf("%w: desligue o agente %s antes", ErrConflict, agent.Name)
		}
		err := s.conn(ctx).Model(&Account{}).Where("agent_id = ?", id).Update("bridge_token", nil).Error
		if err != nil {
			return err
		}
		err = s.conn(ctx).Model(&LLMSettings{}).Where("agent_id = ?", id).
			Update("api_key", nil).Error
		if err != nil {
			return err
		}
		return s.conn(ctx).Delete(&agent).Error
	})
}

// formerNameRow is a name an agent had before it was renamed, see RenameAgent.
type formerNameRow struct {
	AgentID int64
	Name    string
}

func (formerNameRow) TableName() string { return "agent_former_names" }

// RenameAgent renames the agent of id to name, keeping all it has: its ID, its
// account (its comment and magic number at the broker), its language model,
// its orders, its decisions and its journal; its former name is kept (see
// agent_former_names), so the seeds don't bring it back. A name taken by
// another agent is ErrConflict, one out of the rules ErrInvalid. An agent
// enabled isn't renamed (ErrConflict): disabled, it is stopped, and its files
// named after it, eg: its outbox, can be renamed with it, see Manager.
func (s *Store) RenameAgent(ctx context.Context, id int64, name string) (Agent, error) {
	name = strings.TrimSpace(name)
	if !agentNameRegexp.MatchString(name) {
		return Agent{}, fmt.Errorf("%w: nome de agente %q inválido: use letras, números, '.', '_' e '-', até 64 "+
			"caracteres, começando por letra ou número", ErrInvalid, name)
	}
	err := s.Transaction(ctx, func(ctx context.Context) error {
		var agent Agent
		if err := s.conn(ctx).Clauses(lockForUpdate()).First(&agent, id).Error; err != nil {
			return notFound(err, fmt.Sprintf("o agente %d", id))
		}
		switch {
		case agent.Name == name:
			return nil
		case agent.Enabled:
			return fmt.Errorf("%w: desligue o agente %s antes", ErrConflict, agent.Name)
		}
		var taken int64
		if err := s.conn(ctx).Model(&Agent{}).Where("name = ? AND id <> ?", name, id).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("%w: já existe um agente chamado %s", ErrConflict, name)
		}
		err := s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Create(&formerNameRow{AgentID: id, Name: agent.Name}).Error
		if err != nil {
			return err
		}
		err = s.conn(ctx).Model(&Agent{}).Where("id = ?", id).Update("name", name).Error
		return notFound(err, "o agente "+name)
	})
	if err != nil {
		return Agent{}, err
	}
	return s.Agent(ctx, id)
}

// SetAgentPrompt selects the prompt of the LLM agent of id, the default one
// embedded in the agent when nil: one deleted, or deleted meanwhile, is
// ErrNotFound (see sharePrompt).
func (s *Store) SetAgentPrompt(ctx context.Context, id int64, promptID *int64) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		if promptID != nil {
			if err := s.sharePrompt(ctx, *promptID); err != nil {
				return err
			}
		}
		result := s.conn(ctx).Model(&LLMSettings{}).Where("agent_id = ?", id).Update("prompt_id", promptID)
		if result.Error == nil && result.RowsAffected == 0 {
			return fmt.Errorf("%w: o agente %d não é um agente LLM", ErrInvalid, id)
		}
		return result.Error
	})
}

// SetAgentSecrets writes the secrets of the agent of id, the ones set: the
// token of the bridge to its account, the key to its language model.
func (s *Store) SetAgentSecrets(ctx context.Context, id int64, secrets AgentSecrets) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		if secrets.BridgeToken != nil {
			var sealed []byte
			if err := s.sealInto(&sealed, bridgeTokenOf(id), secrets.BridgeToken); err != nil {
				return err
			}
			result := s.conn(ctx).Model(&Account{}).Where("agent_id = ?", id).Update("bridge_token", sealed)
			if result.Error == nil && result.RowsAffected == 0 {
				return fmt.Errorf("%w: o agente %d não tem conta", ErrInvalid, id)
			}
			if result.Error != nil {
				return result.Error
			}
		}
		columns := map[string]interface{}{}
		for column, secret := range map[string]Secret{"api_key": secrets.LLMAPIKey} {
			if secret == nil {
				continue
			}
			var sealed []byte
			if err := s.sealInto(&sealed, placeOf("agent_llm_settings", column, id), secret); err != nil {
				return err
			}
			columns[column] = sealed
		}
		if len(columns) == 0 {
			return nil
		}
		result := s.conn(ctx).Model(&LLMSettings{}).Where("agent_id = ?", id).Updates(columns)
		if result.Error == nil && result.RowsAffected == 0 {
			return fmt.Errorf("%w: o agente %d não é um agente LLM", ErrInvalid, id)
		}
		return result.Error
	})
}

// AgentSecrets returns the secrets of the agent of id, decrypted: only for
// the agent itself, see the internal API of the controller.
func (s *Store) AgentSecrets(ctx context.Context, id int64) (AgentSecretValues, error) {
	agent, err := s.Agent(ctx, id)
	if err != nil {
		return AgentSecretValues{}, err
	}
	return s.SecretsOf(agent)
}

// MT5AccountOf returns the account of MT5 the agent a trades, as read, nil
// when it isn't linked to one (its own bridge then, see Account).
func (a Agent) MT5AccountOf() *MT5Account {
	if a.MT5Link == nil {
		return nil
	}
	return a.MT5Link.Account
}

// SecretsOf returns the secrets of agent, as read, decrypted, eg: the ones it
// was launched with: only for the agent itself, see AgentSecrets.
func (s *Store) SecretsOf(agent Agent) (AgentSecretValues, error) {
	var (
		values AgentSecretValues
		err    error
	)
	switch account := agent.MT5AccountOf(); {
	case account != nil: // the token of the bridge of its account of MT5
		if values.BridgeToken, err = s.open(mt5TokenOf(account.ID), account.BridgeToken); err != nil {
			return AgentSecretValues{}, err
		}
	case agent.Account != nil:
		if values.BridgeToken, err = s.open(bridgeTokenOf(agent.ID), agent.Account.BridgeToken); err != nil {
			return AgentSecretValues{}, err
		}
	}
	if agent.LLM != nil {
		if values.LLMAPIKey, err = s.open(llmAPIKeyOf(agent.ID), agent.LLM.APIKey); err != nil {
			return AgentSecretValues{}, err
		}
	}
	return values, nil
}

// SetPromptStatus keeps the last prompt switch the LLM agent of id reported.
func (s *Store) SetPromptStatus(ctx context.Context, id int64, status PromptStatus) error {
	return s.conn(ctx).Model(&LLMSettings{}).Where("agent_id = ?", id).Update("prompt_status", &status).Error
}

// LeaseOrderIDs returns a range of count order IDs for the agent of id, first
// to last, never leased again: the agent numbers its orders with them.
func (s *Store) LeaseOrderIDs(ctx context.Context, id, count int64) (first, last int64, err error) {
	if count < 1 {
		return 0, 0, fmt.Errorf("%w: não dá para reservar %d IDs de ordem: o mínimo é 1", ErrInvalid, count)
	}
	var next []int64
	err = s.conn(ctx).Raw("UPDATE agents SET order_id_next = order_id_next + ? WHERE id = ? AND deleted_at IS NULL "+
		"RETURNING order_id_next", count, id).Scan(&next).Error
	if err != nil {
		return 0, 0, err
	}
	if len(next) == 0 {
		return 0, 0, fmt.Errorf("%w: o agente %d não existe", ErrNotFound, id)
	}
	return next[0] - count, next[0] - 1, nil
}

// ReserveOrderIDs moves the next order ID of the agent of id past last, the
// highest one of its orders imported.
func (s *Store) ReserveOrderIDs(ctx context.Context, id, last int64) error {
	return s.conn(ctx).Exec("UPDATE agents SET order_id_next = GREATEST(order_id_next, ?) WHERE id = ?",
		last+1, id).Error
}
