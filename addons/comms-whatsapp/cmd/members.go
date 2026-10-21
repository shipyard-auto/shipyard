package main

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// ── group ────────────────────────────────────────────────────────────────

func runGroup(ctx context.Context, e env, args []string) error {
	fs := newFlags("group", e)
	asJSON := fs.Bool("json", false, "emit JSON")
	refresh := fs.Bool("refresh", false, "ask WhatsApp again instead of using the cache (still at most once a minute per group)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 || pos[0] != "info" {
		return fmt.Errorf("usage: group info <group> [--refresh] [--json] (see '%s help group')", progName())
	}
	var info wa.GroupInfo
	if err := wa.CallRPC(ctx, e.paths.Socket, "group_info", wa.GroupInfoParams{Chat: pos[1], Refresh: *refresh}, &info); err != nil {
		var re *wa.RPCError
		if errors.As(err, &re) && re.Data != nil && re.Data.Code == wa.CodeRateLimited {
			return fmt.Errorf("%s (group info is fetched at most once a minute per group)", re.Message)
		}
		return err
	}
	if *asJSON {
		return writeJSON(e.stdout, info)
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Group:\t%s\n", info.Name)
	fmt.Fprintf(tw, "JID:\t%s\n", info.JID)
	fmt.Fprintf(tw, "Participants:\t%d\n", info.Participants)
	fmt.Fprintf(tw, "Admins:\t%d\n", info.Admins)
	fmt.Fprintf(tw, "You are admin:\t%s\n", yesNo(info.SelfIsAdmin))
	age := ""
	if info.Cached {
		age = fmt.Sprintf(" (cached, %s old)", time.Since(info.FetchedAt).Round(time.Second))
	}
	fmt.Fprintf(tw, "Fetched at:\t%s%s\n", info.FetchedAt.Local().Format(time.DateTime), age)
	return tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ── members ──────────────────────────────────────────────────────────────

func runMembers(ctx context.Context, e env, args []string) error {
	fs := newFlags("members", e)
	asJSON := fs.Bool("json", false, "emit JSON (list)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	usage := fmt.Errorf("usage: members list [--json] | members track <group> | members untrack <group> (see '%s help members')", progName())
	if len(pos) == 0 {
		return usage
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()
	switch {
	case pos[0] == "list" && len(pos) == 1:
		groups, err := arch.ListTrackedGroups(ctx)
		if err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(e.stdout, struct {
				Groups []archive.TrackedGroup `json:"groups"`
			}{groups})
		}
		if len(groups) == 0 {
			fmt.Fprintf(e.stdout, "No group's members are tracked. Start with '%s members track <group>'.\n", progName())
			return nil
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "GROUP\tJID\tEVENTS")
		for _, g := range groups {
			fmt.Fprintf(tw, "%s\t%s\t%d\n", g.Name, g.JID, g.Events)
		}
		return tw.Flush()
	case (pos[0] == "track" || pos[0] == "untrack") && len(pos) == 2:
		chat, err := resolveChat(ctx, arch, pos[1])
		if err != nil {
			return err
		}
		if chat.Kind != archive.KindGroup {
			return fmt.Errorf("%s is not a group: only groups have members to track", chat.Name)
		}
		on := pos[0] == "track"
		was, err := arch.MembersTracked(ctx, chat.JID)
		if err != nil {
			return err
		}
		if was == on {
			fmt.Fprintf(e.stdout, "%s: members already %s.\n", chat.Name, map[bool]string{true: "tracked", false: "untracked"}[on])
			return nil
		}
		n, err := arch.SetMembersTracked(ctx, chat.JID, on)
		if err != nil {
			return err
		}
		if on {
			cfg, err := loadConfig(e)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.stdout, "Tracking members of %s (applies live): joins, leaves, promotions and demotions are kept %s\n",
				chat.Name, cfg.MembersTTL)
			fmt.Fprintf(e.stdout, "and published to fairway as %s (scope %s).\n", wa.MembersEventTopic, wa.MembersScope(chat.JID))
			return nil
		}
		fmt.Fprintf(e.stdout, "Stopped tracking members of %s; %d stored events deleted.\n", chat.Name, n)
		return nil
	default:
		return usage
	}
}
