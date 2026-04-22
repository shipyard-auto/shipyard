// Package config loads the crew addon global config from
// ~/.shipyard/crew/config.yaml. Missing file → Default().
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Concurrency ConcurrencyConfig `yaml:"concurrency"`
}

type ConcurrencyConfig struct {
	DefaultPool string                `yaml:"default_pool"`
	Pools       map[string]PoolConfig `yaml:"pools"`
	Queue       QueueConfig           `yaml:"queue"`
}

type PoolConfig struct {
	Max int `yaml:"max"`
}

type QueueStrategy string

const (
	QueueWait   QueueStrategy = "wait"
	QueueReject QueueStrategy = "reject"
)

type QueueConfig struct {
	Strategy     QueueStrategy `yaml:"strategy"`
	MaxWait      time.Duration `yaml:"max_wait"`
	MaxQueueSize int           `yaml:"max_queue_size"`
}

func (q *QueueConfig) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Strategy     QueueStrategy `yaml:"strategy"`
		MaxWait      string        `yaml:"max_wait"`
		MaxQueueSize int           `yaml:"max_queue_size"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	q.Strategy = raw.Strategy
	q.MaxQueueSize = raw.MaxQueueSize
	if raw.MaxWait == "" {
		q.MaxWait = 0
		return nil
	}
	d, err := time.ParseDuration(raw.MaxWait)
	if err != nil {
		return fmt.Errorf("queue.max_wait: %w", err)
	}
	q.MaxWait = d
	return nil
}

func Default() *Config {
	return &Config{
		Concurrency: ConcurrencyConfig{
			DefaultPool: "cli",
			Pools: map[string]PoolConfig{
				"cli": {Max: 4},
			},
			Queue: QueueConfig{
				Strategy:     QueueWait,
				MaxWait:      30 * time.Second,
				MaxQueueSize: 16,
			},
		},
	}
}

func Load(path string) (*Config, error) {
	if path == "" {
		return Default(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return nil, fmt.Errorf("load config %s: %w", path, err)
	}

	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("load config %s: parse: %w", path, err)
	}

	d := Default()
	if c.Concurrency.DefaultPool == "" {
		c.Concurrency.DefaultPool = d.Concurrency.DefaultPool
	}
	if len(c.Concurrency.Pools) == 0 {
		c.Concurrency.Pools = d.Concurrency.Pools
	}
	if c.Concurrency.Queue.Strategy == "" {
		c.Concurrency.Queue.Strategy = d.Concurrency.Queue.Strategy
	}
	if c.Concurrency.Queue.MaxWait == 0 {
		c.Concurrency.Queue.MaxWait = d.Concurrency.Queue.MaxWait
	}
	if c.Concurrency.Queue.MaxQueueSize == 0 {
		c.Concurrency.Queue.MaxQueueSize = d.Concurrency.Queue.MaxQueueSize
	}

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("load config %s: validate: %w", path, err)
	}
	return &c, nil
}

func (c Config) Validate() error {
	return c.Concurrency.Validate()
}

func (c ConcurrencyConfig) Validate() error {
	if c.DefaultPool == "" {
		return errors.New("concurrency: default_pool must be non-empty")
	}
	if len(c.Pools) == 0 {
		return errors.New("concurrency: at least one pool required")
	}
	if _, ok := c.Pools[c.DefaultPool]; !ok {
		return fmt.Errorf("concurrency: default_pool %q not found in pools", c.DefaultPool)
	}
	for name, p := range c.Pools {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("concurrency.pools[%q]: %w", name, err)
		}
	}
	if err := c.Queue.Validate(); err != nil {
		return fmt.Errorf("concurrency.queue: %w", err)
	}
	return nil
}

func (p PoolConfig) Validate() error {
	if p.Max <= 0 {
		return fmt.Errorf("pool: max must be > 0, got %d", p.Max)
	}
	return nil
}

func (q QueueConfig) Validate() error {
	switch q.Strategy {
	case QueueWait, QueueReject:
	default:
		return fmt.Errorf("invalid strategy %q: must be \"wait\" or \"reject\"", q.Strategy)
	}
	if q.MaxWait <= 0 {
		return fmt.Errorf("max_wait must be > 0, got %s", q.MaxWait)
	}
	if q.MaxQueueSize <= 0 {
		return fmt.Errorf("max_queue_size must be > 0, got %d", q.MaxQueueSize)
	}
	return nil
}
