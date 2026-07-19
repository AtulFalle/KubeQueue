package audit

import "time"

const (
	MinimumRetention = 24 * time.Hour
	MaximumRetention = 10 * 365 * 24 * time.Hour
)

type RetentionPolicy struct {
	period time.Duration
}

func NewRetentionPolicy(period time.Duration) (RetentionPolicy, error) {
	if period < MinimumRetention || period > MaximumRetention {
		return RetentionPolicy{}, validationError("retention.period", ErrorInvalid)
	}
	return RetentionPolicy{period: period}, nil
}

func (p RetentionPolicy) Period() time.Duration {
	return p.period
}

type LegalHold struct {
	enabled    bool
	indefinite bool
	until      time.Time
}

func NoLegalHold() LegalHold {
	return LegalHold{}
}

func NewIndefiniteLegalHold() LegalHold {
	return LegalHold{enabled: true, indefinite: true}
}

func NewLegalHoldUntil(until time.Time) (LegalHold, error) {
	if until.IsZero() {
		return LegalHold{}, validationError("legal_hold.until", ErrorRequired)
	}
	return LegalHold{
		enabled: true,
		until:   until.Round(0).UTC(),
	}, nil
}

func (h LegalHold) Enabled() bool {
	return h.enabled
}

func (h LegalHold) Indefinite() bool {
	return h.enabled && h.indefinite
}

func (h LegalHold) Until() (time.Time, bool) {
	if !h.enabled || h.indefinite {
		return time.Time{}, false
	}
	return h.until, true
}

func (h LegalHold) activeAt(evaluatedAt time.Time) bool {
	if !h.enabled {
		return false
	}
	return h.indefinite || evaluatedAt.Before(h.until)
}

type RetentionDecision string

const (
	RetainForPolicy    RetentionDecision = "RETAIN_FOR_POLICY"
	RetainForLegalHold RetentionDecision = "RETAIN_FOR_LEGAL_HOLD"
	EligibleForDelete  RetentionDecision = "ELIGIBLE_FOR_DELETE"
)

// DecideRetention applies legal hold before ordinary age-based retention.
// The function is deterministic because every time input is supplied by the caller.
func DecideRetention(
	policy RetentionPolicy,
	occurredAt time.Time,
	evaluatedAt time.Time,
	hold LegalHold,
) (RetentionDecision, error) {
	if policy.period == 0 {
		return "", validationError("retention.policy", ErrorRequired)
	}
	if occurredAt.IsZero() {
		return "", validationError("occurred_at", ErrorRequired)
	}
	if evaluatedAt.IsZero() {
		return "", validationError("evaluated_at", ErrorRequired)
	}
	occurredAt = occurredAt.Round(0).UTC()
	evaluatedAt = evaluatedAt.Round(0).UTC()
	if evaluatedAt.Before(occurredAt) {
		return "", validationError("evaluated_at", ErrorInvalid)
	}
	if hold.activeAt(evaluatedAt) {
		return RetainForLegalHold, nil
	}
	if evaluatedAt.Before(occurredAt.Add(policy.period)) {
		return RetainForPolicy, nil
	}
	return EligibleForDelete, nil
}
