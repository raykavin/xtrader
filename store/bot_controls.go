package store

import (
	"context"
	"time"

	"gorm.io/gorm/clause"
)

// AgentBotControl is the bot of an agent stopped by a user, from the dashboard
// (Parar bot) or from Telegram (/stop): kept so that the agent starts with its
// bot stopped again, whatever restarts its process, until a user starts it
// (Iniciar bot, /start). ChangedBy and ChangedAt tell the last change.
type AgentBotControl struct {
	AgentID   int64     `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Stopped   bool      `json:"stopped"`
	ChangedBy string    `json:"changed_by"`
	ChangedAt time.Time `json:"changed_at"`
}

func (AgentBotControl) TableName() string { return "agent_bot_controls" }

// BotStopped reports whether a user stopped the bot of the agent a, and by
// whom and when: so it starts stopped (see AgentBotControl).
func (a Agent) BotStopped() (stopped bool, by string, at time.Time) {
	if c := a.BotControl; c != nil && c.Stopped {
		return true, c.ChangedBy, c.ChangedAt
	}
	return false, "", time.Time{}
}

// SetAgentBotStopped records the bot of the agent of id stopped, or started,
// by by, now, see AgentBotControl.
func (s *Store) SetAgentBotStopped(ctx context.Context, id int64, stopped bool, by string) error {
	control := AgentBotControl{AgentID: id, Stopped: stopped, ChangedBy: by, ChangedAt: time.Now()}
	err := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"stopped", "changed_by", "changed_at"})}).
		Create(&control).Error
	return notFound(err, "o controle do bot do agente")
}
