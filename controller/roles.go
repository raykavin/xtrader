package controller

import (
	"net/http"

	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// roleInfo returns the role r as the dashboard shows it: the permissions it
// grants as set, every one for the fixed role.
func roleInfo(r store.Role) ui.RoleInfo {
	return ui.RoleInfo{ID: r.ID, Name: r.Name, Description: r.Description, Fixed: r.Fixed(),
		Permissions: r.Granted(), Users: r.Users, UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt.Unix()}
}

// roleChange returns the change of a role of request, by the user of the
// request.
func roleChange(r *http.Request, request ui.RoleRequest) store.RoleChange {
	return store.RoleChange{Name: request.Name, Description: request.Description, Permissions: request.Permissions,
		By: userOf(r.Context())}
}

// handleRoles lists the roles of the users of the dashboard (the page and the
// search asked for, see listQuery) and the catalog of the permissions.
func (s *settings) handleRoles(w http.ResponseWriter, r *http.Request) {
	q, ok := listQuery(w, r)
	if !ok {
		return
	}
	roles, err := s.store.FindRoles(r.Context(), q)
	if err != nil {
		s.fail(w, err)
		return
	}
	list := make([]ui.RoleInfo, 0, len(roles.Rows))
	for _, role := range roles.Rows {
		list = append(list, roleInfo(role))
	}
	writeJSON(w, ui.RolesResponse{PageInfo: pageInfo(roles), Roles: list, Areas: ui.PermissionAreas()})
}

// handleCreateRole creates a role, of its name and its permissions.
func (s *settings) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var request ui.RoleRequest
	if !decode(w, r, &request) {
		return
	}
	role, err := s.store.CreateRole(r.Context(), roleChange(r, request))
	if err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the role %s created by %s, granting %v", role.Name, userOf(r.Context()), role.Granted())
	writeCreated(w, roleInfo(role))
}

// handleUpdateRole changes a role, of the fields set, not the fixed one: the
// sessions of its users take its permissions at their next request.
func (s *settings) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	var request ui.RoleRequest
	if !decode(w, r, &request) {
		return
	}
	role, err := s.store.UpdateRole(r.Context(), id, roleChange(r, request))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auth().refreshUsers()
	log.Infof("ui: the role %s changed by %s, granting %v", role.Name, userOf(r.Context()), role.Granted())
	writeJSON(w, roleInfo(role))
}

// handleDeleteRole deletes a role no user holds, not the fixed one.
func (s *settings) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "id")
	if !ok {
		return
	}
	role, err := s.store.Role(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.store.DeleteRole(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	log.Infof("ui: the role %s deleted by %s", role.Name, userOf(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}
