package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
)

// ── api ──────────────────────────────────────────────────────────────────

// DefaultAPIPrefix is where 'api enable' mounts the fairway routes.
const DefaultAPIPrefix = "/api/whatsapp"

// apiEndpoints are the routes 'api enable' generates. Sends get a longer
// timeout: humanize types for up to 8s and sends run one at a time.
var apiEndpoints = []struct {
	name, path, scope string
	timeout           time.Duration
}{
	{"messages", wa.APIMessagesPath, wa.ReadScopeName, 30 * time.Second},
	{"chats", wa.APIChatsPath, wa.ReadScopeName, 30 * time.Second},
	{"send", wa.APISendPath, wa.SendScopeName, 60 * time.Second},
	{"limits", wa.APILimitsPath, wa.SendScopeName, 30 * time.Second},
}

func runAPI(_ context.Context, e env, args []string) error {
	fs := newFlags("api", e)
	addr := fs.String("addr", wa.DefaultAPIAddr, "loopback host:port the daemon serves the API on (enable)")
	prefix := fs.String("prefix", DefaultAPIPrefix, "fairway path prefix for the generated routes (enable, rotate-token)")
	asJSON := fs.Bool("json", false, "emit JSON (status)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	usage := fmt.Errorf("usage: api [status] | api enable [--addr H:P] [--prefix P] | api disable | api rotate-token (see '%s help api')", progName())
	if len(pos) > 1 {
		return usage
	}
	sub := "status"
	if len(pos) == 1 {
		sub = pos[0]
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	switch sub {
	case "status":
		return printAPIStatus(e, cfg, *asJSON)
	case "enable":
		if err := wa.ValidateAPIAddr(*addr); err != nil {
			return err
		}
		if err := validPrefix(*prefix); err != nil {
			return err
		}
		if err := e.paths.Ensure(); err != nil {
			return err
		}
		token, err := wa.EnsureAPIToken(e.paths.APIToken, false)
		if err != nil {
			return err
		}
		changed := cfg.APIAddr != *addr
		cfg.APIAddr = *addr
		if err := saveConfig(e, cfg); err != nil {
			return err
		}
		files, err := writeAPIRoutes(e.paths, *addr, *prefix, token)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Query API enabled on %s.\n\n", *addr)
		printAPISteps(e, files, *prefix, changed)
		return nil
	case "disable":
		if cfg.APIAddr == "" {
			fmt.Fprintln(e.stdout, "The query API is already off.")
			return nil
		}
		cfg.APIAddr = ""
		if err := saveConfig(e, cfg); err != nil {
			return err
		}
		fmt.Fprintln(e.stdout, "Query API disabled. Restart the daemon to stop serving it, and remove the fairway routes")
		fmt.Fprintf(e.stdout, "(paths as created by 'api enable', %s/… by default):\n", DefaultAPIPrefix)
		for _, ep := range apiEndpoints {
			fmt.Fprintf(e.stdout, "  shipyard fairway route delete %s/%s\n", DefaultAPIPrefix, ep.name)
		}
		return nil
	case "rotate-token":
		if cfg.APIAddr == "" {
			return fmt.Errorf("the query API is off; run '%s api enable' first", progName())
		}
		token, err := wa.EnsureAPIToken(e.paths.APIToken, true)
		if err != nil {
			return err
		}
		files, err := writeAPIRoutes(e.paths, cfg.APIAddr, *prefix, token)
		if err != nil {
			return err
		}
		fmt.Fprintln(e.stdout, "API token rotated: the current fairway routes stop working until replaced (no daemon restart needed):")
		for _, f := range files {
			fmt.Fprintf(e.stdout, "  shipyard fairway route delete %s && shipyard fairway route add --from-file %s\n", f.path, f.file)
		}
		return nil
	default:
		return usage
	}
}

type apiRouteFile struct {
	path string // fairway path
	file string // generated definition
}

// fairwayRoute is the JSON shape 'shipyard fairway route add --from-file'
// reads. Declared here because addons do not import each other; the JSON
// is the contract.
type fairwayRoute struct {
	Path string `json:"path"`
	Auth struct {
		Type  string `json:"type"`
		Scope string `json:"scope"`
	} `json:"auth"`
	Action struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
	} `json:"action"`
	Timeout time.Duration `json:"timeout"`
}

// writeAPIRoutes writes one fairway route per endpoint. The files hold the
// API token, so they are 0600 inside the 0700 state directory.
func writeAPIRoutes(p wa.Paths, addr, prefix, token string) ([]apiRouteFile, error) {
	if err := os.MkdirAll(p.APIRoutes, 0o700); err != nil {
		return nil, fmt.Errorf("api: create %s: %w", p.APIRoutes, err)
	}
	var out []apiRouteFile
	for _, ep := range apiEndpoints {
		var r fairwayRoute
		r.Path = strings.TrimSuffix(prefix, "/") + "/" + ep.name
		r.Auth.Type, r.Auth.Scope = "signed", ep.scope
		r.Action.Type, r.Action.URL, r.Action.Method = "http.forward", "http://"+addr+ep.path, "POST"
		r.Action.Headers = map[string]string{"Authorization": "Bearer " + token}
		r.Timeout = ep.timeout
		raw, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return nil, err
		}
		file := filepath.Join(p.APIRoutes, ep.name+".json")
		if err := os.WriteFile(file, append(raw, '\n'), 0o600); err != nil {
			return nil, fmt.Errorf("api: write %s: %w", file, err)
		}
		if err := os.Chmod(file, 0o600); err != nil {
			return nil, err
		}
		out = append(out, apiRouteFile{path: r.Path, file: file})
	}
	return out, nil
}

func printAPISteps(e env, files []apiRouteFile, prefix string, restart bool) {
	step := 1
	if restart {
		fmt.Fprintf(e.stdout, "%d. Restart the daemon so it starts listening (e.g. 'shipyard service restart <id>').\n", step)
		step++
	}
	fmt.Fprintf(e.stdout, "%d. Register the routes in fairway (signed clients; scope %s to read, %s to send):\n", step, wa.ReadScopeName, wa.SendScopeName)
	for _, f := range files {
		fmt.Fprintf(e.stdout, "     shipyard fairway route add --from-file %s\n", f.file)
	}
	step++
	fmt.Fprintf(e.stdout, "%d. Grant a client the chats it may read and send to (see '%s help api'):\n", step, progName())
	fmt.Fprintf(e.stdout, "     shipyard fairway client add <client> --scope '%s:<chat jid>' --scope '%s:<group jid>'\n", wa.ReadScopeName, wa.SendScopeName)
	fmt.Fprintf(e.stdout, "   Sending also needs the chat on the allowlist ('allow'), and groups need 'config set allow-groups true'.\n")
	step++
	fmt.Fprintf(e.stdout, "%d. Query it, signed by the client:\n", step)
	fmt.Fprintf(e.stdout, "     POST %s/messages  {\"chat\":\"<jid>\",\"since\":\"24h\"}  → then {\"after\":\"<next_cursor>\"}\n", strings.TrimSuffix(prefix, "/"))
	fmt.Fprintf(e.stdout, "     POST %s/chats     {}\n", strings.TrimSuffix(prefix, "/"))
	fmt.Fprintf(e.stdout, "     POST %s/send      {\"chat\":\"<jid>\",\"text\":\"…\",\"idempotency_key\":\"promo-123\"}\n", strings.TrimSuffix(prefix, "/"))
	fmt.Fprintf(e.stdout, "     POST %s/limits    {\"chat\":\"<jid>\"}\n", strings.TrimSuffix(prefix, "/"))
}

func validPrefix(p string) error {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "/_") || strings.ContainsAny(p, " ?#") || len(p) < 2 {
		return fmt.Errorf("--prefix %q: want a path like %s (not under /_)", p, DefaultAPIPrefix)
	}
	return nil
}

type apiStatus struct {
	Enabled    bool   `json:"enabled"`
	Addr       string `json:"addr,omitempty"`
	Serving    string `json:"serving,omitempty"`
	TokenFile  string `json:"token_file,omitempty"`
	RoutesDir  string `json:"routes_dir,omitempty"`
	NeedsStart bool   `json:"needs_restart,omitempty"`
}

func readAPIStatus(e env, cfg wa.Config) apiStatus {
	s := apiStatus{Enabled: cfg.APIAddr != "", Addr: cfg.APIAddr}
	if st, ok, err := wa.ReadStatus(e.paths.Status); err == nil && ok && st.Running(time.Now()) {
		s.Serving = st.APIAddr
	}
	if t, _ := wa.ReadAPIToken(e.paths.APIToken); t != "" {
		s.TokenFile = e.paths.APIToken
	}
	if _, err := os.Stat(e.paths.APIRoutes); err == nil {
		s.RoutesDir = e.paths.APIRoutes
	}
	s.NeedsStart = s.Serving != s.Addr
	return s
}

func printAPIStatus(e env, cfg wa.Config, asJSON bool) error {
	s := readAPIStatus(e, cfg)
	if asJSON {
		return writeJSON(e.stdout, s)
	}
	switch {
	case !s.Enabled && s.Serving == "":
		fmt.Fprintf(e.stdout, "Query API: off. Turn it on with '%s api enable'.\n", progName())
		return nil
	case !s.Enabled:
		fmt.Fprintf(e.stdout, "Query API: disabled, but the daemon still serves %s until it restarts.\n", s.Serving)
		return nil
	case s.Serving == "":
		fmt.Fprintf(e.stdout, "Query API: enabled on %s, not serving — start or restart the daemon.\n", s.Addr)
	case s.NeedsStart:
		fmt.Fprintf(e.stdout, "Query API: enabled on %s, daemon serving %s — restart it to apply.\n", s.Addr, s.Serving)
	default:
		fmt.Fprintf(e.stdout, "Query API: serving on %s.\n", s.Addr)
	}
	if s.TokenFile == "" {
		return errors.New("API token missing: run 'api enable' again")
	}
	if s.RoutesDir != "" {
		fmt.Fprintf(e.stdout, "Fairway route definitions: %s\n", s.RoutesDir)
	}
	return nil
}
