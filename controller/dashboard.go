// Package controller runs the bots of xtrader as agents: each bot serves the
// API of its chart (see ui.Chart) and the controller serves them to the users,
// on the web dashboard and on Telegram, reaching the agents through their API.
package controller

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	log "github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/agentapi"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/ui"
)

//go:embed templates/*.html
var templateFiles embed.FS

var (
	indexTemplate = template.Must(template.ParseFS(templateFiles, "templates/index.html"))
	errorTemplate = template.Must(template.ParseFS(templateFiles, "templates/error.html"))

	// agentNameRe keeps the name of an agent a plain segment of the URL path.
	agentNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// probeTimeout is how long the list of agents waits for the API of each one.
const probeTimeout = 2 * time.Second

// Agent is a bot of the controller: Address is where its chart serves the API,
// host:port or an http(s) URL, eg: localhost:5555, and Kind the one of its
// settings, eg: llm.
type Agent struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
	Kind    string `yaml:"kind"`
}

// agent is an Agent with the proxy to its API.
type agent struct {
	Agent
	api   *url.URL
	proxy *httputil.ReverseProxy
}

// Dashboard serves the web dashboard of the agents: the bundle, the list of
// agents and a proxy to the API of each one, so that the browser talks to a
// single server, and the settings of the controller (see WithSettings).
// cmd/controller runs it.
//
// Only the users logged in reach it, the users of the database (see
// WithUsers): without any, it is closed. Any of them can operate the agents
// with the control panel enabled, and change the settings.
type Dashboard struct {
	agents      *registry
	keys        *agentKeys   // of the API of the agents the controller launched
	probe       *http.Client // lists the agents online
	client      *http.Client // calls their API, see call
	stream      *http.Client // follows their events, see events
	bundle      bundleConfig
	auth        *auth
	settings    *settings
	supervision *Supervision // see ServeSupervision
	backtests   *Backtests   // see ServeBacktests
}

// DashboardOption configures a Dashboard.
type DashboardOption func(*Dashboard)

// WithUIDir serves the dashboard from a local directory (the output of
// `bun run build` in web/) instead of downloading the release bundle: ui.dir
// of config.yml.
func WithUIDir(dir string) DashboardOption {
	return func(d *Dashboard) { d.bundle.dir = dir }
}

// WithUIVersion forces the release tag of the bundle to download
// (e.g. "v1.4.0" or "latest"): ui.version of config.yml. By default the
// version of the xtrader module compiled into the binary is used.
func WithUIVersion(version string) DashboardOption {
	return func(d *Dashboard) { d.bundle.version = version }
}

// WithCacheDir overrides where downloaded bundles are stored
// (default: os.UserCacheDir()/xtrader/ui).
func WithCacheDir(dir string) DashboardOption {
	return func(d *Dashboard) { d.bundle.cacheDir = dir }
}

// WithUsers sets the users who log in the dashboard, see store.Store:
// without any, the dashboard is closed.
func WithUsers(users Users) DashboardOption {
	return func(d *Dashboard) { d.auth.users = users }
}

// WithTrustedProxies sets the reverse proxies in front of the dashboard,
// whose X-Forwarded-For and X-Forwarded-Proto are believed: ui.trusted_proxies
// of config.yml, the loopback by default.
func WithTrustedProxies(proxies []netip.Prefix) DashboardOption {
	return func(d *Dashboard) { d.auth.proxies = slices.Clone(proxies) }
}

// WithHTTPClient sets the client used to download the bundle.
func WithHTTPClient(client *http.Client) DashboardOption {
	return func(d *Dashboard) { d.bundle.client = client }
}

// withReleaseURL points the downloader to another host (tests).
func withReleaseURL(url string) DashboardOption {
	return func(d *Dashboard) { d.bundle.releaseURL = url }
}

// NewDashboard creates the dashboard of the agents, each with its own name,
// replaced later with SetAgents. Call Handler to serve it.
func NewDashboard(agents []Agent, options ...DashboardOption) (*Dashboard, error) {
	keys := newAgentKeys()
	d := &Dashboard{
		agents: newRegistry(keys),
		keys:   keys,
		probe:  &http.Client{Timeout: probeTimeout},
		client: &http.Client{Timeout: apiTimeout},
		stream: &http.Client{},
		bundle: defaultBundleConfig(),
		auth:   newAuth(nil),
	}
	if err := d.agents.replace(agents); err != nil {
		return nil, err // the agents of a program checked at once
	}
	for _, option := range options {
		option(d)
	}
	return d, nil
}

// SetAgents makes agents the ones of the dashboard, in their order: the
// invalid ones left out, told by the error.
func (d *Dashboard) SetAgents(agents []Agent) error {
	return d.agents.replace(agents)
}

// agentAPI returns the URL of the API of an agent at address, host:port served
// over HTTP or an http(s) URL, on the loopback only (see ui.LoopbackHost):
// the API of an agent answers whoever reaches it, and the dashboard sends
// nothing to another host.
func agentAPI(address string) (*url.URL, error) {
	if address == "" {
		return nil, errors.New("no address")
	}
	raw := address
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	base, err := url.Parse(raw)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("invalid address %q: host:port or an http(s) URL", address)
	}
	if !ui.LoopbackHost(base.Hostname()) {
		return nil, fmt.Errorf("the address %q isn't on the loopback (localhost, 127.0.0.0/8 or [::1]): the "+
			"controller reaches the API of its agents on its machine only", address)
	}
	// JoinPath keeps an empty path relative, which would be requested as api/...
	if base.Path == "" {
		base.Path = "/"
	}
	return base.JoinPath("api"), nil
}

// Handler resolves the dashboard bundle and returns the HTTP handler: the
// static files, the login, and to the users logged in the list of agents and
// the proxy to their API. When the bundle cannot be resolved the API still
// works and the index page explains the failure.
func (d *Dashboard) Handler(ctx context.Context) http.Handler {
	bundle, err := resolveBundle(ctx, d.bundle)
	if err != nil {
		log.Warnf("ui: dashboard bundle unavailable, serving API only: %v", err)
	}
	return securityHeaders(d.auth.protect(d.router(bundle, err)))
}

// router wires the API, the bundle static files and the index page, which the
// login page is too: see auth.protect for who reaches them.
func (d *Dashboard) router(b bundle, bundleErr error) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		// each route needs the permissions it declares, see need
		r.With(need(ui.PermDashboardView)).Get("/agents", d.handleAgents)
		operate := need(ui.PermTradingOperate)
		r.With(operate).Post("/agents/{agent}/controls/stop", d.handleBotControl(true))   // recorded, see recordBotControl
		r.With(operate).Post("/agents/{agent}/controls/start", d.handleBotControl(false)) // and its start
		// the API of the agent: its charts read from the panel and from the
		// results of the agents, its controls operated
		r.With(needByMethod([]string{ui.PermDashboardView, ui.PermAgentsView}, []string{ui.PermTradingOperate})).
			HandleFunc("/agents/{agent}/*", d.handleAgent)
		if d.settings != nil {
			d.settings.routes(r)
		}
		if d.supervision != nil {
			d.supervision.routes(r)
		}
		if d.backtests != nil {
			d.backtests.routes(r)
		}
	})

	var page []byte
	var pageCSP string
	if bundleErr == nil {
		var err error
		if page, pageCSP, err = indexPage(b.version); err != nil {
			log.Errorf("ui: the index page of the dashboard: %v", err)
			bundleErr = err
		}
	}
	if bundleErr == nil {
		fs := http.FileServer(bundleFiles{http.Dir(b.dir)})
		r.Handle("/ui/{version}/*", http.StripPrefix("/ui/"+b.version, bundleCacheHeaders(b, fs)))
		index := func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", pageCSP)
			if _, err := w.Write(page); err != nil {
				log.Debug(err)
			}
		}
		r.Get("/", index)
		r.Get("/login", index)       // the app shows its login
		r.Get("/settings", index)    // and the settings
		r.Get("/performance", index) // and the performance
		r.Get("/backtesting", index) // and the backtests
	} else {
		// the reason is in the log of Handler, never on this page, which the
		// login is too
		unavailable := func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", errorPolicy)
			w.WriteHeader(http.StatusServiceUnavailable)
			if err := errorTemplate.Execute(w, map[string]string{
				"DirKey":     uiDirKey,
				"VersionKey": uiVersionKey,
			}); err != nil {
				log.Error(err)
			}
		}
		r.Get("/", unavailable)
		r.Get("/login", unavailable)
		r.Get("/settings", unavailable)
		r.Get("/performance", unavailable)
		r.Get("/backtesting", unavailable)
	}

	return r
}

// bundleCacheHeaders makes versioned assets immutable and local (dev)
// assets uncached.
func bundleCacheHeaders(b bundle, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.version == versionLocal {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		next.ServeHTTP(w, r)
	})
}

// handleAgents lists the agents.
func (d *Dashboard) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, ui.AgentsResponse{Agents: d.statuses(r.Context())})
}

// statuses returns the agents, in the order of the configuration, probing their
// API at once, with their current period, their supervision and their pairs.
func (d *Dashboard) statuses(ctx context.Context) []ui.AgentStatus {
	agents := d.agents.list()
	statuses := make([]ui.AgentStatus, len(agents))
	periods := d.currentPeriods(ctx)
	stored := d.storedAgents(ctx)
	supervisions := d.supervisions(ctx, stored)

	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = ui.AgentStatus{Name: a.Name, Kind: a.Kind, Address: a.Address, Online: d.online(ctx, a),
				Supervision: supervisions[a.Name]}
			if s, ok := stored[a.Name]; ok {
				statuses[i].Pairs = agentPairs(s.Params)
			}
			if period, ok := periods[a.Name]; ok {
				current := uiPeriod(period, nil)
				statuses[i].Period = &current
			}
		}()
	}
	wg.Wait()
	return statuses
}

// storedAgents returns the agents of the database, by name, none without
// settings or on a failure to read them.
func (d *Dashboard) storedAgents(ctx context.Context) map[string]store.Agent {
	if d.settings == nil {
		return nil
	}
	agents, err := d.settings.store.Agents(ctx)
	if err != nil {
		log.Warnf("ui: the agents of the database: %v", err)
		return nil
	}
	byName := make(map[string]store.Agent, len(agents))
	for _, a := range agents {
		byName[a.Name] = a
	}
	return byName
}

// supervisions returns the supervision of the agents of agents supervised, by
// name.
func (d *Dashboard) supervisions(ctx context.Context, agents map[string]store.Agent) map[string]*ui.AgentSupervisionInfo {
	infos := make(map[string]*ui.AgentSupervisionInfo)
	for _, a := range agents {
		if info := withDaily(ctx, d.settings.store, a, supervisionInfo(a)); info != nil {
			infos[a.Name] = info
		}
	}
	return infos
}

// agentPairs returns the pairs an agent trades, as its params tell them: the
// default pair without any.
func agentPairs(params store.Params) []string {
	return listParam(cmp.Or(params["pair"], agentapi.DefaultPair))
}

// Online tells whether the API of the agent of that name answers.
func (d *Dashboard) Online(ctx context.Context, name string) bool {
	a, ok := d.agents.get(name)
	return ok && d.online(ctx, a)
}

// online tells whether the API of the agent answers. The health endpoint
// doesn't: it fails without recent candles, such as once a backtest ends.
func (d *Dashboard) online(ctx context.Context, a *agent) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.api.JoinPath("pairs").String(), nil)
	if err != nil {
		return false
	}
	d.keys.authorize(a.Name, req.Header)
	resp, err := d.probe.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// handleAgent proxies a request under /api/agents/{agent} to the API of the
// agent, its answer never sniffed, see newAgentProxy.
func (d *Dashboard) handleAgent(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "agent")
	a, ok := d.agents.get(name)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "Agente desconhecido.")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.StripPrefix("/api/agents/"+name, a.proxy).ServeHTTP(w, r)
}
