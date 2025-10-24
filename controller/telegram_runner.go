package controller

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
)

// TelegramRunner runs the Telegram bot of the controller with the settings of
// the database (see Apply): started, stopped or restarted with a new token as
// they change, its users changed as they are, the events of the agents enabled
// relayed (see SyncRelays). A bot that can't start, eg: its token refused or
// the API out of reach, is tried again at the next Apply: the controller runs
// on without it meanwhile.
type TelegramRunner struct {
	ctx       context.Context
	dashboard *Dashboard
	options   []TelegramOption

	mu       sync.Mutex
	token    string // of the bot running or starting
	users    []int64
	relayed  []string
	bot      *Telegram
	stop     context.CancelFunc
	starting bool
	gen      int // of the last start asked, the older ones dropped
}

// NewTelegramRunner creates the runner of the bot of the dashboard, with
// options, eg: WithPrompts, until ctx is done.
func NewTelegramRunner(ctx context.Context, dashboard *Dashboard, options ...TelegramOption) *TelegramRunner {
	return &TelegramRunner{ctx: ctx, dashboard: dashboard, options: options}
}

// Apply runs the bot of cfg: off without a token, without users or disabled.
func (r *TelegramRunner) Apply(cfg TelegramConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users = append([]int64(nil), cfg.Users...)
	wanted := cfg.Enabled && cfg.Token != "" && len(cfg.Users) > 0
	switch {
	case !wanted:
		if r.bot != nil || r.starting {
			log.Infof("[SETUP] telegram: the bot stopped")
		}
		r.halt()
	case (r.bot != nil || r.starting) && r.token == cfg.Token:
		if r.bot != nil {
			r.bot.SetUsers(r.users)
		}
	default:
		r.halt()
		r.token, r.starting = cfg.Token, true
		go r.start(r.gen, cfg.Token)
	}
}

// start creates the bot of token, which needs the API of Telegram, and serves
// it unless applied again meanwhile.
func (r *TelegramRunner) start(gen int, token string) {
	r.mu.Lock()
	users := r.users
	r.mu.Unlock()
	bot, err := NewTelegram(r.dashboard, token, users, r.options...)

	r.mu.Lock()
	defer r.mu.Unlock()
	if gen != r.gen {
		return // stopped or restarted meanwhile: never served
	}
	r.starting = false
	if err != nil { // without the token, see NewTelegram
		log.Errorf("[SETUP] telegram: %v: the controller runs without Telegram until the next try", err)
		r.token = "" // tried again at the next Apply
		return
	}
	ctx, cancel := context.WithCancel(r.ctx)
	bot.SetUsers(r.users)
	bot.Start(ctx)
	bot.SyncRelays(r.relayed)
	r.bot, r.stop = bot, cancel
	log.Infof("[SETUP] telegram: the bot serves %d users", len(r.users))
}

// halt stops the bot running, and drops the one starting. The caller holds
// mu.
func (r *TelegramRunner) halt() {
	if r.stop != nil {
		r.stop()
	}
	r.bot, r.stop, r.token, r.starting = nil, nil, "", false
	r.gen++
}

// SyncRelays relays the events of the agents of names, the ones enabled.
func (r *TelegramRunner) SyncRelays(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.relayed = append([]string(nil), names...)
	if r.bot != nil {
		r.bot.SyncRelays(r.relayed)
	}
}

// Running reports whether the bot serves.
func (r *TelegramRunner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bot != nil
}
