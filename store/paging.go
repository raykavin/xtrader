package store

import (
	"fmt"
	"math"
	"strings"

	"github.com/raykavin/gobox/pagination"
	"gorm.io/gorm"
)

// ListQuery is a list the dashboard asks for: a page of it (Page.Number 0 for
// all of it, as before the pages), of the rows matching Search (see
// searchScope), in the order of the list.
type ListQuery struct {
	Page   pagination.Page
	Search string
}

// Paged reports whether q asks for a page of the list.
func (q ListQuery) Paged() bool { return q.Page.Number > 0 }

// Listed is a page of a list: its rows, the page they are (the last one for a
// page past it; the whole list as one page when not paged), and the rows of
// the list matching its search (Total).
type Listed[T any] struct {
	Rows  []T
	Page  pagination.Page
	Total int64
}

// accented and plain are the letters a search folds, the one of accented to
// the one of plain at its place, in Go (see fold) as in the database (see
// foldSQL, translate).
const (
	accented = "áàâãäåéèêëíìîïóòôõöúùûüçñýÿÁÀÂÃÄÅÉÈÊËÍÌÎÏÓÒÔÕÖÚÙÛÜÇÑÝ"
	plain    = "aaaaaaeeeeiiiiooooouuuucnyyAAAAAAEEEEIIIIOOOOOUUUUCNY"
)

// plainOf maps each letter of accented to its letter of plain.
var plainOf = func() map[rune]rune {
	from, to := []rune(accented), []rune(plain)
	if len(from) != len(to) {
		panic("store: the letters of a search folded don't pair")
	}
	m := make(map[rune]rune, len(from))
	for i, r := range from {
		m[r] = to[i]
	}
	return m
}()

// fold returns text as a search compares it: without its accents, in
// lowercase.
func fold(text string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if p, ok := plainOf[r]; ok {
			return p
		}
		return r
	}, text))
}

// foldSQL returns the SQL of the text of expr folded as fold does.
func foldSQL(expr string) string {
	return fmt.Sprintf("lower(translate(%s, '%s', '%s'))", expr, accented, plain)
}

// searchWords returns the words of search, folded: every one of them in a row
// matching it.
func searchWords(search string) []string {
	return strings.Fields(fold(search))
}

// A searchField returns the condition of a row holding word (a word of a
// search, folded) and its arguments.
type searchField func(word string) (string, []any)

// textField matches the word in the text of expr, a column or an expression
// of the row, eg: name or CAST(login AS TEXT), the case and the accents
// ignored, the wildcards of LIKE in the word taken as they are.
func textField(expr string) searchField {
	return func(word string) (string, []any) {
		return foldSQL(expr) + " LIKE ? " + pagination.LikeEscapeClause,
			[]any{"%" + pagination.EscapeLike(word) + "%"}
	}
}

// relatedField matches the word in the text expr of the rows of another table
// exists tells, its %s the match, eg: EXISTS (SELECT 1 FROM agents a WHERE
// a.id = x.agent_id AND %s) with a.name.
func relatedField(exists, expr string) searchField {
	text := textField(expr)
	return func(word string) (string, []any) {
		match, args := text(word)
		return fmt.Sprintf(exists, match), args
	}
}

// searchScope keeps the rows holding every word of search, each in one of
// fields at least, as the search of the agents on the dashboard: all the rows
// for a blank search.
func searchScope(search string, fields ...searchField) func(*gorm.DB) *gorm.DB {
	words := searchWords(search)
	return func(db *gorm.DB) *gorm.DB {
		for _, word := range words {
			matches := make([]string, 0, len(fields))
			var args []any
			for _, field := range fields {
				match, more := field(word)
				matches = append(matches, match)
				args = append(args, more...)
			}
			db = db.Where("("+strings.Join(matches, " OR ")+")", args...)
		}
		return db
	}
}

// list reads the rows of db (its model, search and filters applied) in the
// order of sorts, which ends by a unique column, eg: id, for the pages not to
// overlap: all of them, or the page of q, the last one for a page past it.
func list[T any](db *gorm.DB, q ListQuery, sorts []pagination.Sort) (Listed[T], error) {
	if !q.Paged() {
		ordered := db
		for _, s := range sorts {
			ordered = ordered.Order(s.Field + " " + string(s.Direction))
		}
		rows := []T{}
		if err := ordered.Find(&rows).Error; err != nil {
			return Listed[T]{}, err
		}
		return Listed[T]{Rows: rows, Page: pagination.Page{Number: 1, PerPage: len(rows)},
			Total: int64(len(rows))}, nil
	}
	page := q.Page
	for {
		query := pagination.NewQuery(page, nil, sorts)
		rows := []T{}
		var total int64
		err := db.Session(&gorm.Session{}).Scopes(pagination.Scope(query, &total)).Find(&rows).Error
		if err != nil {
			return Listed[T]{}, err
		}
		last := max(1, int(math.Ceil(float64(total)/float64(query.Page.PerPage))))
		if query.Page.Number > last {
			page = pagination.Page{Number: last, PerPage: query.Page.PerPage} // past the last one
			continue
		}
		return Listed[T]{Rows: rows, Page: query.Page, Total: total}, nil
	}
}

// listedAs returns listed with its rows mapped by as.
func listedAs[T, E any](listed Listed[T], as func(T) E) Listed[E] {
	rows := make([]E, 0, len(listed.Rows))
	for _, row := range listed.Rows {
		rows = append(rows, as(row))
	}
	return Listed[E]{Rows: rows, Page: listed.Page, Total: listed.Total}
}

// byFields returns the sorts of fields, ascending, eg: lower(name), id.
func byFields(fields ...string) []pagination.Sort {
	b := pagination.NewSortBuilder()
	for _, f := range fields {
		b.OrderBy(f, pagination.Asc)
	}
	return b.Build()
}

// newestFirst returns the sorts of fields, descending, eg: created_at, id.
func newestFirst(fields ...string) []pagination.Sort {
	b := pagination.NewSortBuilder()
	for _, f := range fields {
		b.OrderBy(f, pagination.Desc)
	}
	return b.Build()
}
