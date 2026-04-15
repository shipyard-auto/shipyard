package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
)

var ErrServiceNotFound = errors.New("service not found")

const maxLoggedOutput = 4096

type EventLogger interface {
	Write(event yardlogs.Event) error
}

type Service struct {
	Repo      Repository
	Manager   Manager
	IDGen     IDGenerator
	Now       func() time.Time
	StorePath string
	Logger    EventLogger
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
		Logger:    newLogger(),
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
		s.logPersistFailure("service_create_failed", "Failed to create Shipyard service", record, err)
		return ServiceRecord{}, err
	}
	if record.Enabled {
		if err := s.Manager.Enable(record.ID); err != nil {
			s.logFailure("service_create_failed", "Failed to create Shipyard service", record, err)
			return record, err
		}
	}
	s.logEvent("info", "service_created", "Shipyard service created", record, nil)
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
		s.logPersistFailure("service_update_failed", "Failed to update Shipyard service", after, err)
		return ServiceRecord{}, err
	}
	if before.Enabled != after.Enabled {
		if after.Enabled {
			if err := s.Manager.Enable(after.ID); err != nil {
				s.logFailure("service_update_failed", "Failed to update Shipyard service", after, err)
				return after, err
			}
		} else {
			if err := s.Manager.Disable(after.ID); err != nil {
				s.logFailure("service_update_failed", "Failed to update Shipyard service", after, err)
				return after, err
			}
		}
	}
	eventName, message := classifyUpdateEvent(before, after)
	s.logEvent("info", eventName, message, after, nil)
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
		s.logPersistFailure("service_delete_failed", "Failed to delete Shipyard service", removed, err)
		return err
	}
	if err := s.Manager.Remove(removed.ID); err != nil {
		s.logFailure("service_delete_failed", "Failed to delete Shipyard service", removed, err)
		return err
	}
	s.logEvent("info", "service_deleted", "Shipyard service deleted", removed, nil)
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
		s.logFailure("service_start_failed", "Failed to start Shipyard service", record, err)
		return ServiceRecord{}, err
	}
	s.logEvent("info", "service_started", "Shipyard service started", record, nil)
	return record, nil
}

func (s Service) Stop(id string) (ServiceRecord, error) {
	record, err := s.Get(id)
	if err != nil {
		return ServiceRecord{}, err
	}
	if err := s.Manager.Stop(record.ID); err != nil {
		s.logFailure("service_stop_failed", "Failed to stop Shipyard service", record, err)
		return ServiceRecord{}, err
	}
	s.logEvent("info", "service_stopped", "Shipyard service stopped", record, nil)
	return record, nil
}

func (s Service) Restart(id string) (ServiceRecord, error) {
	record, err := s.Get(id)
	if err != nil {
		return ServiceRecord{}, err
	}
	if err := s.Manager.Restart(record.ID); err != nil {
		s.logFailure("service_restart_failed", "Failed to restart Shipyard service", record, err)
		return ServiceRecord{}, err
	}
	s.logEvent("info", "service_restarted", "Shipyard service restarted", record, nil)
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

func loggableError(err error) map[string]any {
	return map[string]any{"error": err.Error()}
}

func (s Service) logPersistFailure(eventName, message string, record ServiceRecord, err error) {
	s.logEvent("error", eventName, message, record, loggableError(err))
}

func (s Service) logFailure(eventName, message string, record ServiceRecord, err error) {
	s.logEvent("error", eventName, message, record, loggableError(err))
}

func (s Service) logEvent(level, eventName, message string, record ServiceRecord, data map[string]any) {
	if s.Logger == nil {
		return
	}
	_ = s.Logger.Write(yardlogs.Event{
		Timestamp:  s.Now().UTC(),
		Source:     yardlogs.DefaultSourceService,
		Level:      level,
		Event:      eventName,
		Message:    message,
		EntityType: "service",
		EntityID:   record.ID,
		EntityName: record.Name,
		Data:       data,
	})
}

func classifyUpdateEvent(before, after ServiceRecord) (string, string) {
	if before.Enabled != after.Enabled {
		if after.Enabled {
			return "service_enabled", "Shipyard service enabled"
		}
		return "service_disabled", "Shipyard service disabled"
	}
	return "service_updated", "Shipyard service updated"
}

func newLogger() EventLogger {
	service, err := yardlogs.NewService()
	if err != nil {
		return nil
	}
	return service
}
