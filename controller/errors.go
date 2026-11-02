package controller

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/raykavin/xtrader/store"
)

// storeSentinels are the errors of the store whose text leads their details
// ("store: invalid: …"), with what the dashboard says in place of it: alone,
// without a detail; before the detail, when the sentinel is news to the user
// (an action decided, a proposal expired).
var storeSentinels = []struct {
	err   error
	alone string
	lead  string
}{
	{err: store.ErrInvalid, alone: "Valor inválido"},
	{err: store.ErrNotFound, alone: "Não encontrado"},
	{err: store.ErrConflict, alone: "Já existe"},
	{err: store.ErrDecided, alone: "Essa ação já foi decidida", lead: "Essa ação já foi decidida"},
	{err: store.ErrExpired, alone: "A proposta expirou", lead: "A proposta expirou"},
	{err: store.ErrBacktestBusy, alone: "O lote ainda tem execuções na fila ou em andamento"},
	{err: store.ErrCredentials, alone: "Usuário ou senha inválidos"},
}

// userMessage returns the message of err for the dashboard: an error of the
// store (see storeSentinels) by its detail, without the text of the sentinel
// nor what wraps it, as a sentence; any other error as it is.
func userMessage(err error) string {
	text := err.Error()
	for _, s := range storeSentinels {
		if !errors.Is(err, s.err) {
			continue
		}
		i := strings.Index(text, s.err.Error())
		if i < 0 {
			return text // wrapped without its text: as it is
		}
		detail := strings.TrimLeft(text[i+len(s.err.Error()):], ": ")
		switch {
		case detail == "":
			return s.alone + "."
		case s.lead != "":
			return s.lead + ": " + strings.TrimSuffix(detail, ".") + "."
		}
		return sentence(detail)
	}
	return text
}

// sentence returns text with its first letter capitalized and a final period,
// unless it ends a sentence already.
func sentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return text
	}
	first, size := utf8.DecodeRuneInString(text)
	text = string(unicode.ToUpper(first)) + text[size:]
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "?") && !strings.HasSuffix(text, "!") &&
		!strings.HasSuffix(text, "…") {
		text += "."
	}
	return text
}
