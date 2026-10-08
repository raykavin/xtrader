package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/raykavin/xtrader/model"
	"github.com/raykavin/xtrader/ui"
)

// LegacyImported reports whether the file of the previous version source was
// imported already, see MarkLegacyImported.
func (s *Store) LegacyImported(ctx context.Context, source string) (bool, error) {
	var imported LegacyImport
	err := s.conn(ctx).Where("source = ?", source).First(&imported).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

// LegacyImportOf returns the record of the import of source, see
// MarkLegacyImported: ErrNotFound when it wasn't imported.
func (s *Store) LegacyImportOf(ctx context.Context, source string) (LegacyImport, error) {
	var imported LegacyImport
	err := s.conn(ctx).Where("source = ?", source).First(&imported).Error
	return imported, notFound(err, "the import of "+source)
}

// MarkLegacyImported keeps that the file source was imported, with details,
// eg: the counts of its records.
func (s *Store) MarkLegacyImported(ctx context.Context, source string, details Params) error {
	return s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{"details", "imported_at"})}).
		Create(&LegacyImport{Source: source, Details: details}).Error
}

// AgentDeleted reports whether an agent of name was deleted, see DeleteAgent:
// its name is free again, but an import never brings it back.
func (s *Store) AgentDeleted(ctx context.Context, name string) (bool, error) {
	var count int64
	err := s.conn(ctx).Unscoped().Model(&Agent{}).Where("name = ? AND deleted_at IS NOT NULL", name).
		Count(&count).Error
	return count > 0, err
}

// UserDeleted reports whether a user of username, in any case, was deleted,
// see DeleteUser.
func (s *Store) UserDeleted(ctx context.Context, username string) (bool, error) {
	var count int64
	err := s.conn(ctx).Unscoped().Model(&User{}).
		Where("lower(username) = lower(?) AND deleted_at IS NOT NULL", strings.TrimSpace(username)).
		Count(&count).Error
	return count > 0, err
}

// TelegramConfigured reports whether the Telegram bot was configured: its
// token set, the bot on, or a user authorized, even revoked since.
func (s *Store) TelegramConfigured(ctx context.Context) (bool, error) {
	settings, hasToken, err := s.Telegram(ctx)
	if err != nil || hasToken || settings.Enabled {
		return err == nil, err
	}
	var count int64
	err = s.conn(ctx).Unscoped().Model(&TelegramUser{}).Count(&count).Error
	return count > 0, err
}

// ImportedByAnOlderVersion reports whether a version before the imports kept
// what they took from the env and agents.yml (see MarkLegacyImported)
// imported data files: their records tell the agent of a file, never its ID.
// Such an import also wrote Telegram, from TELEGRAM_TOKEN and TELEGRAM_USER
// of its env, keeping no record of it.
func (s *Store) ImportedByAnOlderVersion(ctx context.Context) (bool, error) {
	var count int64
	err := s.conn(ctx).Model(&LegacyImport{}).Where("details->>'agent_id' IS NULL AND source NOT LIKE 'agent:%' " +
		"AND source NOT LIKE 'user:%' AND source NOT LIKE 'env:%'").Count(&count).Error
	return count > 0, err
}

// OrderIDs are the order IDs an agent used: the number of its orders, the
// highest ID among them, and its next one, see LeaseOrderIDs.
type OrderIDs struct {
	Orders int64
	Max    int64
	Next   int64
}

// Used reports whether the agent used order IDs: an order kept, or IDs
// leased, whose orders may come later.
func (ids OrderIDs) Used() bool {
	return ids.Orders > 0 || ids.Next > 1
}

// LockOrderIDs returns the order IDs the agent of id used, the agent locked
// until the end of the transaction of ctx: no ID leased meanwhile.
func (s *Store) LockOrderIDs(ctx context.Context, id int64) (OrderIDs, error) {
	var agent Agent
	err := s.conn(ctx).Clauses(lockForUpdate()).Select("id", "order_id_next").First(&agent, id).Error
	if err != nil {
		return OrderIDs{}, notFound(err, "agent")
	}
	var ids OrderIDs
	err = s.conn(ctx).Model(&orderRow{}).Select("count(*) AS orders, COALESCE(max(id), 0) AS max").
		Where("agent_id = ?", id).Scan(&ids).Error
	ids.Next = agent.OrderIDNext
	return ids, err
}

// LeasedIDs are the order IDs leased to an agent since an import of its
// data, see ImportOrders: past After, the last ID of that import, below
// Next, its next order ID; none when Next is 0.
type LeasedIDs struct {
	After int64
	Next  int64
}

// Holds reports whether the order ID id is one of ids.
func (ids LeasedIDs) Holds(id int64) bool {
	return id > ids.After && id < ids.Next
}

// OrdersImport is what ImportOrders did.
type OrdersImport struct {
	Written   int64   // the orders written
	Kept      int64   // the ones kept already, left as they are
	KeptLast  int64   // the highest ID of the ones kept already
	Conflicts []int64 // the IDs held by other orders of the agent
	Leased    []int64 // the IDs of missing orders leased to the agent, see LeasedIDs
}

// ImportOrders writes the orders of an import to the agent of id: the
// missing ones, an order kept already (the same exchange ID, pair, side, type
// and creation) left as it is, as the agent may have changed it since. An ID
// held by another order is a conflict, and so is a missing order at an ID
// leased to the agent (see LeasedIDs: past the orders kept already too),
// whose order may be on its way, eg: in the outbox of the agent: then nothing
// is written.
func (s *Store) ImportOrders(ctx context.Context, id int64, orders []model.Order, leased LeasedIDs) (OrdersImport,
	error) {
	var result OrdersImport
	ids := make([]int64, 0, len(orders))
	for _, o := range orders {
		ids = append(ids, o.ID)
	}
	kept := make(map[int64]orderRow)
	for chunk := range slices.Chunk(ids, 1000) {
		var rows []orderRow
		if err := s.conn(ctx).Where("agent_id = ? AND id IN ?", id, chunk).Find(&rows).Error; err != nil {
			return result, err
		}
		for _, row := range rows {
			kept[row.ID] = row
		}
	}
	rows := make([]orderRow, 0, len(orders))
	for _, o := range orders {
		row := rowOfOrder(id, o)
		existing, ok := kept[o.ID]
		switch {
		case !ok:
			rows = append(rows, row)
		case sameOrder(existing, row):
			result.Kept++
			result.KeptLast = max(result.KeptLast, o.ID)
		default:
			result.Conflicts = append(result.Conflicts, o.ID)
		}
	}
	// the orders kept already were imported then: their IDs never leased
	leased.After = max(leased.After, result.KeptLast)
	for _, row := range rows {
		if leased.Holds(row.ID) {
			result.Leased = append(result.Leased, row.ID)
		}
	}
	if len(result.Conflicts) > 0 || len(result.Leased) > 0 || len(rows) == 0 {
		return result, nil
	}
	created := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}, {Name: "id"}},
		DoNothing: true}).CreateInBatches(&rows, 500)
	result.Written = created.RowsAffected
	return result, created.Error
}

// sameOrder reports whether two rows are the same order, of the same exchange
// ID, pair, side, type and creation: its status, price and profit change.
func sameOrder(a, b orderRow) bool {
	apart := a.CreatedAt.Sub(b.CreatedAt).Abs()
	return a.ExchangeID == b.ExchangeID && a.Pair == b.Pair && a.Side == b.Side && a.Type == b.Type &&
		apart < time.Microsecond // the database keeps microseconds
}

// DecisionImport is what ImportDecision wrote.
type DecisionImport struct {
	Written     bool  // the decision, false when kept already
	Links       int64 // the links to its orders
	Attachments int64
}

// ImportDecision keeps a decision of an import of the agent of id with its
// attachments and the links of its orders, see SaveDecision: a decision kept
// already is left as it is, without links nor attachments added.
func (s *Store) ImportDecision(ctx context.Context, id int64, d ui.Decision, attachments []Attachment) (
	DecisionImport, error) {
	var result DecisionImport
	row := rowOfDecision(id, d)
	err := s.Transaction(ctx, func(ctx context.Context) error {
		created := s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "agent_id"}, {Name: "id"}},
			DoNothing: true}).Create(&row)
		if created.Error != nil || created.RowsAffected == 0 {
			return created.Error
		}
		result.Written = true
		var err error
		result.Links, result.Attachments, err = s.saveDecisionParts(ctx, id, d, attachments)
		return err
	})
	return result, err
}

// ImportJournal appends the entries of the journal of an import of the agent
// of id, the ones kept already left as they are, and returns how many it
// wrote.
func (s *Store) ImportJournal(ctx context.Context, id int64, entries []JournalEntry) (int64, error) {
	return s.appendJournal(ctx, id, entries)
}
