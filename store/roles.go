package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/ui"
)

// RoleAdmin is the system of the fixed role of the administrators: every
// permission of the dashboard, the ones added later too, never changed nor
// deleted (created by sql/seeds).
const RoleAdmin = "admin"

// roleNameMax and roleDescriptionMax bound the name and the description of a
// role, in characters: the ones of the table.
const (
	roleNameMax        = 64
	roleDescriptionMax = 280
)

// errLastAdmin is the detail of a change of the access refused because it
// would leave no user enabled able to manage the users and the roles.
const errLastAdmin = "ao menos um usuário ativo precisa ter um papel que gerencie usuários e papéis: a mudança " +
	"deixaria o painel sem ninguém para administrá-lo"

// Role is a role of the users of the dashboard: the permissions it grants (see
// Granted), Users the number of users holding it (filled by Roles).
type Role struct {
	ID          int64            `gorm:"primaryKey" json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	System      string           `json:"system"`
	UpdatedBy   string           `json:"updated_by"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Permissions []RolePermission `gorm:"foreignKey:RoleID" json:"permissions"`
	Users       int64            `gorm:"-" json:"users"`
}

// RolePermission is a permission a role grants, see ui.PermissionAreas.
type RolePermission struct {
	RoleID     int64  `gorm:"primaryKey;autoIncrement:false"`
	Permission string `gorm:"primaryKey"`
}

// UserRole is the role of a user of the dashboard, who gave it last and when.
type UserRole struct {
	UserID    int64 `gorm:"primaryKey;autoIncrement:false"`
	RoleID    int64
	Role      *Role `gorm:"foreignKey:RoleID"`
	UpdatedBy string
	UpdatedAt time.Time
}

// RoleChange is a role created or changed, of the fields set, by By.
type RoleChange struct {
	Name        *string
	Description *string
	Permissions *[]string
	By          string
}

// Fixed reports whether r is the fixed role of the administrators.
func (r Role) Fixed() bool {
	return r.System == RoleAdmin
}

// Granted returns the permissions r grants as set, in the order of the
// catalog: every one for the fixed role; a permission gone from the catalog
// dropped.
func (r Role) Granted() []string {
	if r.Fixed() {
		return ui.AllPermissions()
	}
	set := make(map[string]bool, len(r.Permissions))
	for _, p := range r.Permissions {
		set[p.Permission] = true
	}
	granted := []string{}
	for _, p := range ui.AllPermissions() {
		if set[p] {
			granted = append(granted, p)
		}
	}
	return granted
}

// Effective returns what r lets its users see and do, the implied permissions
// included, see ui.Effective.
func (r Role) Effective() []string {
	return ui.Effective(r.Granted())
}

// RoleOf returns the role of u as read, nil for none: it can't log in.
func (u User) RoleOf() *Role {
	if u.RoleLink == nil {
		return nil
	}
	return u.RoleLink.Role
}

// roleName checks the name of a role and returns it trimmed.
func roleName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > roleNameMax {
		return "", fmt.Errorf("%w: o nome do papel deve ter de 1 a %d caracteres", ErrInvalid, roleNameMax)
	}
	return name, nil
}

// roleDescription checks the description of a role and returns it trimmed.
func roleDescription(description string) (string, error) {
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > roleDescriptionMax {
		return "", fmt.Errorf("%w: a descrição do papel deve ter até %d caracteres", ErrInvalid, roleDescriptionMax)
	}
	return description, nil
}

// rolePermissions checks the permissions of a role, each of the catalog, and
// returns them once each, trimmed.
func rolePermissions(list []string) ([]string, error) {
	permissions := []string{}
	for _, p := range list {
		p = strings.TrimSpace(p)
		if !ui.KnownPermission(p) {
			return nil, fmt.Errorf("%w: a permissão %q não existe", ErrInvalid, p)
		}
		if !slices.Contains(permissions, p) {
			permissions = append(permissions, p)
		}
	}
	return permissions, nil
}

// lockAccess serializes the changes of the access (the roles, their
// permissions, the users and their roles) to the end of the transaction of
// ctx, so that two of them can't leave the dashboard without an administrator
// together, see guardAdmins.
func (s *Store) lockAccess(ctx context.Context) error {
	return s.conn(ctx).Exec("SELECT pg_advisory_xact_lock(hashtext('xtrader.access'))").Error
}

// countAdmins returns the number of users enabled whose role lets them manage
// the users and the roles: the fixed role, or one granting users:manage.
func (s *Store) countAdmins(ctx context.Context) (int64, error) {
	var count int64
	err := s.conn(ctx).Raw(`
		SELECT count(*) FROM users u
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r ON r.id = ur.role_id
		WHERE u.enabled AND u.deleted_at IS NULL
		  AND (r.system = ? OR EXISTS (
		      SELECT 1 FROM role_permissions rp WHERE rp.role_id = r.id AND rp.permission = ?))`,
		RoleAdmin, ui.PermUsersManage).Scan(&count).Error
	return count, err
}

// guardAdmins runs fn, a change of the access, in a transaction holding the
// lock of the access (see lockAccess): refused (ErrConflict) when it leaves no
// user enabled able to manage the users and the roles where there was one.
func (s *Store) guardAdmins(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.lockAccess(ctx); err != nil {
			return err
		}
		before, err := s.countAdmins(ctx)
		if err != nil {
			return err
		}
		if err := fn(ctx); err != nil {
			return err
		}
		after, err := s.countAdmins(ctx)
		if err != nil {
			return err
		}
		if before > 0 && after == 0 {
			return fmt.Errorf("%w: %s", ErrConflict, errLastAdmin)
		}
		return nil
	})
}

// Roles returns the roles, the fixed one first and then by name, each with its
// number of users.
func (s *Store) Roles(ctx context.Context) ([]Role, error) {
	listed, err := s.FindRoles(ctx, ListQuery{})
	return listed.Rows, err
}

// roleSearch is where a search of the roles looks: their names, their
// descriptions and the areas of their permissions (see areaField).
var roleSearch = []searchField{
	textField("roles.name"),
	textField("roles.description"),
	areaField,
}

// areaField matches the word in the areas of the permissions of a role, by
// their labels (eg: Backtesting) or keys (eg: mt5-accounts): a role granting a
// permission of an area matching it, or the fixed role, granting them all.
func areaField(word string) (string, []any) {
	var permissions []string
	for _, area := range ui.PermissionAreas() {
		if strings.Contains(fold(area.Label), word) || strings.Contains(area.Key, word) {
			for _, p := range []string{area.View, area.Manage} {
				if p != "" {
					permissions = append(permissions, p)
				}
			}
		}
	}
	if len(permissions) == 0 {
		return "1 = 0", nil
	}
	return `(roles.system = ? OR EXISTS (SELECT 1 FROM role_permissions rp
		WHERE rp.role_id = roles.id AND rp.permission IN ?))`, []any{RoleAdmin, permissions}
}

// FindRoles returns the roles of q, the fixed one first and then by name, each
// with its number of users.
func (s *Store) FindRoles(ctx context.Context, q ListQuery) (Listed[Role], error) {
	db := s.conn(ctx).Model(&Role{}).Preload("Permissions").Scopes(searchScope(q.Search, roleSearch...))
	listed, err := list[Role](db, q, byFields("roles.system = ''", "lower(roles.name)", "roles.id"))
	if err != nil || len(listed.Rows) == 0 {
		return listed, err
	}
	ids := make([]int64, 0, len(listed.Rows))
	for _, role := range listed.Rows {
		ids = append(ids, role.ID)
	}
	var counts []struct {
		RoleID int64
		Users  int64
	}
	if err := s.conn(ctx).Raw(`
		SELECT ur.role_id, count(*) AS users FROM user_roles ur
		JOIN users u ON u.id = ur.user_id AND u.deleted_at IS NULL
		WHERE ur.role_id IN ?
		GROUP BY ur.role_id`, ids).Scan(&counts).Error; err != nil {
		return Listed[Role]{}, err
	}
	for i := range listed.Rows {
		for _, c := range counts {
			if c.RoleID == listed.Rows[i].ID {
				listed.Rows[i].Users = c.Users
			}
		}
	}
	return listed, nil
}

// Role returns the role of id.
func (s *Store) Role(ctx context.Context, id int64) (Role, error) {
	var role Role
	err := s.conn(ctx).Preload("Permissions").First(&role, id).Error
	return role, notFound(err, fmt.Sprintf("o papel %d", id))
}

// AdminRole returns the fixed role of the administrators, created by the
// seeds.
func (s *Store) AdminRole(ctx context.Context) (Role, error) {
	var role Role
	err := s.conn(ctx).Where("system = ?", RoleAdmin).First(&role).Error
	return role, notSeeded(err, "o papel Administrador")
}

// CreateRole creates a role, of the name, the description and the
// permissions of change, by change.By.
func (s *Store) CreateRole(ctx context.Context, change RoleChange) (Role, error) {
	if change.Name == nil {
		return Role{}, fmt.Errorf("%w: informe o nome do papel", ErrInvalid)
	}
	name, err := roleName(*change.Name)
	if err != nil {
		return Role{}, err
	}
	role := Role{Name: name, UpdatedBy: change.By}
	if change.Description != nil {
		if role.Description, err = roleDescription(*change.Description); err != nil {
			return Role{}, err
		}
	}
	permissions := []string{}
	if change.Permissions != nil {
		if permissions, err = rolePermissions(*change.Permissions); err != nil {
			return Role{}, err
		}
	}
	err = s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.lockAccess(ctx); err != nil {
			return err
		}
		if err := s.conn(ctx).Omit("Permissions").Create(&role).Error; err != nil {
			return notFound(err, "o papel "+name)
		}
		return s.setRolePermissions(ctx, role.ID, permissions)
	})
	if err != nil {
		return Role{}, err
	}
	return s.Role(ctx, role.ID)
}

// UpdateRole changes the role of id, of the fields of change set, by
// change.By: not the fixed one; its permissions refused when the change leaves
// nobody to manage the users and the roles (see guardAdmins).
func (s *Store) UpdateRole(ctx context.Context, id int64, change RoleChange) (Role, error) {
	columns := map[string]interface{}{"updated_by": change.By, "updated_at": time.Now()}
	if change.Name != nil {
		name, err := roleName(*change.Name)
		if err != nil {
			return Role{}, err
		}
		columns["name"] = name
	}
	if change.Description != nil {
		description, err := roleDescription(*change.Description)
		if err != nil {
			return Role{}, err
		}
		columns["description"] = description
	}
	var permissions []string
	if change.Permissions != nil {
		var err error
		if permissions, err = rolePermissions(*change.Permissions); err != nil {
			return Role{}, err
		}
	}
	err := s.guardAdmins(ctx, func(ctx context.Context) error {
		var role Role
		if err := s.conn(ctx).Clauses(lockForUpdate()).First(&role, id).Error; err != nil {
			return notFound(err, fmt.Sprintf("o papel %d", id))
		}
		if role.Fixed() {
			return fmt.Errorf("%w: o papel %s é fixo: não pode ser alterado", ErrInvalid, role.Name)
		}
		if err := s.conn(ctx).Model(&Role{}).Where("id = ?", id).Updates(columns).Error; err != nil {
			return notFound(err, "o papel "+role.Name)
		}
		if change.Permissions == nil {
			return nil
		}
		return s.setRolePermissions(ctx, id, permissions)
	})
	if err != nil {
		return Role{}, err
	}
	return s.Role(ctx, id)
}

// setRolePermissions replaces the permissions of the role of id.
func (s *Store) setRolePermissions(ctx context.Context, id int64, permissions []string) error {
	if err := s.conn(ctx).Where("role_id = ?", id).Delete(&RolePermission{}).Error; err != nil {
		return err
	}
	if len(permissions) == 0 {
		return nil
	}
	rows := make([]RolePermission, 0, len(permissions))
	for _, p := range permissions {
		rows = append(rows, RolePermission{RoleID: id, Permission: p})
	}
	return s.conn(ctx).Create(&rows).Error
}

// DeleteRole deletes the role of id: not the fixed one, nor one some users
// hold (their role changed first).
func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	return s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.lockAccess(ctx); err != nil {
			return err
		}
		role, err := s.Role(ctx, id)
		if err != nil {
			return err
		}
		if role.Fixed() {
			return fmt.Errorf("%w: o papel %s é fixo: não pode ser excluído", ErrInvalid, role.Name)
		}
		var holders int64
		if err := s.conn(ctx).Raw(`
			SELECT count(*) FROM user_roles ur JOIN users u ON u.id = ur.user_id AND u.deleted_at IS NULL
			WHERE ur.role_id = ?`, id).Scan(&holders).Error; err != nil {
			return err
		}
		if holders > 0 {
			return fmt.Errorf("%w: o papel %s tem %d usuário(s): troque o papel deles antes de excluí-lo",
				ErrConflict, role.Name, holders)
		}
		// the links of the users deleted before, kept by none
		if err := s.conn(ctx).Where("role_id = ?", id).Delete(&UserRole{}).Error; err != nil {
			return err
		}
		return s.conn(ctx).Delete(&Role{}, id).Error
	})
}

// SetUserRole gives the role of roleID to the user of userID, by by; 0 takes
// its role away, the user then refused at the login. Refused when it leaves
// nobody to manage the users and the roles, see guardAdmins.
func (s *Store) SetUserRole(ctx context.Context, userID, roleID int64, by string) error {
	return s.guardAdmins(ctx, func(ctx context.Context) error {
		if _, err := s.User(ctx, userID); err != nil {
			return err
		}
		if roleID == 0 {
			return s.conn(ctx).Where("user_id = ?", userID).Delete(&UserRole{}).Error
		}
		if _, err := s.Role(ctx, roleID); err != nil {
			return err
		}
		link := UserRole{UserID: userID, RoleID: roleID, UpdatedBy: by, UpdatedAt: time.Now()}
		return s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"role_id", "updated_by", "updated_at"})}).
			Omit("Role").Create(&link).Error
	})
}

// MigrateUserRoles gives the fixed role of the administrators to every user of
// a database whose users hold no role yet, the dashboard having had no roles
// before: once, the users keeping the access they had; and returns how many.
// Afterwards a role is always held (see guardAdmins), so it does nothing.
func (s *Store) MigrateUserRoles(ctx context.Context) (int64, error) {
	var assigned int64
	err := s.Transaction(ctx, func(ctx context.Context) error {
		if err := s.lockAccess(ctx); err != nil {
			return err
		}
		var held, users int64
		if err := s.conn(ctx).Model(&UserRole{}).Count(&held).Error; err != nil || held > 0 {
			return err
		}
		if err := s.conn(ctx).Model(&User{}).Count(&users).Error; err != nil || users == 0 {
			return err
		}
		admin, err := s.AdminRole(ctx)
		if err != nil {
			return err
		}
		result := s.conn(ctx).Exec(`INSERT INTO user_roles (user_id, role_id, updated_by)
			SELECT id, ?, 'migrator' FROM users WHERE deleted_at IS NULL`, admin.ID)
		assigned = result.RowsAffected
		return result.Error
	})
	return assigned, err
}
