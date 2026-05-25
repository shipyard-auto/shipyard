package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
)

// isolateHome redirects SHIPYARD_HOME (and $HOME for good measure) to a
// fresh tmpdir so channel add/list write under the test's sandbox.
func isolateHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("SHIPYARD_HOME", tmp)
	t.Setenv("HOME", tmp)
	return tmp
}

func TestChannelAdd_HappyPath_Telegram(t *testing.T) {
	tmp := isolateHome(t)

	var stdout, stderr bytes.Buffer
	args := []string{
		"--type", "telegram",
		"--name", "tg-personal",
		"--telegram-token", "BOT-TOKEN-123",
		"--telegram-bot-username", "myhelperbot",
	}
	if err := runChannelAdd(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("add: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Registered channel tg-personal") {
		t.Errorf("missing confirmation in stdout: %q", stdout.String())
	}

	// channels.json should now contain exactly one channel.
	chans, err := comms.NewChannelStore(filepath.Join(tmp, "comms")).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(chans) != 1 {
		t.Fatalf("expected 1 channel persisted, got %d", len(chans))
	}
	c := chans[0]
	if c.Name != "tg-personal" || c.Type != comms.ChannelTypeTelegram {
		t.Errorf("channel mismatch: %+v", c)
	}
	if got := c.Settings["bot_username"]; got != "myhelperbot" {
		t.Errorf("settings.bot_username = %v, want myhelperbot", got)
	}

	// Secret file present, perm 0600, contains the token.
	secretPath := comms.SecretPath(filepath.Join(tmp, "comms"), c.ID)
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("secret perm = %o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	var s map[string]string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse secret: %v", err)
	}
	if s["token"] != "BOT-TOKEN-123" {
		t.Errorf("secret token = %q, want BOT-TOKEN-123", s["token"])
	}
}

func TestChannelAdd_JSONOutput(t *testing.T) {
	isolateHome(t)
	var stdout, stderr bytes.Buffer
	args := []string{"--type", "telegram", "--name", "tg-personal", "--telegram-token", "T", "--json"}
	if err := runChannelAdd(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("add: %v", err)
	}
	var c comms.Channel
	if err := json.Unmarshal(stdout.Bytes(), &c); err != nil {
		t.Fatalf("json output not parseable: %v\n%s", err, stdout.String())
	}
	if c.Name != "tg-personal" {
		t.Errorf("decoded name = %q, want tg-personal", c.Name)
	}
}

func TestChannelAdd_RejectsMissingType(t *testing.T) {
	isolateHome(t)
	var stdout, stderr bytes.Buffer
	err := runChannelAdd(context.Background(), []string{"--name", "x", "--telegram-token", "T"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--type is required") {
		t.Errorf("expected missing-type error, got %v", err)
	}
}

func TestChannelAdd_RejectsMissingName(t *testing.T) {
	isolateHome(t)
	var stdout, stderr bytes.Buffer
	err := runChannelAdd(context.Background(), []string{"--type", "telegram", "--telegram-token", "T"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected missing-name error, got %v", err)
	}
}

func TestChannelAdd_RejectsUnknownType(t *testing.T) {
	isolateHome(t)
	var stdout, stderr bytes.Buffer
	err := runChannelAdd(context.Background(), []string{"--type", "slack", "--name", "x"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unknown channel type") {
		t.Errorf("expected unknown-type error, got %v", err)
	}
}

func TestChannelAdd_RejectsTelegramWithoutToken(t *testing.T) {
	isolateHome(t)
	var stdout, stderr bytes.Buffer
	err := runChannelAdd(context.Background(), []string{"--type", "telegram", "--name", "x"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--telegram-token is required") {
		t.Errorf("expected missing-token error, got %v", err)
	}
}

func TestChannelAdd_DuplicateNameRollsBackSecret(t *testing.T) {
	tmp := isolateHome(t)

	// Seed an existing channel with the same name (case-insensitive match).
	first := []string{"--type", "telegram", "--name", "tg-personal", "--telegram-token", "T1"}
	if err := runChannelAdd(context.Background(), first, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first add: %v", err)
	}

	// Try to add a channel with the same Name (different casing → still
	// rejected by FindDuplicateName).
	var stdout, stderr bytes.Buffer
	second := []string{"--type", "telegram", "--name", "TG-Personal", "--telegram-token", "T2"}
	err := runChannelAdd(context.Background(), second, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected duplicate-name error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-name error, got %v", err)
	}

	// Only one channel persists, only one secret file exists.
	chans, _ := comms.NewChannelStore(filepath.Join(tmp, "comms")).Load()
	if len(chans) != 1 {
		t.Errorf("expected 1 surviving channel, got %d", len(chans))
	}
	entries, _ := os.ReadDir(comms.SecretsDir(filepath.Join(tmp, "comms")))
	if len(entries) != 1 {
		t.Errorf("expected 1 secret file, got %d", len(entries))
	}
}

func TestChannelList_Empty(t *testing.T) {
	isolateHome(t)
	var stdout, stderr bytes.Buffer
	if err := runChannelList(context.Background(), nil, &stdout, &stderr); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout.String(), "No channels configured") {
		t.Errorf("expected empty-list message, got %q", stdout.String())
	}
}

func TestChannelList_Table(t *testing.T) {
	isolateHome(t)
	for _, name := range []string{"zeta", "alpha"} {
		if err := runChannelAdd(context.Background(), []string{
			"--type", "telegram", "--name", name, "--telegram-token", "T",
		}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	var stdout, stderr bytes.Buffer
	if err := runChannelList(context.Background(), nil, &stdout, &stderr); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := stdout.String()
	// Header row plus both channels, alphabetically sorted.
	if !strings.Contains(out, "NAME") {
		t.Errorf("missing header in table, got %q", out)
	}
	alphaIdx := strings.Index(out, "alpha")
	zetaIdx := strings.Index(out, "zeta")
	if alphaIdx < 0 || zetaIdx < 0 || alphaIdx > zetaIdx {
		t.Errorf("expected alpha listed before zeta (got idx %d vs %d) in %q", alphaIdx, zetaIdx, out)
	}
}

func TestChannelList_JSON(t *testing.T) {
	isolateHome(t)
	if err := runChannelAdd(context.Background(), []string{
		"--type", "telegram", "--name", "foo", "--telegram-token", "T",
	}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runChannelList(context.Background(), []string{"--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var chans []comms.Channel
	if err := json.Unmarshal(stdout.Bytes(), &chans); err != nil {
		t.Fatalf("not parseable JSON: %v\n%s", err, stdout.String())
	}
	if len(chans) != 1 {
		t.Errorf("expected 1 channel, got %d", len(chans))
	}
}

func TestChannelList_JSON_EmptyArrayWhenNoChannels(t *testing.T) {
	isolateHome(t)
	var stdout, stderr bytes.Buffer
	if err := runChannelList(context.Background(), []string{"--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("list --json: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != "[]" {
		t.Errorf("expected '[]' for empty registry, got %q", got)
	}
}

func TestChannelAdd_HelpVisibleInListKnownTypes(t *testing.T) {
	if got := listKnownTypes(); got != "telegram" {
		t.Errorf("listKnownTypes() = %q, want 'telegram' (v1 contract)", got)
	}
}

// Sanity: confirm runChannelAdd rejects telegram channels with malformed
// names BEFORE any file write (Channel.Validate gate).
func TestChannelAdd_NameValidatedBeforeDiskWrite(t *testing.T) {
	tmp := isolateHome(t)
	var stdout, stderr bytes.Buffer
	err := runChannelAdd(context.Background(), []string{
		"--type", "telegram", "--name", "1bad", "--telegram-token", "T",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	// channels.json must NOT exist.
	if _, err := os.Stat(filepath.Join(tmp, "comms", "channels.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("channels.json must not exist after rejected add, stat err=%v", err)
	}
}
