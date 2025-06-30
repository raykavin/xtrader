package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// MarkLegacyImported keeps that the file source was imported, with details,
// eg: the counts of its records.
func (s *Store) MarkLegacyImported(ctx context.Context, source string, details Params) error {
	return s.conn(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{"details", "imported_at"})}).
		Create(&LegacyImport{Source: source, Details: details}).Error
}
