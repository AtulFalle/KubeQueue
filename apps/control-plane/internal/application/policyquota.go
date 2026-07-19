package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain/policyquota"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/ports"
)

const maxPolicyImages = 256

var errTooManyPolicyImages = errors.New("job image set exceeds policy bound")

var ErrPolicyNotConfigured = errors.New("versioned policy is not configured")

const (
	reasonPriorityBelowMinimum policyquota.RejectionReason = "policy.priority_below_minimum"
	reasonPriorityAboveMaximum policyquota.RejectionReason = "policy.priority_above_maximum"
	reasonDelayHorizonExceeded policyquota.RejectionReason = "policy.delay_horizon_exceeded"
	reasonRegistryDenied       policyquota.RejectionReason = "policy.image_registry_denied"
	reasonManifestTooLarge     policyquota.RejectionReason = "policy.image_set_too_large"
)

type PolicyQuotaRejection struct {
	Detail policyquota.Rejection
}

func (e *PolicyQuotaRejection) Error() string {
	return string(e.Detail.Reason)
}

type PolicyQuotaSubmission struct {
	Job               domain.CreateJob
	IdempotencyKey    string
	PrioritySpecified bool
}

type PolicyQuotaService struct {
	repository ports.PolicyQuotaJobRepository
}

func NewPolicyQuotaService(repository ports.PolicyQuotaJobRepository) *PolicyQuotaService {
	return &PolicyQuotaService{repository: repository}
}

func (s *PolicyQuotaService) Submit(
	ctx context.Context,
	installationID domain.InstallationID,
	target policyquota.Scope,
	submission PolicyQuotaSubmission,
) (domain.Job, error) {
	effective, err := s.EffectivePolicy(ctx, installationID, target)
	if err != nil {
		return domain.Job{}, err
	}
	if err := applyAdmissionPolicy(&submission.Job, submission.PrioritySpecified, effective); err != nil {
		return domain.Job{}, err
	}
	result, err := s.repository.CreateJobWithQuota(ctx, ports.QuotaSubmission{
		InstallationID: installationID,
		Target:         target,
		Policy:         effective,
		IdempotencyKey: submission.IdempotencyKey,
		Job:            submission.Job,
		Demand:         queuedJobDemand(),
	})
	if err != nil {
		return domain.Job{}, err
	}
	if result.Decision.Rejection != nil {
		return domain.Job{}, &PolicyQuotaRejection{Detail: *result.Decision.Rejection}
	}
	return result.Job, nil
}

func (s *PolicyQuotaService) Admit(
	ctx context.Context,
	installationID domain.InstallationID,
	target policyquota.Scope,
	jobID string,
) (policyquota.ReservationDecision, error) {
	effective, err := s.EffectivePolicy(ctx, installationID, target)
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	decision, err := s.repository.AdmitJobQuota(ctx, installationID, jobID, effective)
	if err != nil {
		return policyquota.ReservationDecision{}, err
	}
	if decision.Rejection != nil {
		return decision, &PolicyQuotaRejection{Detail: *decision.Rejection}
	}
	return decision, nil
}

func (s *PolicyQuotaService) Release(
	ctx context.Context,
	installationID domain.InstallationID,
	jobID string,
	cause policyquota.ReleaseCause,
) (policyquota.Reservation, policyquota.Usage, error) {
	return s.repository.ReleaseJobQuota(ctx, installationID, jobID, cause)
}

func (s *PolicyQuotaService) EffectivePolicy(
	ctx context.Context,
	installationID domain.InstallationID,
	target policyquota.Scope,
) (policyquota.EffectivePolicy, error) {
	policies, err := s.repository.PolicyHierarchy(ctx, installationID, target)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return policyquota.EffectivePolicy{}, ErrPolicyNotConfigured
		}
		return policyquota.EffectivePolicy{}, err
	}
	effective, err := policyquota.Compose(policies...)
	if err != nil {
		return policyquota.EffectivePolicy{}, fmt.Errorf("compose effective policy: %w", err)
	}
	return effective, nil
}

func applyAdmissionPolicy(
	job *domain.CreateJob,
	prioritySpecified bool,
	effective policyquota.EffectivePolicy,
) error {
	if effective.Rules.Priority == nil {
		return errors.New("effective policy has no priority rule")
	}
	priority := effective.Rules.Priority
	if !prioritySpecified {
		job.Priority = priority.Default
	}
	if job.Priority < priority.Min {
		return policyRejection(effective, "priority", reasonPriorityBelowMinimum)
	}
	if job.Priority > priority.Max {
		return policyRejection(effective, "priority", reasonPriorityAboveMaximum)
	}
	if job.ScheduledFor != nil && effective.Rules.MaxDelayedStart != nil &&
		job.ScheduledFor.After(time.Now().UTC().Add(*effective.Rules.MaxDelayedStart)) {
		return policyRejection(effective, "scheduled_for", reasonDelayHorizonExceeded)
	}
	images, err := workloadImages(job.Template)
	if errors.Is(err, errTooManyPolicyImages) {
		return policyRejection(effective, "images", reasonManifestTooLarge)
	}
	if err != nil {
		return err
	}
	for _, image := range images {
		allowed, err := effective.AllowsImage(image)
		if err != nil {
			return fmt.Errorf("validate image registry: %w", err)
		}
		if !allowed {
			return policyRejection(effective, "image_registry", reasonRegistryDenied)
		}
	}
	return nil
}

func workloadImages(template json.RawMessage) ([]string, error) {
	var manifest struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
					InitContainers []struct {
						Image string `json:"image"`
					} `json:"initContainers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(template, &manifest); err != nil {
		return nil, fmt.Errorf("decode Job images: %w", err)
	}
	count := len(manifest.Spec.Template.Spec.Containers) +
		len(manifest.Spec.Template.Spec.InitContainers)
	if count > maxPolicyImages {
		return nil, errTooManyPolicyImages
	}
	images := make([]string, 0, count)
	for _, container := range manifest.Spec.Template.Spec.InitContainers {
		images = append(images, container.Image)
	}
	for _, container := range manifest.Spec.Template.Spec.Containers {
		images = append(images, container.Image)
	}
	return images, nil
}

func policyRejection(
	effective policyquota.EffectivePolicy,
	metric string,
	reason policyquota.RejectionReason,
) error {
	ref := effective.Applied[len(effective.Applied)-1]
	return &PolicyQuotaRejection{Detail: policyquota.Rejection{
		Policy: ref, Scope: ref.Scope, Metric: metric, Reason: reason,
		Remediation: policyquota.Remediation("UPDATE_REQUEST_OR_POLICY"),
	}}
}

func queuedJobDemand() policyquota.Usage {
	counters := policyquota.Counters{Queued: 1, Retained: 1}
	return policyquota.Usage{Global: counters, Project: counters, Namespace: counters}
}
