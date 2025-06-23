package store

import (
	"fmt"
	"strings"
)

// Secret is a secret to write, see SetAgentSecrets and SetTelegram: nil keeps
// the one kept, a pointer to "" removes it, any other value replaces it.
type Secret = *string

// seal encrypts a secret for its column: nil for none.
func (s *Store) seal(secret string) ([]byte, error) {
	if secret = strings.TrimSpace(secret); secret == "" {
		return nil, nil
	}
	sealed, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		return nil, fmt.Errorf("store: a secret: %w", err)
	}
	return sealed, nil
}

// open decrypts the secret of a column: "" for none. A secret encrypted with
// another key fails: the one of config.yml changed, the secrets must be set
// again.
func (s *Store) open(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	secret, err := s.cipher.Decrypt(sealed)
	if err != nil {
		return "", fmt.Errorf("store: a secret, encrypted with another key than the secrets_key of config.yml: %w",
			err)
	}
	return string(secret), nil
}
