package store

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Period is an accounting period of an agent: the cards of the dashboard
// (trades, win rate, net profit, profit factor) count the trades closed from
// StartedAt, until the next period started; Deposit, in Currency, is the
// balance it started with, and CreatedBy the user who started it. The orders
// are never touched: a period only tells which of their results count. See
// StartPeriod.
type Period struct {
	ID        int64     `gorm:"primaryKey" json:"id"`
	AgentID   int64     `json:"agent_id"`
	StartedAt time.Time `json:"started_at"`
	Deposit   float64   `json:"deposit"`
	Currency  string    `json:"currency"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}

func (Period) TableName() string { return "agent_periods" }

// maxDeposit is the deposit of a period at most.
const maxDeposit = 1e12

// currencyRegexp matches a code of a currency, eg: BRL.
var currencyRegexp = regexp.MustCompile(`^[A-Z]{3}$`)

// StartPeriod resets the results of the agent of name: a new period starting
// now, deposit, in currency, its balance (rounded to cents, see cents),
// started by the user by. The earlier periods are kept, the current one
// ending as it starts. A deposit out of 0 to 1e12, or a currency that isn't a
// code of 3 letters, is refused (ErrInvalid).
func (s *Store) StartPeriod(ctx context.Context, name string, deposit float64, currency, by string) (Period,
	error) {

	currency = strings.ToUpper(strings.TrimSpace(currency))
	switch {
	case math.IsNaN(deposit) || deposit < 0 || deposit > maxDeposit:
		return Period{}, fmt.Errorf("%w: o depósito deve ficar entre 0 e %.0f", ErrInvalid, float64(maxDeposit))
	case !currencyRegexp.MatchString(currency):
		return Period{}, fmt.Errorf("%w: a moeda deve ser um código de 3 letras, ex.: BRL, não %q", ErrInvalid,
			currency)
	}

	var period Period
	err := s.Transaction(ctx, func(ctx context.Context) error {
		var agent Agent
		err := s.conn(ctx).Clauses(lockForUpdate()).Where("name = ?", name).First(&agent).Error
		if err != nil {
			return notFound(err, "o agente "+name)
		}
		started := s.db.NowFunc()
		var last Period
		err = s.conn(ctx).Where("agent_id = ?", agent.ID).Order("started_at DESC").Limit(1).Find(&last).Error
		if err != nil {
			return err
		}
		if last.ID != 0 && !started.After(last.StartedAt) {
			started = last.StartedAt.Add(time.Microsecond) // after the one before, whatever the clock
		}
		period = Period{AgentID: agent.ID, StartedAt: started, Deposit: cents(deposit),
			Currency: currency, CreatedBy: strings.TrimSpace(by)}
		return notFound(s.conn(ctx).Create(&period).Error, "o período do agente "+name)
	})
	return period, err
}

// cents rounds a non-negative amount to the cent as written, half up: from
// its shortest decimal, eg: 2500.555 to 2500.56, which math.Round(x*100)/100
// makes 2500.55, the float of 2500.555 a hair below it. The column keeps the
// cents exactly (NUMERIC(16,2)).
func cents(amount float64) float64 {
	whole, fraction, _ := strings.Cut(strconv.FormatFloat(amount, 'f', -1, 64), ".")
	fraction += "000"
	units, err := strconv.ParseInt(whole+fraction[:2], 10, 64)
	if err != nil {
		return math.Round(amount*100) / 100
	}
	if fraction[2] >= '5' {
		units++
	}
	return float64(units) / 100
}

// Periods returns the periods of the agent of name, the current one first;
// none before its first reset.
func (s *Store) Periods(ctx context.Context, name string) ([]Period, error) {
	agent, err := s.AgentByName(ctx, name)
	if err != nil {
		return nil, err
	}
	var periods []Period
	err = s.conn(ctx).Where("agent_id = ?", agent.ID).Order("started_at DESC").Find(&periods).Error
	return periods, err
}

// CurrentPeriods returns the current period of the agents with one, by the
// name of the agent.
func (s *Store) CurrentPeriods(ctx context.Context) (map[string]Period, error) {
	var rows []struct {
		Name   string
		Period `gorm:"embedded"`
	}
	err := s.conn(ctx).Raw(`SELECT DISTINCT ON (p.agent_id) a.name, p.id, p.agent_id, p.started_at, p.deposit,
		p.currency, p.created_by, p.created_at
		FROM agent_periods p JOIN agents a ON a.id = p.agent_id AND a.deleted_at IS NULL
		ORDER BY p.agent_id, p.started_at DESC`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	periods := make(map[string]Period, len(rows))
	for _, row := range rows {
		periods[row.Name] = row.Period
	}
	return periods, nil
}
