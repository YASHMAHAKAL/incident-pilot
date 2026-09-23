package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"incidentpilot/internal/incident"
	"incidentpilot/internal/onboarding"
)

var ErrWindowUnavailable = errors.New("incident telemetry window is outside the last 24 hours")
var ErrUnsupportedIncident = errors.New("incident is outside the configured onboarding scope")

// Record is an independently inspectable observation, not an RCA or model claim.
type Record struct {
	ID           string          `json:"id"`
	CollectionID string          `json:"collection_id"`
	IncidentID   string          `json:"incident_id"`
	Tool         string          `json:"tool"`
	Source       string          `json:"source"`
	CollectedAt  time.Time       `json:"collected_at"`
	WindowStart  *time.Time      `json:"window_start,omitempty"`
	WindowEnd    *time.Time      `json:"window_end,omitempty"`
	Parameters   json.RawMessage `json:"parameters"`
	Summary      string          `json:"summary"`
	ResourceRef  string          `json:"resource_ref"`
	TraceID      string          `json:"trace_id,omitempty"`
	Payload      json.RawMessage `json:"payload"`
}

type Store interface {
	SaveBatch(context.Context, []Record) error
	ListByIncident(context.Context, string, int) ([]Record, error)
}

type ToolObservation struct {
	Source      string          `json:"source"`
	CollectedAt time.Time       `json:"collected_at"`
	Data        json.RawMessage `json:"data,omitempty"`
	Text        string          `json:"text,omitempty"`
}

type Caller interface {
	Call(context.Context, string, any) (ToolObservation, error)
}

type Failure struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

type Report struct {
	CollectionID string    `json:"collection_id"`
	IncidentID   string    `json:"incident_id"`
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
	Evidence     []Record  `json:"evidence"`
	Failures     []Failure `json:"failures"`
}

type Collector struct {
	Caller  Caller
	Store   Store
	Now     func() time.Time
	Profile onboarding.Profile
}

func Window(inc incident.Incident, now time.Time) (time.Time, time.Time, error) {
	now = now.UTC()
	end := now
	if inc.ResolvedAt != nil && inc.ResolvedAt.Before(end) {
		end = inc.ResolvedAt.UTC()
	}
	if inc.StartedAt.IsZero() || inc.StartedAt.Before(now.Add(-24*time.Hour)) || inc.StartedAt.After(end.Add(30*time.Second)) || end.Before(now.Add(-24*time.Hour)) {
		return time.Time{}, time.Time{}, ErrWindowUnavailable
	}
	start := inc.StartedAt.UTC().Add(-2 * time.Minute)
	if start.Before(end.Add(-time.Hour)) {
		start = end.Add(-time.Hour)
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, ErrWindowUnavailable
	}
	return start, end, nil
}
