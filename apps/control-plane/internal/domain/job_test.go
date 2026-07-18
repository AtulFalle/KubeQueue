package domain

import (
	"encoding/json"
	"testing"
)

func TestCreateJobValidation(t *testing.T) {
	t.Parallel()
	valid := CreateJob{
		Name: "report", Namespace: "default",
		Template: json.RawMessage(`{
			"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"job","image":"busybox"}]}}}
		}`),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	tests := []CreateJob{
		{Namespace: "default", Template: json.RawMessage(`{}`)},
		{Name: "report", Template: json.RawMessage(`{}`)},
		{Name: "report", Namespace: "default", Template: json.RawMessage(`not-json`)},
		{Name: "report", Namespace: "default", Priority: 1001, Template: json.RawMessage(`{}`)},
	}
	for _, input := range tests {
		if err := input.Validate(); err == nil {
			t.Errorf("invalid input accepted: %#v", input)
		}
	}
}

func TestLifecycleTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to State
		want     bool
	}{
		{StateCreated, StateQueued, true},
		{StateQueued, StatePaused, true},
		{StatePaused, StateQueued, true},
		{StateRunning, StateCompleted, true},
		{StateCompleted, StateQueued, false},
		{StateCancelled, StateRunning, false},
	}
	for _, test := range tests {
		if got := CanTransition(test.from, test.to); got != test.want {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", test.from, test.to, got, test.want)
		}
	}
}
