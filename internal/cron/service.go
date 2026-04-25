package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

var ErrJobNotFound = errors.New("cron job not found")

const maxLoggedOutput = 4096

type Service struct {
	Repo      Repository
	Crontab   Crontab
	IDGen     IDGenerator
	Now       func() time.Time
	StorePath string
	Exec      func(name string, args ...string) *exec.Cmd
	Logger    *slog.Logger
}

func NewService() (Service, error) {
	repo, err := NewFileRepository()
	if err != nil {
		return Service{}, err
	}

	return Service{
		Repo:      repo,
		Crontab:   SystemCrontab{},
		IDGen:     RandomIDGenerator{},
		Now:       time.Now,
		StorePath: repo.Path,
		Exec:      exec.Command,
		Logger:    yardlogs.DefaultLogger(yardlogs.SourceCron),
	}, nil
}

func (s Service) List() ([]Job, error) {
	store, err := s.Repo.Load()
	if err != nil {
		return nil, err
	}

	jobs := slices.Clone(store.Jobs)
	slices.SortFunc(jobs, func(a, b Job) int {
		return strings.Compare(a.ID, b.ID)
	})

	return jobs, nil
}

func (s Service) Get(id string) (Job, error) {
	store, err := s.Repo.Load()
	if err != nil {
		return Job{}, err
	}

	for _, job := range store.Jobs {
		if job.ID == strings.ToUpper(strings.TrimSpace(id)) {
			return job, nil
		}
	}

	return Job{}, ErrJobNotFound
}

func (s Service) Add(input JobInput) (Job, error) {
	if err := validateAddInput(input); err != nil {
		return Job{}, err
	}

	store, err := s.Repo.Load()
	if err != nil {
		return Job{}, err
	}

	existingIDs := make(map[string]struct{}, len(store.Jobs))
	for _, job := range store.Jobs {
		existingIDs[job.ID] = struct{}{}
	}

	id, err := s.IDGen.NewID(existingIDs)
	if err != nil {
		return Job{}, err
	}

	now := s.Now().UTC()
	job := Job{
		ID:          id,
		Name:        strings.TrimSpace(derefString(input.Name)),
		Description: strings.TrimSpace(derefString(input.Description)),
		Schedule:    strings.TrimSpace(derefString(input.Schedule)),
		Command:     strings.TrimSpace(derefString(input.Command)),
		Enabled:     derefBool(input.Enabled, true),
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	next := store
	next.Jobs = append(next.Jobs, job)
	if err := s.persist(store, next); err != nil {
		s.logFailure(yardlogs.EventCronJobCreateFailed, "Failed to create Shipyard cron job", job, "", err)
		return Job{}, err
	}

	s.logJob(slog.LevelInfo, yardlogs.EventCronJobCreated, "Shipyard cron job created", job, "")
	return job, nil
}

func (s Service) Update(id string, patch JobInput) (Job, error) {
	store, err := s.Repo.Load()
	if err != nil {
		return Job{}, err
	}

	targetID := strings.ToUpper(strings.TrimSpace(id))
	index := -1
	for i, job := range store.Jobs {
		if job.ID == targetID {
			index = i
			break
		}
	}
	if index == -1 {
		return Job{}, ErrJobNotFound
	}

	before := store.Jobs[index]
	after := before
	applyPatch(&after, patch)
	if err := validateStoredJob(after); err != nil {
		return Job{}, err
	}
	after.UpdatedAt = s.Now().UTC()

	next := store
	next.Jobs[index] = after
	if err := s.persist(store, next); err != nil {
		s.logFailure(yardlogs.EventCronJobUpdateFailed, "Failed to update Shipyard cron job", after, "", err)
		return Job{}, err
	}

	eventName, message := classifyUpdateEvent(before, after)
	s.logJob(slog.LevelInfo, eventName, message, after, "")
	return after, nil
}

func (s Service) Delete(id string) error {
	store, err := s.Repo.Load()
	if err != nil {
		return err
	}

	targetID := strings.ToUpper(strings.TrimSpace(id))
	nextJobs := make([]Job, 0, len(store.Jobs))
	found := false
	var removed Job
	for _, job := range store.Jobs {
		if job.ID == targetID {
			found = true
			removed = job
			continue
		}
		nextJobs = append(nextJobs, job)
	}
	if !found {
		return ErrJobNotFound
	}

	next := store
	next.Jobs = nextJobs
	if err := s.persist(store, next); err != nil {
		s.logFailure(yardlogs.EventCronJobDeleteFailed, "Failed to delete Shipyard cron job", removed, "", err)
		return err
	}

	s.logJob(slog.LevelInfo, yardlogs.EventCronJobDeleted, "Shipyard cron job deleted", removed, "")
	return nil
}

func (s Service) Enable(id string) (Job, error) {
	return s.Update(id, JobInput{Enabled: boolptr(true)})
}

func (s Service) Disable(id string) (Job, error) {
	return s.Update(id, JobInput{Enabled: boolptr(false)})
}

func (s Service) Run(ctx context.Context, id string) (Job, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = yardlogs.EnsureTraceID(ctx)

	job, err := s.Get(id)
	if err != nil {
		return Job{}, "", err
	}

	runID, err := randomID(8)
	if err != nil {
		return Job{}, "", err
	}
	startedAt := s.Now().UTC()
	s.logger().LogAttrs(ctx, slog.LevelInfo, yardlogs.EventCronJobRunStarted,
		jobAttrs(job, runID, slog.String("schedule", job.Schedule))...,
	)

	cmd := s.Exec("/bin/sh", "-lc", job.Command)
	output, err := cmd.CombinedOutput()
	durationMs := s.Now().UTC().Sub(startedAt).Milliseconds()
	text, truncated := truncateOutput(string(output))
	if err != nil {
		s.logger().LogAttrs(ctx, slog.LevelError, yardlogs.EventCronJobRunFailed,
			jobAttrs(job, runID,
				slog.Int64(yardlogs.KeyDurationMs, durationMs),
				slog.String("output", text),
				slog.Bool("output_truncated", truncated),
				slog.String(yardlogs.KeyError, err.Error()),
				slog.String(yardlogs.KeyErrorKind, fmt.Sprintf("%T", err)),
			)...,
		)
		return job, string(output), fmt.Errorf("run cron job %s: %w", job.ID, err)
	}

	s.logger().LogAttrs(ctx, slog.LevelInfo, yardlogs.EventCronJobRunFinished,
		jobAttrs(job, runID,
			slog.Int64(yardlogs.KeyDurationMs, durationMs),
			slog.String("output", text),
			slog.Bool("output_truncated", truncated),
		)...,
	)
	return job, string(output), nil
}

func (s Service) LoadInputFile(path string) (JobInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return JobInput{}, fmt.Errorf("read cron input file: %w", err)
	}

	var input JobInput
	if err := json.Unmarshal(data, &input); err != nil {
		return JobInput{}, fmt.Errorf("parse cron input file: %w", err)
	}

	return input, nil
}

func (s Service) persist(previous, next Store) error {
	if err := validateStore(next); err != nil {
		return err
	}

	if err := s.Repo.Save(next); err != nil {
		return err
	}

	existing, err := s.Crontab.Read()
	if err != nil {
		_ = s.Repo.Save(previous)
		return err
	}

	rendered := renderCrontab(existing, next.Jobs)
	if err := s.Crontab.Write(rendered); err != nil {
		_ = s.Repo.Save(previous)
		return err
	}

	return nil
}

func applyPatch(job *Job, patch JobInput) {
	if patch.Name != nil {
		job.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Description != nil {
		job.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Schedule != nil {
		job.Schedule = strings.TrimSpace(*patch.Schedule)
	}
	if patch.Command != nil {
		job.Command = strings.TrimSpace(*patch.Command)
	}
	if patch.Enabled != nil {
		job.Enabled = *patch.Enabled
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func boolptr(value bool) *bool {
	return &value
}

func (s Service) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.New(yardlogs.NopHandler())
	}
	return s.Logger
}

// jobAttrs assembles the standard attribute set for a cron job event,
// optionally extended with extras.
func jobAttrs(job Job, runID string, extras ...slog.Attr) []slog.Attr {
	attrs := make([]slog.Attr, 0, 4+len(extras))
	attrs = append(attrs, yardlogs.EntityAttrs(yardlogs.EntityCronJob, job.ID, job.Name)...)
	if runID != "" {
		attrs = append(attrs, slog.String(yardlogs.KeyRunID, runID))
	}
	attrs = append(attrs, extras...)
	return attrs
}

func (s Service) logJob(level slog.Level, event, message string, job Job, runID string) {
	s.logger().LogAttrs(context.Background(), level, event,
		jobAttrs(job, runID, slog.String(yardlogs.KeyMessage, message))...,
	)
}

func (s Service) logFailure(event, message string, job Job, runID string, err error) {
	s.logger().LogAttrs(context.Background(), slog.LevelError, event,
		jobAttrs(job, runID,
			slog.String(yardlogs.KeyMessage, message),
			slog.String(yardlogs.KeyError, err.Error()),
			slog.String(yardlogs.KeyErrorKind, fmt.Sprintf("%T", err)),
		)...,
	)
}

func classifyUpdateEvent(before, after Job) (string, string) {
	if before.Enabled != after.Enabled {
		if after.Enabled {
			return yardlogs.EventCronJobEnabled, "Shipyard cron job enabled"
		}
		return yardlogs.EventCronJobDisabled, "Shipyard cron job disabled"
	}
	return yardlogs.EventCronJobUpdated, "Shipyard cron job updated"
}

func truncateOutput(output string) (text string, truncated bool) {
	clean := strings.TrimSpace(output)
	if len(clean) <= maxLoggedOutput {
		return clean, false
	}
	return clean[:maxLoggedOutput], true
}
