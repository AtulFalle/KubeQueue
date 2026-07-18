package domain

import (
	"encoding/json"
	"strings"
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

func TestSynchronizationStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		desired, observed State
		want              SyncStatus
	}{
		{"queued before creation", StateQueued, StateCreated, SyncStatusSynced},
		{"queued while suspended", StateQueued, StatePaused, SyncStatusSynced},
		{"pause pending", StatePaused, StateRunning, SyncStatusPending},
		{"paused", StatePaused, StatePaused, SyncStatusSynced},
		{"termination pending", StateCancelled, StateRunning, SyncStatusPending},
		{"terminated", StateCancelled, StateCancelled, SyncStatusSynced},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := SynchronizationStatus(test.desired, test.observed); got != test.want {
				t.Fatalf("SynchronizationStatus(%s, %s) = %s, want %s",
					test.desired, test.observed, got, test.want)
			}
		})
	}
}

func TestReconciliationFieldsAreNotPublicJSON(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(Job{
		ManagementMode: ManagementManaged,
		SyncStatus:     SyncStatusPending,
		ActionPending:  true,
		LastError:      "internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"managementMode", "syncStatus", "actionPending", "lastError",
	} {
		if strings.Contains(string(encoded), field) {
			t.Fatalf("internal field %q was serialized: %s", field, encoded)
		}
	}
}
