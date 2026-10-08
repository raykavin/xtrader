package store

import (
	"bytes"
	"fmt"
	"strings"
)

// Secret is a secret to write, see SetAgentSecrets and SetTelegram: nil keeps
// the one kept, a pointer to "" removes it, any other value replaces it.
type Secret = *string

// sealedV1 starts the text a secret is encrypted in, the place it is sealed
// for after it (see placeOf) then a NUL, then the secret: the leading NUL
// tells it from a secret sealed alone, before the places, never starting
// with one; a version of xtrader before reads it as a secret its requests
// refuse, never as a valid one.
const sealedV1 = "\x00xtrader-secret-v1\x00"

// placeOf is the place of a secret: its table, column and row, eg:
// agent_llm_settings:api_key:3. A secret is sealed for its place: copied to
// another one, it doesn't open.
func placeOf(table, column string, id int64) string {
	return fmt.Sprintf("%s:%s:%d", table, column, id)
}

// bridgeTokenOf is the place of the token of the bridge of the agent of
// agentID.
func bridgeTokenOf(agentID int64) string {
	return placeOf("agent_accounts", "bridge_token", agentID)
}

// llmAPIKeyOf is the place of the key of the language model of the agent of
// agentID.
func llmAPIKeyOf(agentID int64) string {
	return placeOf("agent_llm_settings", "api_key", agentID)
}

// calendarTokenOf is the place of the token of the calendar of the agent of
// agentID.
func calendarTokenOf(agentID int64) string {
	return placeOf("agent_llm_settings", "calendar_token", agentID)
}

// telegramToken is the place of the token of the Telegram bot.
func telegramToken() string {
	return placeOf("telegram_settings", "token", 1)
}

// seal encrypts a secret for its place: nil for none.
func (s *Store) seal(place, secret string) ([]byte, error) {
	if secret = strings.TrimSpace(secret); secret == "" {
		return nil, nil
	}
	sealed, err := s.cipher.Encrypt([]byte(sealedV1 + place + "\x00" + secret))
	if err != nil {
		return nil, fmt.Errorf("store: a secret: %w", err)
	}
	return sealed, nil
}

// open decrypts the secret of a place: "" for none. A secret encrypted with
// another key fails: the one of config.yml changed, the secrets must be set
// again; so does one sealed for another place.
func (s *Store) open(place string, sealed []byte) (string, error) {
	secret, _, err := s.unseal(place, sealed)
	return secret, err
}

// unseal is open, and reports whether the secret is sealed for its place: one
// sealed alone, by a version before the places, opens too, sealed for its
// place the next time it is written.
func (s *Store) unseal(place string, sealed []byte) (secret string, placed bool, err error) {
	if len(sealed) == 0 {
		return "", true, nil
	}
	text, err := s.cipher.Decrypt(sealed)
	if err != nil {
		return "", false, fmt.Errorf("store: a secret, encrypted with another key than the secrets_key of config.yml: %w",
			err)
	}
	rest, ok := bytes.CutPrefix(text, []byte(sealedV1))
	switch {
	case !ok && bytes.HasPrefix(text, []byte{0}):
		return "", false, fmt.Errorf("store: the secret of %s, sealed by a later version of xtrader", place)
	case !ok:
		return string(text), false, nil
	}
	sealedFor, value, found := bytes.Cut(rest, []byte{0})
	if !found || string(sealedFor) != place {
		return "", false, fmt.Errorf("store: the secret of %s, sealed for another place (%q): set it again", place,
			sealedFor)
	}
	return string(value), true, nil
}
