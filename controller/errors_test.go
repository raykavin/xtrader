package controller

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/raykavin/xtrader/store"
)

func TestUserMessage(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		// a detail of the store as a sentence, its sentinel and what wraps it dropped
		{fmt.Errorf("%w: desligue o agente gold antes", store.ErrInvalid), "Desligue o agente gold antes."},
		{fmt.Errorf("settings: %w", fmt.Errorf("%w: o prompt 9 não existe", store.ErrNotFound)),
			"O prompt 9 não existe."},
		{fmt.Errorf("%w: o agente gold já existe.", store.ErrConflict), "O agente gold já existe."},
		// a sentinel alone, said in Portuguese
		{store.ErrNotFound, "Não encontrado."},
		{store.ErrBacktestBusy, "O lote ainda tem execuções na fila ou em andamento."},
		// news to the user: the sentinel before its detail
		{fmt.Errorf("%w: a ação 7 está succeeded", store.ErrDecided),
			"Essa ação já foi decidida: a ação 7 está succeeded."},
		{store.ErrExpired, "A proposta expirou."},
		// any other error as it is
		{errors.New("O agente gold não responde em localhost:5560."), "O agente gold não responde em localhost:5560."},
	} {
		assert.Equal(t, tc.want, userMessage(tc.err), "%v", tc.err)
	}
}

func TestSentence(t *testing.T) {
	assert.Equal(t, "Ótimo.", sentence(" ótimo "))
	assert.Equal(t, "Continuar?", sentence("continuar?"))
	assert.Equal(t, "Aguarde…", sentence("aguarde…"))
	assert.Equal(t, "", sentence(" "))
}
