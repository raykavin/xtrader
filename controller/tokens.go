package controller

import (
	"crypto/sha256"
	"crypto/subtle"
	"strconv"
	"strings"
	"sync"
)

// tokenPrefix starts the tokens of the agents, see tokens.
const tokenPrefix = "xta_"

// tokens are the tokens of the internal API of the agents: one issued at each
// start of an agent, for that start only, revoked once it exited. A token is
// xta_<the ID of the agent>_<32 random bytes>: only its hash is kept, in
// memory, so a restart of the controller revokes every token, the agents of a
// previous one refused (401) and then stopped.
type tokens struct {
	mu     sync.RWMutex
	hashes map[int64][][32]byte // by the ID of the agent
}

func newTokens() *tokens {
	return &tokens{hashes: make(map[int64][][32]byte)}
}

// issue issues a token for the agent of id.
func (t *tokens) issue(id int64) (string, error) {
	secret, err := randomToken()
	if err != nil {
		return "", err
	}
	token := tokenPrefix + strconv.FormatInt(id, 10) + "_" + secret
	t.mu.Lock()
	t.hashes[id] = append(t.hashes[id], sha256.Sum256([]byte(token)))
	t.mu.Unlock()
	return token, nil
}

// revoke revokes token.
func (t *tokens) revoke(token string) {
	id, ok := tokenAgent(token)
	if !ok {
		return
	}
	sum := sha256.Sum256([]byte(token))
	t.mu.Lock()
	defer t.mu.Unlock()
	kept := t.hashes[id][:0]
	for _, hash := range t.hashes[id] {
		if hash != sum {
			kept = append(kept, hash)
		}
	}
	if len(kept) == 0 {
		delete(t.hashes, id)
		return
	}
	t.hashes[id] = kept
}

// verify returns the ID of the agent of token, false for a token unknown or
// revoked: its hash compared in a constant time.
func (t *tokens) verify(token string) (int64, bool) {
	id, ok := tokenAgent(token)
	if !ok {
		return 0, false
	}
	sum := sha256.Sum256([]byte(token))
	t.mu.RLock()
	defer t.mu.RUnlock()
	found := 0
	for _, hash := range t.hashes[id] {
		found |= subtle.ConstantTimeCompare(hash[:], sum[:])
	}
	return id, found == 1
}

// tokenAgent returns the ID of the agent a token names.
func tokenAgent(token string) (int64, bool) {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok {
		return 0, false
	}
	value, _, ok := strings.Cut(rest, "_")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}
