package store

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/ui"
)

const (
	// maxExposure is the highest limit of a rule of the exposure: agents on an
	// asset in a direction.
	maxExposure = 100
	// maxExposurePairs is the most assets limited by the exposure.
	maxExposurePairs = 50
)

// exposureSymbol is an asset of the exposure limit, as MT5 names them.
var exposureSymbol = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._#-]{0,31}$`)

// ExposureLimits is the exposure limit of all the agents, a single record, see
// ui.ExposureSettings: none while never set (off). Its rules are its pairs;
// the column max_agents of the table, a limit of every asset of its first
// version, is never read nor written (left at 0).
type ExposureLimits struct {
	ID        int16           `gorm:"primaryKey;autoIncrement:false"`
	Pairs     json.RawMessage `gorm:"type:jsonb"`
	UpdatedBy string
	UpdatedAt time.Time
}

func (ExposureLimits) TableName() string { return "exposure_limits" }

// Settings returns the limit as the dashboard takes it.
func (e ExposureLimits) Settings() ui.ExposureSettings {
	settings := ui.ExposureSettings{UpdatedBy: e.UpdatedBy}
	_ = json.Unmarshal(e.Pairs, &settings.Pairs)
	if len(settings.Pairs) == 0 {
		settings.Pairs = nil
	}
	if !e.UpdatedAt.IsZero() {
		settings.UpdatedAt = e.UpdatedAt.Unix()
	}
	return settings
}

// LimitOf returns the most agents that may hold pair in a direction at once,
// by its rule, 0 for none (no rule).
func (e ExposureLimits) LimitOf(pair string) int {
	return e.Settings().Pairs[pair]
}

// On reports whether the limit applies to some asset: a rule at least.
func (e ExposureLimits) On() bool {
	for _, limit := range e.Settings().Pairs {
		if limit > 0 {
			return true
		}
	}
	return false
}

// validateExposure returns why settings are refused: at most
// maxExposurePairs assets, each as MT5 names it, its limit from 1 to
// maxExposure.
func validateExposure(settings ui.ExposureSettings) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: exposição por ativo: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	if len(settings.Pairs) > maxExposurePairs {
		return invalid("use no máximo %d ativos", maxExposurePairs)
	}
	for pair, limit := range settings.Pairs {
		switch {
		case !exposureSymbol.MatchString(pair):
			return invalid("o ativo %q é inválido: use o nome do ativo no MT5, ex.: XAUUSDm", pair)
		case limit < 1 || limit > maxExposure:
			return invalid("o máximo de agentes de %s deve ficar entre 1 e %d", pair, maxExposure)
		}
	}
	return nil
}

// ExposureLimits returns the exposure limit of all the agents, false while
// never set.
func (s *Store) ExposureLimits(ctx context.Context) (ExposureLimits, bool, error) {
	var limits ExposureLimits
	err := s.conn(ctx).Where("id = 1").Take(&limits).Error
	if err == gorm.ErrRecordNotFound {
		return ExposureLimits{}, false, nil
	}
	return limits, err == nil, err
}

// SetExposureLimits sets the exposure limit of all the agents, validated (see
// validateExposure), by by, and returns it.
func (s *Store) SetExposureLimits(ctx context.Context, settings ui.ExposureSettings, by string) (ExposureLimits,
	error) {
	if err := validateExposure(settings); err != nil {
		return ExposureLimits{}, err
	}
	pairs := settings.Pairs
	if pairs == nil {
		pairs = map[string]int{}
	}
	data, err := json.Marshal(pairs)
	if err != nil {
		return ExposureLimits{}, err
	}
	limits := ExposureLimits{ID: 1, Pairs: data, UpdatedBy: by, UpdatedAt: time.Now()}
	err = s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
		Create(&limits).Error
	return limits, err
}
