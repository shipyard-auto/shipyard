package logs

import "time"

const (
	DefaultRetentionDays = 14
	DefaultSourceCron    = "cron"
)

type Config struct {
	RetentionDays int `json:"retentionDays"`
}

type Event struct {
	Timestamp  time.Time      `json:"ts"`
	Source     string         `json:"source"`
	Level      string         `json:"level"`
	Event      string         `json:"event"`
	Message    string         `json:"message"`
	EntityType string         `json:"entityType"`
	EntityID   string         `json:"entityId"`
	EntityName string         `json:"entityName"`
	RunID      string         `json:"runId,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

type Query struct {
	Source string
	Entity string
	Level  string
	Limit  int
}

type SourceSummary struct {
	Source     string
	Files      int
	SizeBytes  int64
	NewestFile string
}

type PruneResult struct {
	DeletedFiles int
	FreedBytes   int64
}
