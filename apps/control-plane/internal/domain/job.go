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

type ManagementMode string

const (
	ManagementManaged    ManagementMode = "MANAGED"
	ManagementObserved   ManagementMode = "OBSERVED"
	ManagementIgnored    ManagementMode = "IGNORED"
	ManagementConflicted ManagementMode = "CONFLICTED"
)

type SyncStatus string

const (
	SyncStatusSynced     SyncStatus = "SYNCED"
	SyncStatusPending    SyncStatus = "PENDING"
	SyncStatusMissing    SyncStatus = "MISSING"
	SyncStatusStale      SyncStatus = "STALE"
	SyncStatusError      SyncStatus = "ERROR"
	SyncStatusOutOfScope SyncStatus = "OUT_OF_SCOPE"
	SyncStatusConflicted SyncStatus = "CONFLICTED"
)

var (
	ErrInvalidTransition   = errors.New("invalid lifecycle transition")
	ErrUnmanagedJob        = errors.New("job is not managed by KubeQueue")
	ErrNotArchivable       = errors.New("job is not stale and cannot be archived")
	ErrIdempotencyConflict = errors.New("idempotency key was already used for different Job intent")
)
var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)
var idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)

type Job struct {
	ID                 string             `json:"id"`
	ParentID           string             `json:"parentId,omitempty"`
	ProjectID          ProjectID          `json:"projectId"`
	NamespaceBindingID NamespaceBindingID `json:"namespaceBindingId"`
	CreatorPrincipalID PrincipalID        `json:"creatorPrincipalId"`
	SubmissionSource   SubmissionSource   `json:"submissionSource"`
	IdempotencyKey     string             `json:"-"`
	Name               string             `json:"name"`
	Namespace          string             `json:"namespace"`
	Team               string             `json:"team,omitempty"`
	Priority           int                `json:"priority"`
	Position           int64              `json:"position"`
	DesiredState       State              `json:"desiredState"`
	ObservedState      State              `json:"observedState"`
	ManagementMode     ManagementMode     `json:"managementMode"`
	SyncStatus         SyncStatus         `json:"syncStatus"`
	ActionPending      bool               `json:"actionPending"`
	ObservedReason     string             `json:"observedReason,omitempty"`
	ObservedMessage    string             `json:"observedMessage,omitempty"`
	ObservedAt         *time.Time         `json:"observedAt,omitempty"`
	LastError          string             `json:"lastError,omitempty"`
	LastErrorCode      string             `json:"lastErrorCode,omitempty"`
	ErrorRemediation   string             `json:"errorRemediation,omitempty"`
	ScheduledFor       *time.Time         `json:"scheduledFor,omitempty"`
	KubernetesUID      string             `json:"kubernetesUid,omitempty"`
	Template           json.RawMessage    `json:"template"`
	Attempt            int                `json:"attempt"`
	Version            int64              `json:"version"`
	CreatedAt          time.Time          `json:"createdAt"`
	UpdatedAt          time.Time          `json:"updatedAt"`
	ResourceVersion    string             `json:"-"`
	LastSeenAt         *time.Time         `json:"-"`
	PendingAction      string             `json:"-"`
	ReconcileRetries   int                `json:"-"`
	NextReconcileAt    *time.Time         `json:"-"`
	ArchivedAt         *time.Time         `json:"-"`
}

type Observation struct {
	State                   State
	KubernetesUID           string
	ResourceVersion         string
	ExpectedResourceVersion string
	Reason                  string
	Message                 string
	ObservedAt              time.Time
	ManagementMode          ManagementMode
	SyncStatus              SyncStatus
}

type Event struct {
	ID        int64           `json:"id"`
	JobID     string          `json:"jobId"`
	Type      string          `json:"type"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type JobFacets struct {
	Total               int            `json:"total"`
	ObservedStateCounts map[string]int `json:"observedStateCounts"`
	Namespaces          []string       `json:"namespaces"`
	Teams               []string       `json:"teams"`
}

type CreateJob struct {
	ID                 string
	Name               string
	Namespace          string
	Team               string
	Priority           int
	ScheduledFor       *time.Time
	Template           json.RawMessage
	ParentID           string
	Attempt            int
	ProjectID          ProjectID
	NamespaceBindingID NamespaceBindingID
	CreatorPrincipalID PrincipalID
	SubmissionSource   SubmissionSource
	IdempotencyKey     string
}

func (c CreateJob) Validate() error {
	if c.IdempotencyKey != "" && !idempotencyKey.MatchString(c.IdempotencyKey) {
		return errors.New("idempotency key is invalid")
	}
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
		Metadata   struct {
			Annotations map[string]string `json:"annotations"`
			Labels      map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
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
	if strings.EqualFold(strings.TrimSpace(
		template.Metadata.Annotations["kubequeue.io/ignore"],
	), "true") ||
		strings.TrimSpace(template.Metadata.Annotations["helm.sh/hook"]) != "" ||
		strings.EqualFold(strings.TrimSpace(
			template.Metadata.Labels["kubequeue.io/internal"],
		), "true") {
		return errors.New("template is excluded from KubeQueue management")
	}
	if _, exists := template.Metadata.Labels["kubequeue.io/job-id"]; exists {
		return errors.New("template must not set KubeQueue ownership labels")
	}
	if _, exists := template.Metadata.Labels["kubequeue.io/managed"]; exists {
		return errors.New("template must not set KubeQueue ownership labels")
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

func SynchronizationStatus(desired, observed State) SyncStatus {
	switch desired {
	case StateCreated:
		if observed == StateCreated {
			return SyncStatusSynced
		}
	case StateQueued:
		if observed == StateCreated || observed == StateQueued || observed == StatePaused {
			return SyncStatusSynced
		}
	case StateRunning:
		if observed == StateRunning {
			return SyncStatusSynced
		}
	case StatePaused:
		if observed == StatePaused {
			return SyncStatusSynced
		}
	case StateCompleted, StateFailed, StateCancelled:
		if observed == desired {
			return SyncStatusSynced
		}
	}
	return SyncStatusPending
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
