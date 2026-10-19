package main

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// ── publish ──────────────────────────────────────────────────────────────

func runPublish(ctx context.Context, e env, args []string) error {
	fs := newFlags("publish", e)
	asJSON := fs.Bool("json", false, "emit JSON (list)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	usage := fmt.Errorf("usage: publish list | publish add <chat|watched|*> | publish rm <chat|watched|*> (see '%s help publish')", progName())
	if len(pos) == 0 {
		return usage
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	switch {
	case pos[0] == "list" && len(pos) == 1:
		return printPublish(ctx, e, cfg, *asJSON)
	case (pos[0] == "add" || pos[0] == "rm") && len(pos) == 2:
		item, label, err := publishItem(ctx, e, pos[1])
		if err != nil {
			return err
		}
		changed := false
		if pos[0] == "add" {
			changed = cfg.Publish.Add(item)
		} else {
			changed = cfg.Publish.Remove(item)
		}
		if !changed {
			if pos[0] == "add" {
				fmt.Fprintf(e.stdout, "%s is already published.\n", label)
			} else {
				fmt.Fprintf(e.stdout, "%s was not in the list.\n", label)
			}
			return nil
		}
		if err := saveConfig(e, cfg); err != nil {
			return err
		}
		if pos[0] == "add" {
			fmt.Fprintf(e.stdout, "New messages of %s are now published to fairway's events API (applies live).\n", label)
		} else {
			fmt.Fprintf(e.stdout, "%s is no longer published.\n", label)
		}
		if !cfg.Publish.Enabled() {
			fmt.Fprintln(e.stdout, "Nothing is published now.")
		}
		return nil
	default:
		return usage
	}
}

// publishItem turns a CLI argument into a selection item: the two
// selectors as they are, anything else resolved to a catalog chat.
func publishItem(ctx context.Context, e env, arg string) (item, label string, err error) {
	switch strings.TrimSpace(arg) {
	case wa.PublishWatched:
		return wa.PublishWatched, "every watched chat", nil
	case wa.PublishAll, "all":
		return wa.PublishAll, "every chat", nil
	}
	c, err := resolveForRoute(ctx, e, arg)
	if err != nil {
		return "", "", err
	}
	return c.jid, c.name, nil
}

func printPublish(ctx context.Context, e env, cfg wa.Config, asJSON bool) error {
	if asJSON {
		sel := cfg.Publish
		if sel == nil {
			sel = wa.PublishSelection{}
		}
		return writeJSON(e.stdout, struct {
			Publish wa.PublishSelection `json:"publish"`
		}{sel})
	}
	if !cfg.Publish.Enabled() {
		fmt.Fprintf(e.stdout, "Nothing is published to fairway's events API. Start with '%s publish add watched'.\n", progName())
		return nil
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PUBLISHED\tJID")
	for _, it := range cfg.Publish {
		switch it {
		case wa.PublishAll:
			fmt.Fprintf(tw, "every chat (*)\t\n")
		case wa.PublishWatched:
			watched, _ := arch.ListChats(ctx, archive.ListFilter{WatchedOnly: true})
			fmt.Fprintf(tw, "every watched chat (%d now)\t\n", len(watched))
		default:
			name := "(not in catalog)"
			if c, err := arch.GetChat(ctx, it); err == nil {
				name = c.Name
			}
			fmt.Fprintf(tw, "%s\t%s\n", name, it)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !cfg.Routes.IncludeOwn {
		fmt.Fprintln(e.stdout, "\nMessages the account sends itself are not published (config set include-own true to change).")
	}
	return nil
}

// publishSummary is the status line fragment.
func publishSummary(sel wa.PublishSelection) string {
	switch {
	case !sel.Enabled():
		return ""
	case len(sel) == 1 && sel[0] == wa.PublishAll:
		return "publishing every chat"
	case len(sel) == 1 && sel[0] == wa.PublishWatched:
		return "publishing watched chats"
	}
	return fmt.Sprintf("publishing %d selections (see 'publish list')", len(sel))
}
