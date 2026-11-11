package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/raykavin/gobox/pagination"

	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

// listQuery returns the list the request asks for, see ui.PageInfo: ?page=
// (from 1; without it, the whole list), ?per_page= (from 1, ui.MaxPerPage at
// most) and ?q=, its search; it answers 400 for one invalid.
func listQuery(w http.ResponseWriter, r *http.Request) (store.ListQuery, bool) {
	values := r.URL.Query()
	var q store.ListQuery
	if value := values.Get("page"); value != "" {
		page, err := strconv.Atoi(value)
		if err != nil || page < 1 {
			writeJSONError(w, http.StatusBadRequest, "Parâmetro page inválido: use um número a partir de 1.")
			return store.ListQuery{}, false
		}
		q.Page = pagination.Page{Number: page, PerPage: ui.DefPerPage}
	}
	if value := values.Get("per_page"); value != "" {
		size, err := strconv.Atoi(value)
		if err != nil || size < 1 {
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("Parâmetro per_page inválido: use um número de 1 a %d.", ui.MaxPerPage))
			return store.ListQuery{}, false
		}
		q.Page.PerPage = min(size, ui.MaxPerPage)
	}
	q.Search = strings.TrimSpace(values.Get("q"))
	if utf8.RuneCountInString(q.Search) > ui.MaxSearch {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("A pesquisa tem no máximo %d caracteres.", ui.MaxSearch))
		return store.ListQuery{}, false
	}
	return q, true
}

// pageOf returns the page for the dashboard of total rows, see ui.PageInfo: a
// whole list empty without a size.
func pageOf(page pagination.Page, total int64) ui.PageInfo {
	if page.PerPage < 1 {
		return ui.PageInfo{Page: 1}
	}
	result := pagination.NewResult[struct{}](nil, int(total), page)
	return ui.PageInfo{Total: result.Total, Page: result.Page, PerPage: result.PerPage,
		TotalPages: result.TotalPages, HasNext: result.HasNext, HasPrev: result.HasPrev}
}

// pageInfo returns the page of listed for the dashboard, see ui.PageInfo.
func pageInfo[T any](listed store.Listed[T]) ui.PageInfo {
	return pageOf(listed.Page, listed.Total)
}

// wholePage returns n rows as a whole list, one page, see ui.PageInfo.
func wholePage(n int) ui.PageInfo {
	return pageOf(pagination.Page{Number: 1, PerPage: n}, int64(n))
}

// pageSlice returns the rows of q of a list kept in memory (rows, in its
// order, the search done): all of them, or its page, the last one for a page
// past it.
func pageSlice[T any](rows []T, q store.ListQuery) ([]T, ui.PageInfo) {
	if !q.Paged() {
		return rows, wholePage(len(rows))
	}
	page := pagination.NewQuery(q.Page, nil, nil).Page
	last := max(1, (len(rows)+page.PerPage-1)/page.PerPage)
	page.Number = min(page.Number, last)
	start := min(page.Offset(), len(rows))
	end := min(start+page.PerPage, len(rows))
	return rows[start:end], pageOf(page, int64(len(rows)))
}
