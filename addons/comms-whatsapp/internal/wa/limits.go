package wa

import (
	"context"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// LimitUsage is one rate limit with its live usage: what a person (or an
// agent, or an external system) needs to know before sending.
type LimitUsage struct {
	Scope     string `json:"scope"` // per-chat | per-group | global
	Limit     string `json:"limit"` // "20/1h"
	Human     string `json:"human"` // "20 per hour"
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
	// NextFreeAt is when the next slot opens; zero when one is free now.
	NextFreeAt        time.Time `json:"next_free_at,omitzero"`
	RetryAfterSeconds int       `json:"retry_after_seconds,omitempty"`
}

// ChatLimits answers "can I send to this chat right now, and if not, why
// and until when?".
type ChatLimits struct {
	JID        string     `json:"jid"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	CanSendNow bool       `json:"can_send_now"`
	Blockers   []string   `json:"blockers,omitempty"` // refusal codes that apply right now
	Usage      LimitUsage `json:"usage"`              // per-chat or per-group
	// RetryAfterSeconds is the wait until rate limits alone stop blocking
	// (other blockers, like not_allowed, never clear by waiting).
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}

// LimitsReport is the full picture: configured limits, global usage and,
// optionally, one chat.
type LimitsReport struct {
	PerChat     LimitUsage  `json:"per_chat"`  // configured value; usage needs a chat
	PerGroup    LimitUsage  `json:"per_group"` // configured value; usage needs a chat
	Global      LimitUsage  `json:"global"`
	AllowGroups bool        `json:"allow_groups"`
	Paused      bool        `json:"paused"`
	Chat        *ChatLimits `json:"chat,omitempty"`
}

// usage computes one limit's usage over the sliding window ending at now.
func usage(ctx context.Context, arch *archive.Store, scope, chatJID string, r Rate, now time.Time) (LimitUsage, error) {
	u := LimitUsage{Scope: scope, Limit: r.String(), Human: r.Human()}
	since := now.Add(-r.Window)
	n, err := arch.CountSent(ctx, chatJID, since)
	if err != nil {
		return u, err
	}
	u.Used, u.Remaining = n, max(r.Count-n, 0)
	if u.Remaining == 0 {
		oldest, ok, err := arch.OldestSentSince(ctx, chatJID, since)
		if err != nil {
			return u, err
		}
		if ok {
			u.NextFreeAt = oldest.Add(r.Window).UTC()
			u.RetryAfterSeconds = max(int(u.NextFreeAt.Sub(now).Seconds()+0.999), 1)
		}
	}
	return u, nil
}

// BuildLimitsReport reads the archive (no daemon needed) and returns the
// limits with live usage; chat, when non-nil, adds that chat's view.
func BuildLimitsReport(ctx context.Context, arch *archive.Store, cfg Config, now time.Time, chat *archive.Chat) (LimitsReport, error) {
	l := cfg.ParsedLimits()
	rep := LimitsReport{
		PerChat:     LimitUsage{Scope: "per-chat", Limit: l.PerChat.String(), Human: l.PerChat.Human(), Remaining: l.PerChat.Count},
		PerGroup:    LimitUsage{Scope: "per-group", Limit: l.PerGroup.String(), Human: l.PerGroup.Human(), Remaining: l.PerGroup.Count},
		AllowGroups: cfg.AllowGroups,
		Paused:      cfg.Paused,
	}
	var err error
	if rep.Global, err = usage(ctx, arch, "global", "", l.Global, now); err != nil {
		return rep, err
	}
	if chat == nil {
		return rep, nil
	}
	group := chat.Kind == archive.KindGroup
	scope := "per-chat"
	if group {
		scope = "per-group"
	}
	cu, err := usage(ctx, arch, scope, chat.JID, l.ForKind(group), now)
	if err != nil {
		return rep, err
	}
	cl := &ChatLimits{JID: chat.JID, Name: chat.Name, Kind: string(chat.Kind), Usage: cu}
	if cfg.Paused {
		cl.Blockers = append(cl.Blockers, string(CodePaused))
	}
	if group && !cfg.AllowGroups {
		cl.Blockers = append(cl.Blockers, string(CodeGroupsDisabled))
	}
	if !chat.SendAllowed {
		cl.Blockers = append(cl.Blockers, string(CodeNotAllowed))
	}
	if chat.LastActivity.IsZero() {
		cl.Blockers = append(cl.Blockers, string(CodeNoHistory))
	}
	if cu.Remaining == 0 || rep.Global.Remaining == 0 {
		cl.Blockers = append(cl.Blockers, string(CodeRateLimited))
		cl.RetryAfterSeconds = max(cu.RetryAfterSeconds, rep.Global.RetryAfterSeconds)
	}
	cl.CanSendNow = len(cl.Blockers) == 0
	rep.Chat = cl
	return rep, nil
}
