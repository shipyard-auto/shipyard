package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	d := Default()
	if d.Concurrency.DefaultPool != "cli" {
		t.Errorf("default_pool=%q", d.Concurrency.DefaultPool)
	}
	if p, ok := d.Concurrency.Pools["cli"]; !ok || p.Max != 4 {
		t.Errorf("cli pool=%+v ok=%v", p, ok)
	}
	if d.Concurrency.Queue.Strategy != QueueWait {
		t.Errorf("strategy=%q", d.Concurrency.Queue.Strategy)
	}
	if d.Concurrency.Queue.MaxWait != 30*time.Second {
		t.Errorf("max_wait=%v", d.Concurrency.Queue.MaxWait)
	}
	if d.Concurrency.Queue.MaxQueueSize != 16 {
		t.Errorf("max_queue_size=%d", d.Concurrency.Queue.MaxQueueSize)
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr string
		check   func(*testing.T, *Config)
	}{
		{
			name: "empty path",
			path: "",
			check: func(t *testing.T, c *Config) {
				if c.Concurrency.DefaultPool != "cli" {
					t.Errorf("got default_pool=%q", c.Concurrency.DefaultPool)
				}
			},
		},
		{
			name: "missing file",
			path: filepath.Join(t.TempDir(), "absent.yaml"),
			check: func(t *testing.T, c *Config) {
				if c.Concurrency.Queue.MaxWait != 30*time.Second {
					t.Errorf("expected defaults")
				}
			},
		},
		{
			name: "valid",
			path: "testdata/valid.yaml",
			check: func(t *testing.T, c *Config) {
				if _, ok := c.Concurrency.Pools["cli"]; !ok {
					t.Errorf("missing cli pool")
				}
				if _, ok := c.Concurrency.Pools["ollama"]; !ok {
					t.Errorf("missing ollama pool")
				}
				if c.Concurrency.Queue.Strategy != QueueWait {
					t.Errorf("strategy=%q", c.Concurrency.Queue.Strategy)
				}
			},
		},
		{
			name: "minimal",
			path: "testdata/minimal.yaml",
			check: func(t *testing.T, c *Config) {
				if c.Concurrency.Pools["cli"].Max != 8 {
					t.Errorf("cli.max=%d", c.Concurrency.Pools["cli"].Max)
				}
				if c.Concurrency.DefaultPool != "cli" {
					t.Errorf("default_pool=%q", c.Concurrency.DefaultPool)
				}
				if c.Concurrency.Queue.Strategy != QueueWait {
					t.Errorf("strategy=%q", c.Concurrency.Queue.Strategy)
				}
				if c.Concurrency.Queue.MaxWait != 30*time.Second {
					t.Errorf("max_wait=%v", c.Concurrency.Queue.MaxWait)
				}
				if c.Concurrency.Queue.MaxQueueSize != 16 {
					t.Errorf("max_queue_size=%d", c.Concurrency.Queue.MaxQueueSize)
				}
			},
		},
		{name: "invalid strategy", path: "testdata/invalid-strategy.yaml", wantErr: "strategy"},
		{name: "unknown field", path: "testdata/unknown-field.yaml", wantErr: "field"},
		{name: "zero max", path: "testdata/zero-max.yaml", wantErr: "max must be > 0"},
		{name: "malformed", path: "testdata/malformed.yaml", wantErr: "parse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Load(tc.path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

func TestLoad_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, chmod not enforced")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("concurrency: {}\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("want load error, got %v", err)
	}
}

func TestLoad_DefaultPoolNotInPools(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	body := `concurrency:
  default_pool: gpu
  pools:
    cli:
      max: 4
  queue:
    strategy: wait
    max_wait: 30s
    max_queue_size: 16
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "not found in pools") {
		t.Fatalf("want not found error, got %v", err)
	}
}

func TestLoad_ParseMaxWait(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	body := `concurrency:
  default_pool: cli
  pools:
    cli:
      max: 4
  queue:
    strategy: wait
    max_wait: 1m30s
    max_queue_size: 8
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Concurrency.Queue.MaxWait != 90*time.Second {
		t.Errorf("max_wait=%v want 90s", c.Concurrency.Queue.MaxWait)
	}
}

func TestLoad_InvalidMaxWait(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	body := `concurrency:
  default_pool: cli
  pools:
    cli:
      max: 4
  queue:
    strategy: wait
    max_wait: forever
    max_queue_size: 8
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "queue.max_wait") {
		t.Fatalf("want max_wait error, got %v", err)
	}
}

func TestValidate_Concurrency(t *testing.T) {
	tests := []struct {
		name    string
		c       ConcurrencyConfig
		wantErr string
	}{
		{
			name: "ok",
			c: ConcurrencyConfig{
				DefaultPool: "cli",
				Pools:       map[string]PoolConfig{"cli": {Max: 2}},
				Queue:       QueueConfig{Strategy: QueueWait, MaxWait: time.Second, MaxQueueSize: 1},
			},
		},
		{
			name:    "empty default",
			c:       ConcurrencyConfig{Pools: map[string]PoolConfig{"cli": {Max: 2}}},
			wantErr: "default_pool",
		},
		{
			name:    "no pools",
			c:       ConcurrencyConfig{DefaultPool: "cli"},
			wantErr: "at least one pool",
		},
		{
			name: "default not in pools",
			c: ConcurrencyConfig{
				DefaultPool: "gpu",
				Pools:       map[string]PoolConfig{"cli": {Max: 2}},
			},
			wantErr: "not found",
		},
		{
			name: "bad pool",
			c: ConcurrencyConfig{
				DefaultPool: "cli",
				Pools:       map[string]PoolConfig{"cli": {Max: 0}},
			},
			wantErr: "max must be > 0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidate_Pool(t *testing.T) {
	if err := (PoolConfig{Max: 1}).Validate(); err != nil {
		t.Errorf("unexpected: %v", err)
	}
	if err := (PoolConfig{Max: 0}).Validate(); err == nil {
		t.Errorf("expected error")
	}
	if err := (PoolConfig{Max: -1}).Validate(); err == nil {
		t.Errorf("expected error")
	}
}

func TestValidate_Queue(t *testing.T) {
	tests := []struct {
		name    string
		q       QueueConfig
		wantErr string
	}{
		{"ok wait", QueueConfig{Strategy: QueueWait, MaxWait: time.Second, MaxQueueSize: 1}, ""},
		{"ok reject", QueueConfig{Strategy: QueueReject, MaxWait: time.Second, MaxQueueSize: 1}, ""},
		{"bad strategy", QueueConfig{Strategy: "abort", MaxWait: time.Second, MaxQueueSize: 1}, "invalid strategy"},
		{"zero max_wait", QueueConfig{Strategy: QueueWait, MaxWait: 0, MaxQueueSize: 1}, "max_wait"},
		{"zero queue", QueueConfig{Strategy: QueueWait, MaxWait: time.Second, MaxQueueSize: 0}, "max_queue_size"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.q.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}
