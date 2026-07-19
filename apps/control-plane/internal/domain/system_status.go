package domain

import "time"

type WorkerState string

const (
	WorkerStateReady       WorkerState = "ready"
	WorkerStateDegraded    WorkerState = "degraded"
	WorkerStateUnavailable WorkerState = "unavailable"
)

type NamespaceAuthorityStatus struct {
	Namespace      string     `json:"namespace"`
	InformerSynced bool       `json:"informerSynced"`
	Authorized     bool       `json:"authorized"`
	Message        string     `json:"message,omitempty"`
	ObservedAt     *time.Time `json:"observedAt,omitempty"`
}

type WorkerStatus struct {
	State                          WorkerState                `json:"state"`
	HeartbeatAt                    *time.Time                 `json:"heartbeatAt,omitempty"`
	LastSuccessfulReconciliationAt *time.Time                 `json:"lastSuccessfulReconciliationAt,omitempty"`
	WatchMode                      WatchMode                  `json:"-"`
	EffectiveNamespaces            []string                   `json:"-"`
	ExcludedNamespaces             []string                   `json:"-"`
	Namespaces                     []NamespaceAuthorityStatus `json:"-"`
	GlobalConcurrency              int                        `json:"-"`
	NamespaceConcurrency           int                        `json:"-"`
	ReleaseVersion                 string                     `json:"-"`
	ActiveError                    string                     `json:"-"`
}

type StatusError struct {
	Scope   string `json:"scope"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SystemStatus struct {
	API struct {
		Ready bool `json:"ready"`
	} `json:"api"`
	Database struct {
		Ready bool `json:"ready"`
	} `json:"database"`
	Worker WorkerStatus `json:"worker"`
	Watch  struct {
		Mode                WatchMode                  `json:"mode"`
		EffectiveNamespaces []string                   `json:"effectiveNamespaces"`
		ExcludedNamespaces  []string                   `json:"excludedNamespaces"`
		Namespaces          []NamespaceAuthorityStatus `json:"namespaces"`
	} `json:"watch"`
	Concurrency struct {
		Global       int `json:"global"`
		PerNamespace int `json:"perNamespace"`
	} `json:"concurrency"`
	ReleaseVersion string        `json:"releaseVersion"`
	ActiveErrors   []StatusError `json:"activeErrors"`
}
