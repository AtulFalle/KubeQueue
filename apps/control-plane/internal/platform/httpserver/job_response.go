package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/sensitivedata"
)

type jobResponse struct {
	ID               string                `json:"id"`
	ProjectID        domain.ProjectID      `json:"projectId"`
	ParentID         string                `json:"parentId,omitempty"`
	Name             string                `json:"name"`
	Namespace        string                `json:"namespace"`
	Team             string                `json:"team,omitempty"`
	Priority         int                   `json:"priority"`
	Position         int64                 `json:"position"`
	DesiredState     domain.State          `json:"desiredState"`
	ObservedState    domain.State          `json:"observedState"`
	ManagementMode   domain.ManagementMode `json:"managementMode"`
	SyncStatus       domain.SyncStatus     `json:"syncStatus"`
	ActionPending    bool                  `json:"actionPending"`
	ObservedReason   string                `json:"observedReason,omitempty"`
	ObservedMessage  string                `json:"observedMessage,omitempty"`
	ObservedAt       *time.Time            `json:"observedAt,omitempty"`
	LastError        string                `json:"lastError,omitempty"`
	LastErrorCode    string                `json:"lastErrorCode,omitempty"`
	ErrorRemediation string                `json:"errorRemediation,omitempty"`
	ScheduledFor     *time.Time            `json:"scheduledFor,omitempty"`
	KubernetesUID    string                `json:"kubernetesUid,omitempty"`
	Attempt          int                   `json:"attempt"`
	Version          int64                 `json:"version"`
	CreatedAt        time.Time             `json:"createdAt"`
	UpdatedAt        time.Time             `json:"updatedAt"`
}

type jobManifestResponse struct {
	JobID    string          `json:"jobId"`
	Manifest json.RawMessage `json:"manifest"`
}

func newJobResponse(job domain.Job) jobResponse {
	return jobResponse{
		ID: job.ID, ProjectID: job.ProjectID, ParentID: job.ParentID, Name: job.Name, Namespace: job.Namespace,
		Team: job.Team, Priority: job.Priority, Position: job.Position,
		DesiredState: job.DesiredState, ObservedState: job.ObservedState,
		ManagementMode: job.ManagementMode, SyncStatus: job.SyncStatus,
		ActionPending: job.ActionPending, ObservedReason: job.ObservedReason,
		ObservedMessage: job.ObservedMessage, ObservedAt: job.ObservedAt,
		LastError: job.LastError, LastErrorCode: job.LastErrorCode,
		ErrorRemediation: job.ErrorRemediation, ScheduledFor: job.ScheduledFor,
		KubernetesUID: job.KubernetesUID, Attempt: job.Attempt, Version: job.Version,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
}

func newJobResponses(jobs []domain.Job) []jobResponse {
	responses := make([]jobResponse, 0, len(jobs))
	for _, job := range jobs {
		responses = append(responses, newJobResponse(job))
	}
	return responses
}

func newJobManifestResponse(jobID string, stored json.RawMessage) (jobManifestResponse, error) {
	inspection, err := sensitivedata.InspectManifestJSON(stored, sensitivedata.Limits{})
	if err != nil {
		return jobManifestResponse{}, fmt.Errorf("redact stored job manifest: %w", err)
	}

	var manifest map[string]any
	decoder := json.NewDecoder(bytes.NewReader(inspection.Redacted))
	decoder.UseNumber()
	if err := decoder.Decode(&manifest); err != nil {
		return jobManifestResponse{}, fmt.Errorf("decode stored job manifest: %w", err)
	}
	delete(manifest, "status")
	if metadata, ok := manifest["metadata"].(map[string]any); ok {
		for _, field := range []string{
			"creationTimestamp", "deletionGracePeriodSeconds", "deletionTimestamp", "generation",
			"managedFields", "resourceVersion", "selfLink", "uid",
		} {
			delete(metadata, field)
		}
	}
	sanitized, err := json.Marshal(manifest)
	if err != nil {
		return jobManifestResponse{}, fmt.Errorf("encode stored job manifest: %w", err)
	}
	return jobManifestResponse{JobID: jobID, Manifest: sanitized}, nil
}
