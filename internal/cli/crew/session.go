package crew

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

// Exit codes produced by the `shipyard crew session` commands.
const (
	sessionExitOK       = 0
	sessionExitNotFound = 1
	sessionExitBusy     = 2
)

// sessionClearLockWait bounds how long `session clear` waits for an in-flight
// run to release a key. Clearing is an interactive operation, so it gives up
// quickly and reports the busy key instead of blocking the terminal for the
// length of an agent turn.
const sessionClearLockWait = 5 * time.Second

// sessionAgentDoc is the tolerant subset of agent.yaml needed to interpret
// sessions.json. The core MUST NOT import addons/crew/internal/*, so the
// handful of relevant fields are mirrored here — same contract as listAgentDoc.
type sessionAgentDoc struct {
	Backend struct {
		Type string `yaml:"type"`
	} `yaml:"backend"`
	Conversation struct {
		Mode string `yaml:"mode"`
		Key  string `yaml:"key"`
		TTL  string `yaml:"ttl"`
	} `yaml:"conversation"`
}

// sessionRow is one row of `session list`, in both table and JSON form.
type sessionRow struct {
	Key       string     `json:"key"`
	SessionID string     `json:"session_id,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	AgeSec    *int64     `json:"age_seconds,omitempty"`
	ExpiresIn *int64     `json:"expires_in_seconds,omitempty"`
	Expired   bool       `json:"expired"`
	// Hashed marks rows whose key could not be recovered from disk — the
	// anthropic_api backend stores one transcript per hashed key and keeps
	// no plaintext index of them.
	Hashed bool `json:"hashed,omitempty"`
}

type sessionDeps struct {
	Home   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
	IsTTY  func() bool
	// LockWait bounds the wait for a key held by a running agent. Zero
	// means sessionClearLockWait.
	LockWait time.Duration
}

func (d sessionDeps) withDefaults() sessionDeps {
	if d.Home == "" {
		if h, err := shipyardHome(); err == nil {
			d.Home = h
		}
	}
	if d.Stdin == nil {
		d.Stdin = os.Stdin
	}
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.IsTTY == nil {
		d.IsTTY = func() bool { return tty.IsInteractive(tty.StdinFD()) }
	}
	if d.LockWait <= 0 {
		d.LockWait = sessionClearLockWait
	}
	return d
}

func newSessionCmd() *cobra.Command {
	return newSessionCmdWith(sessionDeps{})
}

func newSessionCmdWith(deps sessionDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Inspect and reset stored conversations of a stateful agent",
		Long: `Agents with "conversation.mode: stateful" keep one session per conversation
key under ~/.shipyard/crew/<agent>/. These commands show what is stored and
let you reset a conversation by hand, without editing state files.`,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newSessionListCmd(deps))
	cmd.AddCommand(newSessionClearCmd(deps))
	return cmd
}

func newSessionListCmd(deps sessionDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list <agent>",
		Short: "List stored conversation keys, their age and time to expiry",
		Long: `Reads ~/.shipyard/crew/<agent>/sessions.json and prints one row per stored
conversation: the key, the external session id, how long since the last run
touched it, and how long it has left before "conversation.ttl" discards it.

TTL measures inactivity, so EXPIRES IN is counted from the last use, not from
the moment the session started.

For "anthropic_api" agents the transcripts on disk are named by a hash of the
key, so keys cannot be listed in plaintext; rows show the hash instead. Pass
the key to "session clear" to remove one of them.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runSessionList(deps, args[0], asJSON)
			if code == sessionExitOK {
				return nil
			}
			return &ExitError{Code: code}
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit output as JSON")
	return cmd
}

func newSessionClearCmd(deps sessionDeps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "clear <agent> [key]",
		Short: "Discard one stored conversation, or all of them",
		Long: `Removes the stored session for <key>, or every stored session when no key is
given. The next run starts a fresh conversation.

This clears Shipyard's side of the link. The transcript the external CLI keeps
for that session (under ~/.claude, for the "cli" backend) is left untouched and
simply stops being referenced.

A key currently held by a running agent is skipped rather than yanked from
under it; the command reports which keys it could not clear.`,
		Args:          cobra.RangeArgs(1, 2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			key := ""
			if len(args) == 2 {
				key = args[1]
			}
			code := runSessionClear(deps, args[0], key, len(args) == 2, yes)
			if code == sessionExitOK {
				return nil
			}
			return &ExitError{Code: code}
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip interactive confirmation when clearing every key")
	return cmd
}

func runSessionList(deps sessionDeps, name string, asJSON bool) int {
	deps = deps.withDefaults()

	agentDir, doc, code := loadSessionAgent(deps, name)
	if code != sessionExitOK {
		return code
	}
	ttl, ttlErr := parseSessionTTL(doc)
	if ttlErr != nil {
		fmt.Fprintf(deps.Stderr, "warning: %s\n", ttlErr)
	}

	rows, err := collectSessionRows(agentDir, doc, ttl, deps.Now())
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard crew session list: %s\n", err)
		return sessionExitNotFound
	}

	if asJSON {
		enc := json.NewEncoder(deps.Stdout)
		enc.SetIndent("", "  ")
		return encodeOrFail(deps, enc.Encode(rows))
	}
	if doc.Conversation.Mode != "stateful" {
		fmt.Fprintf(deps.Stdout, "%s is not stateful (conversation.mode: %s) — no sessions are stored\n",
			name, defaultStr(doc.Conversation.Mode, "stateless"))
		return sessionExitOK
	}
	if len(rows) == 0 {
		fmt.Fprintln(deps.Stdout, "no sessions stored")
		return sessionExitOK
	}

	tw := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tSESSION\tLAST USE\tEXPIRES IN")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			sessionKeyLabel(r), defaultStr(truncateID(r.SessionID), "-"),
			formatLastUse(r), formatExpiry(r, ttl))
	}
	if err := tw.Flush(); err != nil {
		return sessionExitNotFound
	}
	if ttl == 0 {
		fmt.Fprintln(deps.Stdout, "\nconversation.ttl is not set: sessions never expire on their own.")
	}
	return sessionExitOK
}

func runSessionClear(deps sessionDeps, name, key string, hasKey, yes bool) int {
	deps = deps.withDefaults()

	agentDir, doc, code := loadSessionAgent(deps, name)
	if code != sessionExitOK {
		return code
	}

	targets := []string{}
	if hasKey {
		targets = append(targets, key)
	} else {
		ttl, _ := parseSessionTTL(doc)
		rows, err := collectSessionRows(agentDir, doc, ttl, deps.Now())
		if err != nil {
			fmt.Fprintf(deps.Stderr, "shipyard crew session clear: %s\n", err)
			return sessionExitNotFound
		}
		if len(rows) == 0 {
			fmt.Fprintln(deps.Stdout, "no sessions stored")
			return sessionExitOK
		}
		if !yes {
			if !deps.IsTTY() {
				fmt.Fprintln(deps.Stderr, "shipyard crew session clear: refusing to clear every key non-interactively without --yes")
				return sessionExitNotFound
			}
			if !confirmSessionClear(deps.Stdin, deps.Stdout, name, len(rows)) {
				fmt.Fprintln(deps.Stdout, "cancelled.")
				return sessionExitOK
			}
		}
		for _, r := range rows {
			// Hashed rows carry no recoverable key; the lock and the file
			// are both addressed by the hash, which is all removal needs.
			targets = append(targets, r.Key)
		}
	}

	cleared, busy := 0, []string{}
	for _, t := range targets {
		hash := t
		if !isSessionHash(t) || doc.Backend.Type != backendAnthropicAPI {
			hash = hashSessionKey(t)
		}
		release, err := lockSessionKey(filepath.Join(agentDir, "locks", hash+".lock"), deps.LockWait)
		if err != nil {
			busy = append(busy, t)
			continue
		}
		removed, err := clearSessionKey(agentDir, doc, t, hash)
		release()
		if err != nil {
			fmt.Fprintf(deps.Stderr, "shipyard crew session clear: %s\n", err)
			return sessionExitNotFound
		}
		if removed {
			cleared++
		}
	}

	switch {
	case cleared == 0 && len(busy) > 0:
		// A "cleared 0 sessions" line would only add noise ahead of the
		// busy-key report below.
	case cleared == 0 && hasKey:
		fmt.Fprintf(deps.Stdout, "no stored session for key %q\n", key)
	case cleared == 1:
		fmt.Fprintln(deps.Stdout, "cleared 1 session")
	default:
		fmt.Fprintf(deps.Stdout, "cleared %d sessions\n", cleared)
	}
	if len(busy) > 0 {
		fmt.Fprintf(deps.Stderr, "skipped %d key(s) held by a running agent: %s\n",
			len(busy), strings.Join(busy, ", "))
		return sessionExitBusy
	}
	return sessionExitOK
}
