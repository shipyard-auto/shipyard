package cron

import "time"

const storeVersion = 1

type Store struct {
	Version int   `json:"version"`
	Jobs    []Job `json:"jobs"`
}

type Job struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Schedule    string    `json:"schedule"`
	Command     string    `json:"command"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type JobInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Schedule    *string `json:"schedule"`
	Command     *string `json:"command"`
	Enabled     *bool   `json:"enabled"`
}
