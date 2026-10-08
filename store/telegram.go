package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

// Telegram returns the settings of the Telegram bot, and whether it has a
// token, without it.
func (s *Store) Telegram(ctx context.Context) (settings TelegramSettings, hasToken bool, err error) {
	err = s.conn(ctx).Attrs(TelegramSettings{ID: 1}).FirstOrInit(&settings, TelegramSettings{ID: 1}).Error
	return settings, len(settings.Token) > 0, err
}

// TelegramToken returns the token of the Telegram bot, decrypted: "" for none.
func (s *Store) TelegramToken(ctx context.Context) (string, error) {
	settings, _, err := s.Telegram(ctx)
	if err != nil {
		return "", err
	}
	return s.open(telegramToken(), settings.Token)
}

// SetTelegram changes the settings of the Telegram bot: enabled when set, and
// its token, see Secret; the one kept, sealed by a version before the places
// of the secrets (see unseal), sealed again for its place as it is written.
// The row is locked from its read to its write, and only the columns changed
// are written: two changes at once, eg: the switch and the token, both apply.
func (s *Store) SetTelegram(ctx context.Context, enabled *bool, token Secret) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		err := s.conn(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&TelegramSettings{ID: 1}).Error
		if err != nil {
			return err
		}
		var settings TelegramSettings
		if err := s.conn(ctx).Clauses(lockForUpdate()).First(&settings, 1).Error; err != nil {
			return err
		}
		columns := map[string]interface{}{"updated_at": time.Now()}
		if enabled != nil {
			columns["enabled"] = *enabled
		}
		if token == nil {
			if kept, placed, err := s.unseal(telegramToken(), settings.Token); err == nil && !placed {
				token = &kept
			}
		}
		if token != nil {
			if err := s.sealInto(&settings.Token, telegramToken(), token); err != nil {
				return err
			}
			columns["token"] = settings.Token
		}
		return s.conn(ctx).Model(&TelegramSettings{}).Where("id = 1").Updates(columns).Error
	})
}

// TelegramUsers returns the Telegram users the bot answers, by their ID.
func (s *Store) TelegramUsers(ctx context.Context) ([]TelegramUser, error) {
	var users []TelegramUser
	err := s.conn(ctx).Order("telegram_id").Find(&users).Error
	return users, err
}

// AddTelegramUser authorizes the Telegram user of telegramID, named name.
func (s *Store) AddTelegramUser(ctx context.Context, telegramID int64, name string) (TelegramUser, error) {
	if telegramID <= 0 {
		return TelegramUser{}, fmt.Errorf("%w: Telegram ID %d", ErrInvalid, telegramID)
	}
	user := TelegramUser{TelegramID: telegramID, Name: strings.TrimSpace(name)}
	err := s.conn(ctx).Create(&user).Error
	return user, notFound(err, fmt.Sprintf("Telegram user %d", telegramID))
}

// DeleteTelegramUser stops answering the Telegram user of id, the record.
func (s *Store) DeleteTelegramUser(ctx context.Context, id int64) error {
	result := s.conn(ctx).Delete(&TelegramUser{}, id)
	if result.Error == nil && result.RowsAffected == 0 {
		return fmt.Errorf("%w: Telegram user %d", ErrNotFound, id)
	}
	return result.Error
}
