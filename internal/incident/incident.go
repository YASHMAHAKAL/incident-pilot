package incident

import (
	"context"
	"errors"
	"time"
)

type Status string

const (
	StatusDetected Status = "DETECTED"
	StatusResolved Status = "RESOLVED"
)

var ErrNotFound = errors.New("incident not found")

// Signal is the validated, normalized subset of an Alertmanager alert that
// Phase 3 persists. Raw annotations and other untrusted payloads are omitted.
type Signal struct {
	Fingerprint string
	AlertName   string
	Namespace   string
	Service     string
	Severity    string
	StartedAt   time.Time
	ResolvedAt  *time.Time
	Status      Status
}

type Incident struct {
	ID          string     `json:"id"`
	Fingerprint string     `json:"fingerprint"`
	AlertName   string     `json:"alert_name"`
	Namespace   string     `json:"namespace"`
	Service     string     `json:"service"`
	Severity    string     `json:"severity"`
	StartedAt   time.Time  `json:"started_at"`
	DetectedAt  time.Time  `json:"detected_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Status      Status     `json:"status"`
}

type Store interface {
	Upsert(context.Context, Signal) (Incident, error)
	List(context.Context, int) ([]Incident, error)
	Get(context.Context, string) (Incident, error)
	Ping(context.Context) error
}
