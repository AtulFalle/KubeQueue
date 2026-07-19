package scheduler

import (
	"errors"
	"reflect"
	"testing"
)

func TestSelectHonorsProjectWeights(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		projects []Project
		steps    int
		want     []string
	}{
		{
			name: "equal shares",
			projects: []Project{
				project("bravo", 1, job("bravo-job", 0, 0)),
				project("alpha", 1, job("alpha-job", 0, 0)),
			},
			steps: 4,
			want:  []string{"alpha", "bravo", "alpha", "bravo"},
		},
		{
			name: "one to three shares",
			projects: []Project{
				project("alpha", 1, job("alpha-job", 0, 0)),
				project("bravo", 3, job("bravo-job", 0, 0)),
			},
			steps: 8,
			want:  []string{"alpha", "bravo", "bravo", "bravo", "alpha", "bravo", "bravo", "bravo"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := State{}
			got := make([]string, 0, test.steps)
			for range test.steps {
				outcome, err := Select(testPolicy(), state, test.projects)
				if err != nil {
					t.Fatalf("Select() error = %v", err)
				}
				got = append(got, outcome.Decision.ProjectID)
				state = outcome.State
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("selected projects = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSelectBoundsStandardLaneStarvation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		projects []Project
		bound    int
	}{
		{
			name: "unequal shares",
			projects: []Project{
				project("alpha", 1, job("alpha-job", 0, 0)),
				project("bravo", 4, job("bravo-job", 0, 0)),
				project("charlie", 2, job("charlie-job", 0, 0)),
			},
			bound: 7,
		},
		{
			name: "large adjacent shares",
			projects: []Project{
				project("alpha", 5, job("alpha-job", 0, 0)),
				project("bravo", 1, job("bravo-job", 0, 0)),
				project("charlie", 5, job("charlie-job", 0, 0)),
			},
			bound: 11,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := State{}
			selected := map[string]bool{}
			for range test.bound {
				outcome, err := Select(testPolicy(), state, test.projects)
				if err != nil {
					t.Fatalf("Select() error = %v", err)
				}
				selected[outcome.Decision.ProjectID] = true
				state = outcome.State
			}
			for _, project := range test.projects {
				if !selected[project.ID] {
					t.Errorf("project %q was not selected within %d decisions", project.ID, test.bound)
				}
			}
		})
	}
}

func TestSelectIsDeterministicAcrossInputOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		projects []Project
	}{
		{
			name: "original order",
			projects: []Project{
				project("bravo", 1, job("zulu", 5, 1), job("alpha", 5, 1)),
				project("alpha", 1, job("job", 0, 0)),
			},
		},
		{
			name: "reversed projects and jobs",
			projects: []Project{
				project("alpha", 1, job("job", 0, 0)),
				project("bravo", 1, job("alpha", 5, 1), job("zulu", 5, 1)),
			},
		},
	}

	want := Decision{
		ProjectID:            "bravo",
		JobID:                "alpha",
		AppliedPolicyVersion: testPolicy().Version,
		Basis: Basis{
			Lane:              LaneStandard,
			ProjectWeight:     1,
			DeficitBefore:     1,
			DeficitAfter:      0,
			BasePriority:      5,
			Age:               1,
			AgingStep:         testPolicy().AgingStep,
			EffectivePriority: 7,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outcome, err := Select(testPolicy(), State{NextProjectID: "bravo"}, test.projects)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}
			if !reflect.DeepEqual(*outcome.Decision, want) {
				t.Fatalf("decision = %#v, want %#v", *outcome.Decision, want)
			}
		})
	}
}

func TestSelectOrdersJobsWithinProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		jobs []Job
		want string
	}{
		{
			name: "base priority",
			jobs: []Job{
				job("low", 1, 0),
				job("high", 2, 0),
			},
			want: "high",
		},
		{
			name: "aging overtakes priority",
			jobs: []Job{
				job("new-high", 10, 0),
				job("old-low", 1, 5),
			},
			want: "old-low",
		},
		{
			name: "stable job ID tie breaker",
			jobs: []Job{
				job("zulu", 5, 2),
				job("alpha", 5, 2),
			},
			want: "alpha",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outcome, err := Select(testPolicy(), State{}, []Project{project("project", 1, test.jobs...)})
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}
			if got := outcome.Decision.JobID; got != test.want {
				t.Fatalf("selected job = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSelectSkipsEmptyAndIneligibleProjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		projects []Project
		wantID   string
	}{
		{name: "no projects"},
		{
			name: "all unavailable",
			projects: []Project{
				project("empty", 1),
				project("ineligible", 1, Job{ID: "job", Eligible: false, Lane: LaneStandard}),
			},
		},
		{
			name: "skip unavailable shares",
			projects: []Project{
				project("empty", 5),
				project("ineligible", 5, Job{ID: "job", Eligible: false, Lane: LaneStandard}),
				project("ready", 1, job("ready-job", 0, 0)),
			},
			wantID: "ready-job",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outcome, err := Select(testPolicy(), State{}, test.projects)
			if err != nil {
				t.Fatalf("Select() error = %v", err)
			}
			if test.wantID == "" {
				if outcome.Decision != nil {
					t.Fatalf("Decision = %#v, want nil", outcome.Decision)
				}
				return
			}
			if outcome.Decision == nil || outcome.Decision.JobID != test.wantID {
				t.Fatalf("Decision = %#v, want job %q", outcome.Decision, test.wantID)
			}
		})
	}
}

func TestSelectUsesExplicitEmergencyLane(t *testing.T) {
	t.Parallel()

	projects := []Project{
		project("alpha", 10, job("standard-high", 100, 10)),
		project("bravo", 1, Job{
			ID:                     "emergency",
			Priority:               -100,
			Eligible:               true,
			Lane:                   LaneEmergency,
			EmergencyRequested:     true,
			EmergencyAuthorized:    true,
			EmergencyAuthorization: "queue.global.reorder",
		}),
	}
	outcome, err := Select(testPolicy(), State{}, projects)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	decision := outcome.Decision
	if decision.ProjectID != "bravo" || decision.JobID != "emergency" {
		t.Fatalf("Decision = %#v, want bravo/emergency", decision)
	}
	if decision.AppliedPolicyVersion != testPolicy().Version {
		t.Errorf("applied policy version = %q, want %q", decision.AppliedPolicyVersion, testPolicy().Version)
	}
	if decision.Basis.Lane != LaneEmergency {
		t.Errorf("basis lane = %q, want %q", decision.Basis.Lane, LaneEmergency)
	}
}

func TestSelectRejectsUnauthorizedEmergencyLane(t *testing.T) {
	t.Parallel()
	_, err := Select(testPolicy(), State{}, []Project{
		project("alpha", 1, Job{
			ID: "unauthorized", Eligible: true, Lane: LaneEmergency,
			EmergencyRequested: true,
		}),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Select() error = %v, want invalid input", err)
	}
}

func TestSelectSustainsPositiveWeightWithoutStarvation(t *testing.T) {
	t.Parallel()
	projects := []Project{
		project("small", 1, job("small-job", 0, 0)),
		project("large", 9, job("large-job", 0, 0)),
	}
	state := State{}
	lastSmall := -1
	for decision := range 1_000 {
		outcome, err := Select(testPolicy(), state, projects)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Decision.ProjectID == "small" {
			if lastSmall >= 0 && decision-lastSmall > 10 {
				t.Fatalf("small project starved for %d decisions", decision-lastSmall)
			}
			lastSmall = decision
		}
		state = outcome.State
	}
	if lastSmall < 990 {
		t.Fatalf("small project was not selected in final weighted round: %d", lastSmall)
	}
}

func TestSelectRejectsInvalidPolicyAndInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   Policy
		projects []Project
		target   error
	}{
		{
			name:     "missing policy version",
			policy:   Policy{AgingStep: 1},
			projects: []Project{project("project", 1, job("job", 0, 0))},
			target:   ErrInvalidPolicy,
		},
		{
			name:     "non-positive aging step",
			policy:   Policy{Version: "v1"},
			projects: []Project{project("project", 1, job("job", 0, 0))},
			target:   ErrInvalidPolicy,
		},
		{
			name:     "non-positive project weight",
			policy:   testPolicy(),
			projects: []Project{project("project", 0, job("job", 0, 0))},
			target:   ErrInvalidInput,
		},
		{
			name:   "unknown lane",
			policy: testPolicy(),
			projects: []Project{project("project", 1, Job{
				ID:       "job",
				Eligible: true,
				Lane:     Lane("UNKNOWN"),
			})},
			target: ErrInvalidInput,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Select(test.policy, State{}, test.projects)
			if !errors.Is(err, test.target) {
				t.Fatalf("Select() error = %v, want errors.Is(_, %v)", err, test.target)
			}
		})
	}
}

func testPolicy() Policy {
	return Policy{Version: "policy-v7", AgingStep: 2}
}

func project(id string, weight uint64, jobs ...Job) Project {
	return Project{ID: id, Weight: weight, Jobs: jobs}
}

func job(id string, priority int64, age uint64) Job {
	return Job{
		ID:       id,
		Priority: priority,
		Age:      age,
		Eligible: true,
		Lane:     LaneStandard,
	}
}
