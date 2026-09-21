package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"

	"incidentpilot/internal/agent"
	"incidentpilot/internal/incident"
)

func (store *Postgres) SaveInvestigation(ctx context.Context, report agent.Report) error {
	if report.ID == "" || report.IncidentID == "" || report.StartedAt.IsZero() || report.CompletedAt.IsZero() || (report.Status != agent.StatusRootCauseFound && report.Status != agent.StatusInsufficientEvidence) {
		return errors.New("invalid investigation report")
	}
	data, err := json.Marshal(report)
	if err != nil || len(data) > 256<<10 {
		return errors.New("investigation report invalid or too large")
	}
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "investigation.save")
	defer span.End()
	_, err = store.pool.Exec(ctx, `INSERT INTO investigations (id,incident_id,status,started_at,completed_at,report) VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6::jsonb)`, report.ID, report.IncidentID, report.Status, report.StartedAt, report.CompletedAt, data)
	if err != nil {
		return fmt.Errorf("save investigation: %w", err)
	}
	return nil
}

func (store *Postgres) GetInvestigation(ctx context.Context, id string) (agent.Report, error) {
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "investigation.get")
	defer span.End()
	var data []byte
	err := store.pool.QueryRow(ctx, `SELECT report FROM investigations WHERE id=$1::uuid`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.Report{}, incident.ErrNotFound
	}
	if err != nil {
		return agent.Report{}, fmt.Errorf("get investigation: %w", err)
	}
	var report agent.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return agent.Report{}, fmt.Errorf("decode investigation: %w", err)
	}
	return report, nil
}
