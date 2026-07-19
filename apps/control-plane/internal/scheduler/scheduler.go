package scheduler

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Select chooses at most one unit-cost job and returns the state to pass to the
// next call. Inputs are never mutated.
//
// Emergency jobs preempt the standard weighted-fair lane and are ordered
// globally. In the absence of emergency work, every continuously eligible
// project is selected at least once per sum-of-active-weights decisions.
func Select(policy Policy, state State, projects []Project) (Outcome, error) {
	if err := validate(policy, state, projects); err != nil {
		return Outcome{}, err
	}

	ordered := slices.Clone(projects)
	slices.SortFunc(ordered, func(left, right Project) int {
		return strings.Compare(left.ID, right.ID)
	})

	next := normalizedState(state, ordered)
	if project, job, ok := bestEmergency(ordered, policy); ok {
		return decision(policy, next, project, job, project.Weight, next.Deficits[project.ID], next.Deficits[project.ID]), nil
	}

	if len(ordered) == 0 {
		return Outcome{State: next}, nil
	}

	start := startIndex(ordered, next.NextProjectID)
	for offset := range len(ordered) {
		index := (start + offset) % len(ordered)
		project := ordered[index]
		job, ok := bestJob(project.Jobs, LaneStandard, policy)
		if !ok {
			next.Deficits[project.ID] = 0
			continue
		}

		before := next.Deficits[project.ID]
		if before == 0 {
			before = project.Weight
		}
		after := before - 1
		next.Deficits[project.ID] = after
		if after == 0 {
			next.NextProjectID = ordered[(index+1)%len(ordered)].ID
		} else {
			next.NextProjectID = project.ID
		}

		return decision(policy, next, project, job, project.Weight, before, after), nil
	}

	next.NextProjectID = ordered[start].ID
	return Outcome{State: next}, nil
}

func validate(policy Policy, state State, projects []Project) error {
	if strings.TrimSpace(policy.Version) == "" {
		return fmt.Errorf("%w: version is required", ErrInvalidPolicy)
	}
	if policy.AgingStep <= 0 {
		return fmt.Errorf("%w: aging step must be positive", ErrInvalidPolicy)
	}

	projectIDs := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		if project.ID == "" {
			return fmt.Errorf("%w: project ID is required", ErrInvalidInput)
		}
		if project.Weight == 0 {
			return fmt.Errorf("%w: project %q weight must be positive", ErrInvalidInput, project.ID)
		}
		if _, exists := projectIDs[project.ID]; exists {
			return fmt.Errorf("%w: duplicate project %q", ErrInvalidInput, project.ID)
		}
		projectIDs[project.ID] = struct{}{}

		jobIDs := make(map[string]struct{}, len(project.Jobs))
		for _, job := range project.Jobs {
			if job.ID == "" {
				return fmt.Errorf("%w: project %q has a job without an ID", ErrInvalidInput, project.ID)
			}
			if _, exists := jobIDs[job.ID]; exists {
				return fmt.Errorf("%w: duplicate job %q in project %q", ErrInvalidInput, job.ID, project.ID)
			}
			jobIDs[job.ID] = struct{}{}
			if job.Lane != LaneStandard && job.Lane != LaneEmergency {
				return fmt.Errorf("%w: job %q has unknown lane %q", ErrInvalidInput, job.ID, job.Lane)
			}
			if job.Lane == LaneEmergency &&
				(!job.EmergencyRequested || !job.EmergencyAuthorized ||
					strings.TrimSpace(job.EmergencyAuthorization) == "") {
				return fmt.Errorf(
					"%w: emergency job %q lacks explicit authorization metadata",
					ErrInvalidInput, job.ID,
				)
			}
		}
	}

	for projectID, deficit := range state.Deficits {
		for _, project := range projects {
			if project.ID == projectID && deficit >= project.Weight {
				return fmt.Errorf("%w: project %q deficit must be less than its weight", ErrInvalidState, projectID)
			}
		}
	}
	return nil
}

func normalizedState(state State, projects []Project) State {
	deficits := make(map[string]uint64, len(projects))
	for _, project := range projects {
		deficits[project.ID] = state.Deficits[project.ID]
	}
	return State{
		NextProjectID: state.NextProjectID,
		Deficits:      deficits,
	}
}

func startIndex(projects []Project, nextProjectID string) int {
	index, _ := slices.BinarySearchFunc(projects, nextProjectID, func(project Project, id string) int {
		return strings.Compare(project.ID, id)
	})
	if index == len(projects) {
		return 0
	}
	return index
}

func bestEmergency(projects []Project, policy Policy) (Project, Job, bool) {
	var selectedProject Project
	var selectedJob Job
	found := false
	for _, project := range projects {
		job, ok := bestJob(project.Jobs, LaneEmergency, policy)
		if !ok {
			continue
		}
		if !found || jobBefore(job, selectedJob, policy) ||
			(!jobBefore(selectedJob, job, policy) && project.ID < selectedProject.ID) {
			selectedProject = project
			selectedJob = job
			found = true
		}
	}
	return selectedProject, selectedJob, found
}

func bestJob(jobs []Job, lane Lane, policy Policy) (Job, bool) {
	var selected Job
	found := false
	for _, job := range jobs {
		if !job.Eligible || job.Lane != lane {
			continue
		}
		if !found || jobBefore(job, selected, policy) {
			selected = job
			found = true
		}
	}
	return selected, found
}

func jobBefore(left, right Job, policy Policy) bool {
	leftEffective := effectivePriority(left, policy)
	rightEffective := effectivePriority(right, policy)
	if leftEffective != rightEffective {
		return leftEffective > rightEffective
	}
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if left.Age != right.Age {
		return left.Age > right.Age
	}
	return left.ID < right.ID
}

func effectivePriority(job Job, policy Policy) int64 {
	if job.Age > uint64(math.MaxInt64)/uint64(policy.AgingStep) {
		return math.MaxInt64
	}
	boost := int64(job.Age) * policy.AgingStep
	if job.Priority > math.MaxInt64-boost {
		return math.MaxInt64
	}
	return job.Priority + boost
}

func decision(
	policy Policy,
	state State,
	project Project,
	job Job,
	weight uint64,
	before uint64,
	after uint64,
) Outcome {
	return Outcome{
		Decision: &Decision{
			ProjectID:            project.ID,
			JobID:                job.ID,
			AppliedPolicyVersion: policy.Version,
			Basis: Basis{
				Lane:                   job.Lane,
				ProjectWeight:          weight,
				DeficitBefore:          before,
				DeficitAfter:           after,
				BasePriority:           job.Priority,
				Age:                    job.Age,
				AgingStep:              policy.AgingStep,
				EffectivePriority:      effectivePriority(job, policy),
				EmergencyRequested:     job.EmergencyRequested,
				EmergencyAuthorized:    job.EmergencyAuthorized,
				EmergencyAuthorization: job.EmergencyAuthorization,
			},
		},
		State: state,
	}
}
