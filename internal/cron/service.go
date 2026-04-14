package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

var ErrJobNotFound = errors.New("cron job not found")

type Service struct {
	Repo      Repository
	Crontab   Crontab
	IDGen     IDGenerator
	Now       func() time.Time
	StorePath string
	Exec      func(name string, args ...string) *exec.Cmd
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
		return Job{}, err
	}

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

	job := store.Jobs[index]
	applyPatch(&job, patch)
	if err := validateStoredJob(job); err != nil {
		return Job{}, err
	}
	job.UpdatedAt = s.Now().UTC()

	next := store
	next.Jobs[index] = job
	if err := s.persist(store, next); err != nil {
		return Job{}, err
	}

	return job, nil
}

func (s Service) Delete(id string) error {
	store, err := s.Repo.Load()
	if err != nil {
		return err
	}

	targetID := strings.ToUpper(strings.TrimSpace(id))
	nextJobs := make([]Job, 0, len(store.Jobs))
	found := false
	for _, job := range store.Jobs {
		if job.ID == targetID {
			found = true
			continue
		}
		nextJobs = append(nextJobs, job)
	}
	if !found {
		return ErrJobNotFound
	}

	next := store
	next.Jobs = nextJobs
	return s.persist(store, next)
}

func (s Service) Enable(id string) (Job, error) {
	return s.Update(id, JobInput{Enabled: boolptr(true)})
}

func (s Service) Disable(id string) (Job, error) {
	return s.Update(id, JobInput{Enabled: boolptr(false)})
}

func (s Service) Run(id string) (Job, string, error) {
	job, err := s.Get(id)
	if err != nil {
		return Job{}, "", err
	}

	cmd := s.Exec("/bin/sh", "-lc", job.Command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return job, string(output), fmt.Errorf("run cron job %s: %w", job.ID, err)
	}

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
