package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"text/tabwriter"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
)

// ── route ────────────────────────────────────────────────────────────────

func runRoute(ctx context.Context, e env, args []string) error {
	fs := newFlags("route", e)
	asJSON := fs.Bool("json", false, "emit JSON (list)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	usage := fmt.Errorf("usage: route list | route set <chat|default> <path> | route rm <chat|default> (see '%s help route')", progName())
	if len(pos) == 0 {
		return usage
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	switch {
	case pos[0] == "list" && len(pos) == 1:
		return printRoutes(ctx, e, cfg, *asJSON)
	case pos[0] == "set" && len(pos) == 3:
		target, path := pos[1], pos[2]
		label, err := setRoute(ctx, e, &cfg, target, path)
		if err != nil {
			return err
		}
		if err := saveConfig(e, cfg); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Messages from %s now go to fairway %s.\n", label, path)
		fmt.Fprintf(e.stdout, "Make sure the route exists, e.g.:\n  shipyard fairway route add --path %s --auth local-only --action crew.run --target <agent> --async\n", path)
		return nil
	case pos[0] == "rm" && len(pos) == 2:
		label, err := removeRoute(ctx, e, &cfg, pos[1])
		if err != nil {
			return err
		}
		if err := saveConfig(e, cfg); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Messages from %s are no longer delivered.\n", label)
		return nil
	default:
		return usage
	}
}

func setRoute(ctx context.Context, e env, cfg *wa.Config, target, path string) (string, error) {
	if target == "default" {
		cfg.Routes.Default = path
		return "every unrouted chat", nil
	}
	chat, err := resolveForRoute(ctx, e, target)
	if err != nil {
		return "", err
	}
	if cfg.Routes.ByChat == nil {
		cfg.Routes.ByChat = map[string]string{}
	}
	cfg.Routes.ByChat[chat.jid] = path
	return chat.name, nil
}

func removeRoute(ctx context.Context, e env, cfg *wa.Config, target string) (string, error) {
	if target == "default" {
		if cfg.Routes.Default == "" {
			return "", errors.New("no default route is set")
		}
		cfg.Routes.Default = ""
		return "unrouted chats", nil
	}
	chat, err := resolveForRoute(ctx, e, target)
	if err != nil {
		return "", err
	}
	if _, ok := cfg.Routes.ByChat[chat.jid]; !ok {
		return "", fmt.Errorf("%s has no route", chat.name)
	}
	delete(cfg.Routes.ByChat, chat.jid)
	return chat.name, nil
}

type routeChat struct{ jid, name string }

func resolveForRoute(ctx context.Context, e env, query string) (routeChat, error) {
	arch, err := openArchive(ctx, e)
	if err != nil {
		return routeChat{}, err
	}
	defer func() { _ = arch.Close() }()
	c, err := resolveChat(ctx, arch, query)
	if err != nil {
		return routeChat{}, err
	}
	return routeChat{jid: c.JID, name: c.Name}, nil
}

func printRoutes(ctx context.Context, e env, cfg wa.Config, asJSON bool) error {
	addr := wa.FairwayAddr(cfg.FairwayAddr, e.paths.FairwayDir)
	if asJSON {
		return writeJSON(e.stdout, struct {
			wa.Routes
			FairwayAddr string `json:"fairway_addr"`
		}{cfg.Routes, addr})
	}
	names := map[string]string{}
	if arch, err := openArchive(ctx, e); err == nil {
		for jid := range cfg.Routes.ByChat {
			if c, err := arch.GetChat(ctx, jid); err == nil {
				names[jid] = c.Name
			}
		}
		_ = arch.Close()
	}
	fmt.Fprintf(e.stdout, "Delivering to fairway at %s (own messages: %v)\n\n", addr, cfg.Routes.IncludeOwn)
	if len(cfg.Routes.ByChat) == 0 && cfg.Routes.Default == "" {
		fmt.Fprintln(e.stdout, "No routes: received messages are not delivered anywhere.")
		return nil
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHAT\tPATH\tJID")
	for _, jid := range slices.Sorted(maps.Keys(cfg.Routes.ByChat)) {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", names[jid], cfg.Routes.ByChat[jid], jid)
	}
	if cfg.Routes.Default != "" {
		fmt.Fprintf(tw, "(default)\t%s\t\n", cfg.Routes.Default)
	}
	return tw.Flush()
}

func saveConfig(e env, cfg wa.Config) error {
	if err := e.paths.Ensure(); err != nil {
		return err
	}
	return wa.SaveConfig(e.paths.Config, cfg)
}
