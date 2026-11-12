package controller

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"

	"github.com/raykavin/xtrader/ui"
)

// configSecretValues are the secrets of the items of a configuration file, in
// clear: by the name of the agent and of the account of MT5, and the ones of
// the news, of the XTrader AI and of the bot of Telegram. Never written to a
// disk nor to a log: sealed in the file, see sealConfigSecrets.
type configSecretValues struct {
	Agents      map[string]configAgentSecrets `json:"agents,omitempty"`
	MT5Accounts map[string]configMT5Secrets   `json:"mt5_accounts,omitempty"`
	News        string                        `json:"news,omitempty"`
	Supervisor  string                        `json:"supervisor,omitempty"`
	Telegram    string                        `json:"telegram,omitempty"`
}

// configAgentSecrets are the secrets of an agent: the key of its language
// model and the token of its bridge (none when it trades an account of MT5,
// whose token is the account's).
type configAgentSecrets struct {
	LLMAPIKey   string `json:"llm_api_key,omitempty"`
	BridgeToken string `json:"bridge_token,omitempty"`
}

// configMT5Secrets are the secrets of an account of MT5: its password and the
// token of its bridge.
type configMT5Secrets struct {
	Password    string `json:"password,omitempty"`
	BridgeToken string `json:"bridge_token,omitempty"`
}

// empty reports whether no secret is set.
func (v configSecretValues) empty() bool {
	return len(v.Agents) == 0 && len(v.MT5Accounts) == 0 && v.News == "" && v.Supervisor == "" && v.Telegram == ""
}

// The derivation of the key of the secrets of a configuration file from its
// password, argon2id, and the bounds of the params a file may ask for, so
// that a file can't make the controller spend more than they allow.
const (
	configKDF        = "argon2id"
	configKDFTime    = 3
	configKDFMemory  = 64 * 1024 // KiB
	configKDFThreads = 4
	configKeyLen     = 32
	configSaltLen    = 16

	maxConfigKDFTime   = 10
	maxConfigKDFMemory = 256 * 1024 // KiB
)

// errConfigPassword is the error of a password that doesn't open the secrets
// of a configuration file, or of secrets altered.
var errConfigPassword = errors.New("senha incorreta: os segredos do arquivo não puderam ser abertos")

// configCipher returns the cipher of the secrets of a configuration file, of
// the key derived from password with salt.
func configCipher(password string, salt []byte, time, memory uint32, threads uint8) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(password), salt, time, memory, threads, configKeyLen)
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealConfigSecrets seals the secrets of values with password, see
// ui.ConfigSecrets: the format of the file bound to them.
func sealConfigSecrets(values configSecretValues, password string) (*ui.ConfigSecrets, error) {
	plain, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	salt := make([]byte, configSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	aead, err := configCipher(password, salt, configKDFTime, configKDFMemory, configKDFThreads)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	data := aead.Seal(nil, nonce, plain, []byte(ui.ConfigFormat))
	encode := base64.StdEncoding.EncodeToString
	return &ui.ConfigSecrets{KDF: configKDF, Time: configKDFTime, Memory: configKDFMemory, Threads: configKDFThreads,
		Salt: encode(salt), Nonce: encode(nonce), Data: encode(data)}, nil
}

// openConfigSecrets opens the secrets of sealed with password:
// errConfigPassword for a wrong one (or secrets altered), an error for params
// out of their bounds.
func openConfigSecrets(sealed *ui.ConfigSecrets, password string) (configSecretValues, error) {
	switch {
	case sealed.KDF != configKDF:
		return configSecretValues{}, fmt.Errorf("os segredos do arquivo usam uma derivação desconhecida (%q)", sealed.KDF)
	case sealed.Time < 1 || sealed.Time > maxConfigKDFTime || sealed.Memory < 8*1024 ||
		sealed.Memory > maxConfigKDFMemory || sealed.Threads < 1:
		return configSecretValues{}, errors.New("os parâmetros da cifra dos segredos do arquivo são inválidos")
	}
	decode := base64.StdEncoding.DecodeString
	salt, err := decode(sealed.Salt)
	if err != nil {
		return configSecretValues{}, errConfigPassword
	}
	nonce, err := decode(sealed.Nonce)
	if err != nil {
		return configSecretValues{}, errConfigPassword
	}
	data, err := decode(sealed.Data)
	if err != nil {
		return configSecretValues{}, errConfigPassword
	}
	aead, err := configCipher(password, salt, sealed.Time, sealed.Memory, sealed.Threads)
	if err != nil {
		return configSecretValues{}, err
	}
	if len(nonce) != aead.NonceSize() {
		return configSecretValues{}, errConfigPassword
	}
	plain, err := aead.Open(nil, nonce, data, []byte(ui.ConfigFormat))
	if err != nil {
		return configSecretValues{}, errConfigPassword
	}
	defer clear(plain)
	var values configSecretValues
	if err := json.Unmarshal(plain, &values); err != nil {
		return configSecretValues{}, errConfigPassword
	}
	return values, nil
}
