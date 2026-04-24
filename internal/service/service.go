package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

var ErrServiceNotFound = errors.New("service not found")

const maxLoggedOutput = 4096

type Service struct {
	Repo      Repository
	Manager   Manager
	IDGen     IDGenerator
	Now       func() time.Time
	StorePath string
	Logger    *slog.Logger
}

func NewService() (Service, error) {
	repo, err := NewFileRepository()
	if err != nil {
		return Service{}, err
	}
	manager, err := NewManager()
	if err != nil {
		return Service{}, err
	}
	return Service{
		Repo:      repo,
		Manager:   manager,
		IDGen:     RandomIDGenerator{},
		Now:       time.Now,
		StorePath: repo.Path,
		Logger:    yardlogs.DefaultLogger(yardlogs.SourceService),
	}, nil
}

func (s Service) List() ([]ServiceRecord, error) {
	store, err := s.Repo.Load()
	if err != nil {
		return nil, err
	}
	records := slices.Clone(store.Services)
	slices.SortFunc(records, func(a, b ServiceRecord) int { return strings.Compare(a.ID, b.ID) })
	return records, nil
}

func (s Service) Get(id string) (ServiceRecord, error) {
	store, err := s.Repo.Load()
	if err != nil {
		return ServiceRecord{}, err
	}
	targetID := strings.ToUpper(strings.TrimSpace(id))
	for _, record := range store.Services {
		if record.ID == targetID {
			return record, nil
		}
	}
	return ServiceRecord{}, ErrServiceNotFound
}

func (s Service) Add(input ServiceInput) (ServiceRecord, error) {
	if err := validateAddInput(input); err != nil {
		return ServiceRecord{}, err
	}
	store, err := s.Repo.Load()
	if err != nil {
		return ServiceRecord{}, err
	}
	existingIDs := make(map[string]struct{}, len(store.Services))
	for _, record := range store.Services {
		existingIDs[record.ID] = struct{}{}
	}
	id, err := s.IDGen.NewID(existingIDs)
	if err != nil {
		return ServiceRecord{}, err
	}
	now := s.Now().UTC()
	record := ServiceRecord{
		ID:          id,
		Name:        strings.TrimSpace(derefString(input.Name)),
		Description: strings.TrimSpace(derefString(input.Description)),
		Command:     strings.TrimSpace(derefString(input.Command)),
		WorkingDir:  strings.TrimSpace(derefString(input.WorkingDir)),
		Environment: cloneEnvironment(derefEnvironment(input.Environment)),
		AutoRestart: derefBool(input.AutoRestart, false),
		Enabled:     derefBool(input.Enabled, true),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	next := store
	next.Services = append(next.Services, record)
	if err := s.persist(store, next); err != nil {
		s.logPersistFailure(yardlogs.EventServiceCreateFailed, "Failed to create Shipyard service", record, err)
		return ServiceRecord{}, err
	}
	if record.Enabled {
		if err := s.Manager.Enable(record.ID); err != nil {
			s.logFailure(yardlogs.EventServiceCreateFailed, "Failed to create Shipyard service", record, err)
			return record, err
		}
	}
	s.logRecord(slog.LevelInfo, yardlogs.EventServiceCreated, "Shipyard service created", record)
	return record, nil
}

func (s Service) Update(id string, patch ServiceInput) (ServiceRecord, error) {
	store, err := s.Repo.Load()
	if err != nil {
		return ServiceRecord{}, err
	}
	targetID := strings.ToUpper(strings.TrimSpace(id))
	index := -1
	for i, record := range store.Services {
		if record.ID == targetID {
			index = i
			break
		}
	}
	if index == -1 {
		return ServiceRecord{}, ErrServiceNotFound
	}
	before := store.Services[index]
	after := before
	applyPatch(&after, patch)
	if err := validateStoredService(after); err != nil {
		return ServiceRecord{}, err
	}
	after.UpdatedAt = s.Now().UTC()
	next := store
	next.Services[index] = after
	if err := s.persist(store, next); err != nil {
		s.logPersistFailure(yardlogs.EventServiceUpdateFailed, "Failed to update Shipyard service", after, err)
		return ServiceRecord{}, err
	}
	if before.Enabled != after.Enabled {
		if after.Enabled {
			if err := s.Manager.Enable(after.ID); err != nil {
				s.logFailure(yardlogs.EventServiceUpdateFailed, "Failed to update Shipyard service", after, err)
				return after, err
			}
		} else {
			if err := s.Manager.Disable(after.ID); err != nil {
				s.logFailure(yardlogs.EventServiceUpdateFailed, "Failed to update Shipyard service", after, err)
				return after, err
			}
		}
	}
	eventName, message := classifyUpdateEvent(before, after)
	s.logRecord(slog.LevelInfo, eventName, message, after)
	return after, nil
}

func (s Service) Delete(id string) error {
	store, err := s.Repo.Load()
	if err != nil {
		return err
	}
	targetID := strings.ToUpper(strings.TrimSpace(id))
	nextRecords := make([]ServiceRecord, 0, len(store.Services))
	found := false
	var removed ServiceRecord
	for _, record := range store.Services {
		if record.ID == targetID {
			found = true
			removed = record
			continue
		}
		nextRecords = append(nextRecords, record)
	}
	if !found {
		return ErrServiceNotFound
	}
	next := store
	next.Services = nextRecords
	if err := s.persist(store, next); err != nil {
		s.logPersistFailure(yardlogs.EventServiceDeleteFailed, "Failed to delete Shipyard service", removed, err)
		return err
	}
	if err := s.Manager.Remove(removed.ID); err != nil {
		s.logFailure(yardlogs.EventServiceDeleteFailed, "Failed to delete Shipyard service", removed, err)
		return err
	}
	s.logRecord(slog.LevelInfo, yardlogs.EventServiceDeleted, "Shipyard service deleted", removed)
	return nil
}

func (s Service) Enable(id string) (ServiceRecord, error) {
	return s.Update(id, ServiceInput{Enabled: boolptr(true)})
}

func (s Service) Disable(id string) (ServiceRecord, error) {
	return s.Update(id, ServiceInput{Enabled: boolptr(false)})
}

func (s Service) Start(id string) (ServiceRecord, error) {
	record, err := s.Get(id)
	if err != nil {
		return ServiceRecord{}, err
	}
	if err := s.Manager.Start(record.ID); err != nil {
		s.logFailure(yardlogs.EventServiceStartFailed, "Failed to start Shipyard service", record, err)
		return ServiceRecord{}, err
	}
	s.logRecord(slog.LevelInfo, yardlogs.EventServiceStarted, "Shipyard service started", record)
	return record, nil
}

func (s Service) Stop(id string) (ServiceRecord, error) {
	record, err := s.Get(id)
	if err != nil {
		return ServiceRecord{}, err
	}
	if err := s.Manager.Stop(record.ID); err != nil {
		s.logFailure(yardlogs.EventServiceStopFailed, "Failed to stop Shipyard service", record, err)
		return ServiceRecord{}, err
	}
	s.logRecord(slog.LevelInfo, yardlogs.EventServiceStopped, "Shipyard service stopped", record)
	return record, nil
}

func (s Service) Restart(id string) (ServiceRecord, error) {
	record, err := s.Get(id)
	if err != nil {
		return ServiceRecord{}, err
	}
	if err := s.Manager.Restart(record.ID); err != nil {
		s.logFailure(yardlogs.EventServiceRestartFailed, "Failed to restart Shipyard service", record, err)
		return ServiceRecord{}, err
	}
	s.logRecord(slog.LevelInfo, yardlogs.EventServiceRestarted, "Shipyard service restarted", record)
	return record, nil
}

func (s Service) Status(id string) (ServiceRecord, RuntimeStatus, error) {
	record, err := s.Get(id)
	if err != nil {
		return ServiceRecord{}, RuntimeStatus{}, err
	}
	status, err := s.Manager.Status(record.ID)
	if err != nil {
		return ServiceRecord{}, RuntimeStatus{}, err
	}
	return record, status, nil
}

func (s Service) LoadInputFile(path string) (ServiceInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ServiceInput{}, fmt.Errorf("read service input file: %w", err)
	}
	var input ServiceInput
	if err := json.Unmarshal(data, &input); err != nil {
		return ServiceInput{}, fmt.Errorf("parse service input file: %w", err)
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
	if err := s.Manager.Sync(next.Services); err != nil {
		_ = s.Repo.Save(previous)
		return err
	}
	if err := s.Manager.Reload(); err != nil {
		_ = s.Repo.Save(previous)
		_ = s.Manager.Sync(previous.Services)
		return err
	}
	return nil
}

func applyPatch(record *ServiceRecord, patch ServiceInput) {
	if patch.Name != nil {
		record.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Description != nil {
		record.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Command != nil {
		record.Command = strings.TrimSpace(*patch.Command)
	}
	if patch.WorkingDir != nil {
		record.WorkingDir = strings.TrimSpace(*patch.WorkingDir)
	}
	if patch.Environment != nil {
		record.Environment = cloneEnvironment(*patch.Environment)
	}
	if patch.AutoRestart != nil {
		record.AutoRestart = *patch.AutoRestart
	}
	if patch.Enabled != nil {
		record.Enabled = *patch.Enabled
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

func derefEnvironment(value *map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	return *value
}

func cloneEnvironment(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func boolptr(value bool) *bool { return &value }

func (s Service) logger() *slog.Logger {
	if s.Logger == nil {
		return slog.New(yardlogs.NopHandler())
	}
	return s.Logger
}

func recordAttrs(record ServiceRecord, extras ...slog.Attr) []slog.Attr {
	attrs := make([]slog.Attr, 0, 3+len(extras))
	attrs = append(attrs, yardlogs.EntityAttrs(yardlogs.EntityService, record.ID, record.Name)...)
	attrs = append(attrs, extras...)
	return attrs
}

func (s Service) logRecord(level slog.Level, event, message string, record ServiceRecord) {
	s.logger().LogAttrs(context.Background(), level, event,
		recordAttrs(record, slog.String(yardlogs.KeyMessage, message))...,
	)
}

func (s Service) logFailure(event, message string, record ServiceRecord, err error) {
	s.logger().LogAttrs(context.Background(), slog.LevelError, event,
		recordAttrs(record,
			slog.String(yardlogs.KeyMessage, message),
			slog.String(yardlogs.KeyError, err.Error()),
			slog.String(yardlogs.KeyErrorKind, fmt.Sprintf("%T", err)),
		)...,
	)
}

// Persist failures are functionally identical to operational failures —
// kept as a separate name only for call-site readability.
func (s Service) logPersistFailure(event, message string, record ServiceRecord, err error) {
	s.logFailure(event, message, record, err)
}

func classifyUpdateEvent(before, after ServiceRecord) (string, string) {
	if before.Enabled != after.Enabled {
		if after.Enabled {
			return yardlogs.EventServiceEnabled, "Shipyard service enabled"
		}
		return yardlogs.EventServiceDisabled, "Shipyard service disabled"
	}
	return yardlogs.EventServiceUpdated, "Shipyard service updated"
}
