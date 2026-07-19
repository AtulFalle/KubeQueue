package domain

import "time"

const MaxSupportErrorClasses = 20

type SchemaDiagnostics struct {
	Healthy bool   `json:"healthy"`
	Current string `json:"current"`
	Latest  string `json:"latest"`
}

type LeadershipDiagnostics struct {
	Held       bool   `json:"held"`
	Generation uint64 `json:"generation"`
}

type SupportErrorClass struct {
	Class      string     `json:"class"`
	Count      uint64     `json:"count"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
}

// SupportDiagnostics is an allowlisted local snapshot. It intentionally has no
// fields capable of carrying credentials, manifests, environment values, DSNs,
// lease-holder identities, or raw error text.
type SupportDiagnostics struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Versions    struct {
		API    string `json:"api"`
		Worker string `json:"worker"`
	} `json:"versions"`
	Schema     SchemaDiagnostics     `json:"schema"`
	Leadership LeadershipDiagnostics `json:"leadership"`
	Worker     WorkerStatus          `json:"worker"`
	Watch      struct {
		Mode                WatchMode                  `json:"mode"`
		EffectiveNamespaces []string                   `json:"effectiveNamespaces"`
		ExcludedNamespaces  []string                   `json:"excludedNamespaces"`
		Namespaces          []NamespaceAuthorityStatus `json:"namespaces"`
	} `json:"watch"`
	RecentErrorClasses       []SupportErrorClass `json:"recentErrorClasses"`
	AuditWriterOverloadCount uint64              `json:"auditWriterOverloadCount"`
}
