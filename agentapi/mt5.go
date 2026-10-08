package agentapi

import (
	"fmt"
	"hash/fnv"
	"maps"
	"math"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/raykavin/xtrader/exchange"
)

// DefaultPair is the pair an agent trades without -pair.
const DefaultPair = "XAUUSDm"

// MT5MaxComment is the longest comment of an order MT5 keeps.
const MT5MaxComment = 31

// defaultStrategies are the default strategies of the agents of the runner
// by kind (agents/internal/runner), the first one of the binary of each kind,
// eg: agents/trendagent: they name the comments of their orders on MT5.
var defaultStrategies = map[string]string{"trend": "trendconfluence", "momentum": "adxema",
	"breakout": "atrbreakout", "intraday": "orb"}

// DefaultStrategy returns the strategy an agent of kind trades without
// -strategy, "" for an LLM agent or a kind unknown.
func DefaultStrategy(kind string) string {
	return defaultStrategies[kind]
}

// MT5Comment returns the comment of the orders of an agent on MT5, the one
// its magic number is a hash of by default (see MT5Magic): comment, trimmed,
// when set (-mt5-comment), else "nb:" followed by name, its strategy or llm
// for an LLM agent, cut to MT5MaxComment. The agents, the controller and the
// store compute it alike, so that the store refuses two agents trading the
// same positions.
func MT5Comment(comment, name string) string {
	if comment = strings.TrimSpace(comment); comment != "" {
		return comment
	}
	comment = "nb:" + name
	if len(comment) > MT5MaxComment {
		comment = comment[:MT5MaxComment]
	}
	return comment
}

// MT5Magic returns the magic number of the orders and positions of an agent
// on MT5: magic when above 0 (-mt5-magic), else a stable hash of its comment
// (see MT5Comment), a positive int32, so that the agent finds its positions
// again after a restart, and two agents of comments of their own keep theirs
// apart.
func MT5Magic(magic int64, comment string) int64 {
	if magic > 0 {
		return magic
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(comment))
	if value := int64(hash.Sum32() & math.MaxInt32); value > 0 {
		return value
	}
	return 1
}

// MT5Book is what an agent trades on MT5: the bridge of the terminal, the
// magic number of its orders and positions, the comment it is a hash of by
// default, and the pairs. Two agents of overlapping books (see Overlaps) would
// trade the positions of each other.
type MT5Book struct {
	Bridge  string
	Magic   int64
	Comment string
	Pairs   []string
}

// NewMT5Book returns the book of an agent of kind as the agent reads it from
// its command line (see remote.Agent.Args): the fields of its account, nil for
// none, over its params (its flags by name, the ones of its language model
// included), over the defaults of the flags: the bridge
// exchange.MT5DefaultBaseURL, the comment of MT5Comment, of the param strategy
// else of the strategy of the kind (llm for an LLM agent), its hash for the
// magic number (see MT5Magic), and the pairs of the param pair, else
// DefaultPair.
func NewMT5Book(kind string, params map[string]string, account *Account) MT5Book {
	bridge, ok := params["mt5-bridge"]
	if !ok {
		bridge = exchange.MT5DefaultBaseURL
	}
	comment := params["mt5-comment"]
	var magic int64
	if value, ok := params["mt5-magic"]; ok {
		magic, _ = strconv.ParseInt(value, 0, 64) // as flag.Int64, the agent refusing a value it can't read
	}
	if account != nil {
		if account.BridgeURL != "" {
			bridge = account.BridgeURL
		}
		if account.Magic != nil {
			magic = *account.Magic
		}
		if account.Comment != nil {
			comment = *account.Comment
		}
	}
	name := "llm"
	if kind != "llm" {
		name = DefaultStrategy(kind)
		if strategy, ok := params["strategy"]; ok {
			name = strategy
		}
	}
	pairs, ok := params["pair"]
	if !ok {
		pairs = DefaultPair
	}
	comment = MT5Comment(comment, name)
	return MT5Book{Bridge: bridge, Magic: MT5Magic(magic, comment), Comment: comment, Pairs: MT5Pairs(pairs)}
}

// MT5Book returns the book of the agent of c, see NewMT5Book.
func (c Config) MT5Book() MT5Book {
	params := maps.Clone(c.Params)
	if params == nil {
		params = map[string]string{}
	}
	if c.LLM != nil {
		maps.Copy(params, c.LLM.Params)
	}
	return NewMT5Book(c.Kind, params, c.Account)
}

// CheckMT5Book fails when book, the one the agent of c trades as it read its
// flags, isn't the one of its settings (see Config.MT5Book), the one the store
// and the controller keep apart from the books of the other agents: the agent
// would trade positions they don't know of.
func (c Config) CheckMT5Book(book MT5Book) error {
	want := c.MT5Book()
	if book.Bridge == want.Bridge && book.Magic == want.Magic && book.Comment == want.Comment &&
		slices.Equal(book.Pairs, want.Pairs) {
		return nil
	}
	return fmt.Errorf("the agent %s trades the bridge %s, the magic number %d (comment %q) and %s, but its "+
		"settings give the bridge %s, the magic number %d (comment %q) and %s, the ones the controller keeps "+
		"apart from the other agents: refused", c.Name, book.Bridge, book.Magic, book.Comment,
		strings.Join(book.Pairs, ";"), want.Bridge, want.Magic, want.Comment, strings.Join(want.Pairs, ";"))
}

// MT5Pairs returns the pairs of -pair, separated by ; or by ,, as the agents
// read them.
func MT5Pairs(value string) []string {
	var pairs []string
	for _, pair := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' }) {
		if pair = strings.TrimSpace(pair); pair != "" {
			pairs = append(pairs, pair)
		}
	}
	return pairs
}

// Overlaps reports whether the books b and o share positions: the same
// bridge (see SameMT5Bridge), the same magic number and a pair in common, case
// aside, or no pair known.
func (b MT5Book) Overlaps(o MT5Book) bool {
	unknown := len(b.Pairs) == 0 || len(o.Pairs) == 0
	return b.Magic == o.Magic && SameMT5Bridge(b.Bridge, o.Bridge) && (unknown || len(b.Common(o)) > 0)
}

// Common returns the pairs of b that o trades too, case aside, the ones of
// both when either has none known.
func (b MT5Book) Common(o MT5Book) []string {
	if len(b.Pairs) == 0 || len(o.Pairs) == 0 {
		return append(slices.Clone(b.Pairs), o.Pairs...)
	}
	var common []string
	for _, pair := range b.Pairs {
		if slices.ContainsFunc(o.Pairs, func(other string) bool { return strings.EqualFold(pair, other) }) {
			common = append(common, pair)
		}
	}
	return common
}

// SameMT5Bridge reports whether the URLs a and b reach the same bridge, a
// process listening at a host and a port: the same host, case aside, or two
// names of the loopback, and the same port, the one of the scheme when
// missing, whatever their paths. Two names of a host, eg: a name and its
// address, can't be told apart.
func SameMT5Bridge(a, b string) bool {
	return bridgeKey(a) == bridgeKey(b)
}

// bridgeKey returns the host and the port of the bridge at the URL raw, the
// URL itself, trimmed and in lowercase, when it has no host.
func bridgeKey(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	if ip := net.ParseIP(host); host == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}
