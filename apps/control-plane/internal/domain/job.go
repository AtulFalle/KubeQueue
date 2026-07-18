package domain

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

type State string

const (
	StateCreated   State = "CREATED"
	StateQueued    State = "QUEUED"
	StateRunning   State = "RUNNING"
	StatePaused    State = "PAUSED"
	StateCompleted State = "COMPLETED"
	StateFailed    State = "FAILED"
	StateCancelled State = "CANCELLED"
)

var ErrInvalidTransition = errors.New("invalid lifecycle transition")
var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

type Job struct {
	ID            string          `json:"id"`
	ParentID      string          `json:"parentId,omitempty"`
	Name          string          `json:"name"`
	Namespace     string          `json:"namespace"`
	Team          string          `json:"team,omitempty"`
	Priority      int             `json:"priority"`
	Position      int64           `json:"position"`
	DesiredState  State           `json:"desiredState"`
	ObservedState State           `json:"observedState"`
	ScheduledFor  *time.Time      `json:"scheduledFor,omitempty"`
	KubernetesUID string          `json:"kubernetesUid,omitempty"`
	Template      json.RawMessage `json:"template"`
	Attempt       int             `json:"attempt"`
	Version       int64           `json:"version"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

type Event struct {
	ID        int64           `json:"id"`
	JobID     string          `json:"jobId"`
	Type      string          `json:"type"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type CreateJob struct {
	Name         string
	Namespace    string
	Team         string
	Priority     int
	ScheduledFor *time.Time
	Template     json.RawMessage
	ParentID     string
	Attempt      int
}

func (c CreateJob) Validate() error {
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Namespace) == "" {
		return errors.New("name and namespace are required")
	}
	if len(c.Name) > 63 || !dnsLabel.MatchString(c.Name) {
		return errors.New("name must be a valid Kubernetes DNS label")
	}
	if len(c.Namespace) > 63 || !dnsLabel.MatchString(c.Namespace) {
		return errors.New("namespace must be a valid Kubernetes DNS label")
	}
	if len(c.Template) == 0 || !json.Valid(c.Template) ||
		!strings.HasPrefix(strings.TrimSpace(string(c.Template)), "{") {
		return errors.New("template must be a valid JSON object")
	}
	var template struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Spec       struct {
			Template struct {
				Spec struct {
					RestartPolicy string            `json:"restartPolicy"`
					Containers    []json.RawMessage `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(c.Template, &template); err != nil {
		return errors.New("template must be a valid Kubernetes Job")
	}
	if template.APIVersion != "" && template.APIVersion != "batch/v1" {
		return errors.New("template apiVersion must be batch/v1")
	}
	if template.Kind != "" && template.Kind != "Job" {
		return errors.New("template kind must be Job")
	}
	if len(template.Spec.Template.Spec.Containers) == 0 {
		return errors.New("template must include at least one container")
	}
	if template.Spec.Template.Spec.RestartPolicy != "Never" &&
		template.Spec.Template.Spec.RestartPolicy != "OnFailure" {
		return errors.New("template restartPolicy must be Never or OnFailure")
	}
	if c.Priority < -1000 || c.Priority > 1000 {
		return errors.New("priority must be between -1000 and 1000")
	}
	return nil
}

func CanTransition(from, to State) bool {
	if from == to {
		return true
	}
	switch to {
	case StateCreated:
		return false
	case StateCancelled:
		return from != StateCompleted && from != StateCancelled
	case StateQueued:
		return from == StateCreated || from == StatePaused || from == StateFailed
	case StatePaused:
		return from == StateQueued || from == StateRunning
	case StateRunning:
		return from == StateQueued || from == StatePaused
	case StateCompleted, StateFailed:
		return from == StateRunning
	default:
		return false
	}
}

func (j Job) Terminal() bool {
	return j.ObservedState == StateCompleted || j.ObservedState == StateFailed ||
		j.DesiredState == StateCancelled
}

func (s State) Valid() bool {
	switch s {
	case StateCreated, StateQueued, StateRunning, StatePaused, StateCompleted, StateFailed,
		StateCancelled:
		return true
	default:
		return false
	}
}
