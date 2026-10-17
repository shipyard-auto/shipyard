package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// limitKeys maps the names people use with `limits set` to config keys.
var limitKeys = map[string]string{
	"per-chat":  "limit-per-chat",
	"per-group": "limit-per-group",
	"global":    "limit-global",
}

// blockerText explains each refusal code in plain words, with the fix.
var blockerText = map[string]string{
	string(wa.CodePaused):         "sending is paused — run 'resume'",
	string(wa.CodeGroupsDisabled): "sending to groups is off — run 'config set allow-groups true'",
	string(wa.CodeNotAllowed):     "not on the send allowlist — run 'allow <chat>'",
	string(wa.CodeNoHistory):      "no conversation seen yet — send or receive one message there from the phone",
	string(wa.CodeRateLimited):    "a rate limit is full — wait for the next slot",
}

func runLimits(ctx context.Context, e env, args []string) error {
	fs := newFlags("limits", e)
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	switch {
	case len(pos) >= 1 && pos[0] == "set":
		if len(pos) != 3 {
			return errors.New("usage: limits set <per-chat|per-group|global> <count>/<window>  (e.g. 20/h, 6/10m, 100/1d)")
		}
		key, ok := limitKeys[pos[1]]
		if !ok {
			return fmt.Errorf("unknown limit %q: use per-chat, per-group or global", pos[1])
		}
		if err := cfg.Set(key, pos[2]); err != nil {
			return err
		}
		if err := saveConfig(e, cfg); err != nil {
			return err
		}
		r, _ := wa.ParseRate(pos[2])
		fmt.Fprintf(e.stdout, "%s limit is now %s. Applies to the next send, no restart needed.\n", pos[1], r.Human())
		return nil
	case len(pos) == 1 && pos[0] == "reset":
		cfg.Limits = wa.DefaultConfig().Limits
		if err := saveConfig(e, cfg); err != nil {
			return err
		}
		fmt.Fprintln(e.stdout, "Limits restored to the defaults:")
		return printLimits(ctx, e, cfg, nil, false)
	case len(pos) > 1:
		return fmt.Errorf("usage: limits [chat] | limits set <name> <rate> | limits reset (see '%s help limits')", progName())
	}

	var chat *archive.Chat
	if len(pos) == 1 {
		arch, err := openArchive(ctx, e)
		if err != nil {
			return err
		}
		c, err := resolveChat(ctx, arch, pos[0])
		_ = arch.Close()
		if err != nil {
			return err
		}
		chat = &c
	}
	return printLimits(ctx, e, cfg, chat, *asJSON)
}

func limitsReport(ctx context.Context, e env, cfg wa.Config, chat *archive.Chat) (wa.LimitsReport, error) {
	arch, err := openArchive(ctx, e)
	if err != nil {
		return wa.LimitsReport{}, err
	}
	defer func() { _ = arch.Close() }()
	return wa.BuildLimitsReport(ctx, arch, cfg, time.Now(), chat)
}

func printLimits(ctx context.Context, e env, cfg wa.Config, chat *archive.Chat, asJSON bool) error {
	rep, err := limitsReport(ctx, e, cfg, chat)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(e.stdout, rep)
	}
	w := e.stdout
	groups := "sending to groups is OFF (config set allow-groups true)"
	if rep.AllowGroups {
		groups = "sending to groups is on"
	}
	fmt.Fprintln(w, "Sending limits (each counts the messages sent in the last window):")
	fmt.Fprintf(w, "  each contact   %-22s per-chat\n", rep.PerChat.Human)
	fmt.Fprintf(w, "  each group     %-22s per-group  · %s\n", rep.PerGroup.Human, groups)
	fmt.Fprintf(w, "  all chats      %-22s global     · %s\n", rep.Global.Human, usageLine(rep.Global))
	if rep.Paused {
		fmt.Fprintln(w, "\n  PAUSED: nothing is sent until 'resume'.")
	}
	if c := rep.Chat; c != nil {
		kind := "contact"
		if c.Kind == string(archive.KindGroup) {
			kind = "group"
		}
		fmt.Fprintf(w, "\n%s (%s): %s\n", c.Name, kind, usageLine(c.Usage))
		if c.CanSendNow {
			fmt.Fprintln(w, "  ✓ can send now")
		} else {
			fmt.Fprintln(w, "  ✗ cannot send now:")
			for _, b := range c.Blockers {
				fmt.Fprintf(w, "    - %s: %s\n", b, blockerText[b])
			}
			if c.RetryAfterSeconds > 0 {
				fmt.Fprintf(w, "    rate limits clear %s\n", retryText(time.Duration(c.RetryAfterSeconds)*time.Second))
			}
		}
	}
	fmt.Fprintf(w, "\nChange:   limits set <per-chat|per-group|global> <count>/<window>   e.g. 20/h, 6/10m, 100/1d\n")
	fmt.Fprintf(w, "Defaults: %s per contact, %s per group, %s overall ('limits reset')\n",
		mustHuman(wa.DefaultLimitPerChat), mustHuman(wa.DefaultLimitPerGroup), mustHuman(wa.DefaultLimitGlobal))
	return nil
}

func usageLine(u wa.LimitUsage) string {
	s := fmt.Sprintf("%d used, %d left", u.Used, u.Remaining)
	if u.Remaining == 0 && !u.NextFreeAt.IsZero() {
		s += ", next slot " + retryText(time.Duration(u.RetryAfterSeconds)*time.Second)
	}
	return s
}

// retryText renders a wait as "in 4m12s (at 14:32:05)".
func retryText(d time.Duration) string {
	return fmt.Sprintf("in %s (at %s)", d.Round(time.Second), time.Now().Add(d).Format("15:04:05"))
}

func mustHuman(s string) string {
	r, err := wa.ParseRate(s)
	if err != nil {
		return s
	}
	return r.Human()
}

// refusalMessage explains a policy refusal, including when a rate limit
// clears.
func refusalMessage(se *wa.SendError) string {
	msg := fmt.Sprintf("refused (%s): %s", se.Code, se.Msg)
	if se.RetryAfter > 0 {
		msg += "; try again " + retryText(se.RetryAfter)
	}
	return msg
}

func toolLimits(ctx context.Context, e env, raw []byte) envelope {
	var in struct {
		Chat string `json:"chat"`
	}
	if err := decodeInput(raw, &in); err != nil {
		return envelope{Error: "invalid input: " + err.Error()}
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return envelope{Error: err.Error()}
	}
	var chat *archive.Chat
	if strings.TrimSpace(in.Chat) != "" {
		arch, err := openArchive(ctx, e)
		if err != nil {
			return envelope{Error: err.Error()}
		}
		c, err := resolveChat(ctx, arch, in.Chat)
		_ = arch.Close()
		if err != nil {
			return envelope{Error: err.Error()}
		}
		chat = &c
	}
	rep, err := limitsReport(ctx, e, cfg, chat)
	if err != nil {
		return envelope{Error: err.Error()}
	}
	return envelope{OK: true, Data: rep}
}
