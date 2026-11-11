package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ErrCredentials is the error of a login refused: an unknown or disabled user,
// or a wrong password, told apart nowhere.
var ErrCredentials = errors.New("store: invalid username or password")

// bcryptCost is the cost of the password hashes: about 250 ms a login.
const bcryptCost = 12

// passwordMin and passwordMax bound the length of a password in bytes, bcrypt
// reading 72 at most.
const (
	passwordMin = 8
	passwordMax = 72
)

// usernameRegexp is the rule of the usernames, the one of the table.
var usernameRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// dummyHash is compared with the password of an unknown user, so that the time
// of a login doesn't tell whether the user exists: computed at its first use,
// not at the start of every program importing the store.
var dummyHash = sync.OnceValue(func() []byte {
	hash, _ := bcrypt.GenerateFromPassword([]byte("xtrader: no such user"), bcryptCost)
	return hash
})

// hashPassword checks password and returns its bcrypt hash.
func hashPassword(password string) (string, error) {
	switch {
	case len(password) < passwordMin:
		return "", fmt.Errorf("%w: a senha deve ter ao menos %d caracteres", ErrInvalid, passwordMin)
	case len(password) > passwordMax:
		return "", fmt.Errorf("%w: a senha deve ter até %d bytes", ErrInvalid, passwordMax)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("store: the password: %w", err)
	}
	return string(hash), nil
}

// CreateUser creates an enabled user of the dashboard.
func (s *Store) CreateUser(ctx context.Context, username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if !usernameRegexp.MatchString(username) {
		return User{}, fmt.Errorf("%w: nome de usuário %q inválido: use letras, números e . _ @ -, até 64 "+
			"caracteres", ErrInvalid, username)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	user := User{Username: username, PasswordHash: hash, Enabled: true}
	if err := s.conn(ctx).Create(&user).Error; err != nil {
		return User{}, notFound(err, "o usuário "+username)
	}
	return user, nil
}

// Users returns the users, by username, with their roles.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	var users []User
	err := s.conn(ctx).Preload("RoleLink.Role").Order("lower(username)").Find(&users).Error
	return users, err
}

// User returns the user of id, with its role and the permissions of it.
func (s *Store) User(ctx context.Context, id int64) (User, error) {
	var user User
	err := s.conn(ctx).Preload("RoleLink.Role.Permissions").First(&user, id).Error
	return user, notFound(err, fmt.Sprintf("o usuário %d", id))
}

// CountUsers returns the number of users, the disabled ones included.
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var count int64
	err := s.conn(ctx).Model(&User{}).Count(&count).Error
	return count, err
}

// SetPassword replaces the password of the user of id.
func (s *Store) SetPassword(ctx context.Context, id int64, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return s.updateUser(ctx, id, map[string]interface{}{"password_hash": hash})
}

// SetUserEnabled enables or disables the user of id: the last user enabled
// can't be disabled, nor the last one able to manage the users and the roles
// (see guardAdmins), which would lock the dashboard.
func (s *Store) SetUserEnabled(ctx context.Context, id int64, enabled bool) error {
	return s.guardAdmins(ctx, func(ctx context.Context) error {
		if !enabled {
			if err := s.keepOneEnabled(ctx, id); err != nil {
				return err
			}
		}
		return s.updateUser(ctx, id, map[string]interface{}{"enabled": enabled})
	})
}

// DeleteUser deletes the user of id, its role with it: not the last one
// enabled, nor the last one able to manage the users and the roles.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.guardAdmins(ctx, func(ctx context.Context) error {
		if err := s.keepOneEnabled(ctx, id); err != nil {
			return err
		}
		result := s.conn(ctx).Delete(&User{}, id)
		if result.Error == nil && result.RowsAffected == 0 {
			return fmt.Errorf("%w: o usuário %d não existe", ErrNotFound, id)
		}
		if result.Error != nil {
			return result.Error
		}
		return s.conn(ctx).Where("user_id = ?", id).Delete(&UserRole{}).Error
	})
}

// keepOneEnabled fails when the user of id is the only one enabled, locked
// meanwhile so that two requests can't disable the last two.
func (s *Store) keepOneEnabled(ctx context.Context, id int64) error {
	var enabled []int64
	if err := s.conn(ctx).Model(&User{}).Where("enabled").Clauses(lockForUpdate()).
		Pluck("id", &enabled).Error; err != nil {
		return err
	}
	if len(enabled) == 1 && enabled[0] == id {
		return fmt.Errorf("%w: o último usuário ativo não pode ser desativado nem excluído", ErrInvalid)
	}
	return nil
}

// updateUser updates the columns of the user of id.
func (s *Store) updateUser(ctx context.Context, id int64, columns map[string]interface{}) error {
	result := s.conn(ctx).Model(&User{}).Where("id = ?", id).Updates(columns)
	if result.Error == nil && result.RowsAffected == 0 {
		return fmt.Errorf("%w: o usuário %d não existe", ErrNotFound, id)
	}
	return result.Error
}

// Authenticate returns the enabled user of username whose password is
// password, with its role (see User), else ErrCredentials, and keeps the time
// of the login.
func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	var user User
	err := s.conn(ctx).Preload("RoleLink.Role.Permissions").
		Where("lower(username) = lower(?) AND enabled", strings.TrimSpace(username)).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return User{}, ErrCredentials
	}
	if err != nil {
		return User{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return User{}, ErrCredentials
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.conn(ctx).Model(&User{}).Where("id = ?", user.ID).UpdateColumn("last_login_at", now).Error; err != nil {
		return User{}, err
	}
	user.LastLoginAt = &now
	return user, nil
}

// Bootstrap creates the first user, username with password, when the
// database has none, the fixed role of the administrators given to it, and
// reports whether it did.
func (s *Store) Bootstrap(ctx context.Context, username, password string) (bool, error) {
	created := false
	err := s.Transaction(ctx, func(ctx context.Context) error {
		// one bootstrap at a time: two controllers starting on an empty database
		if err := s.conn(ctx).Exec("SELECT pg_advisory_xact_lock(hashtext('xtrader.bootstrap'))").Error; err != nil {
			return err
		}
		count, err := s.CountUsers(ctx)
		if err != nil || count > 0 {
			return err
		}
		user, err := s.CreateUser(ctx, username, password)
		if err != nil {
			return err
		}
		admin, err := s.AdminRole(ctx)
		if err != nil {
			return err
		}
		if err := s.SetUserRole(ctx, user.ID, admin.ID, "bootstrap"); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}
