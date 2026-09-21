package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"

	"incidentpilot/internal/evidence"
)

const maxEvidenceBatchSize = 16

func (store *Postgres) SaveBatch(ctx context.Context, records []evidence.Record) error {
	if err := validateEvidenceBatchSize(len(records)); err != nil {
		return err
	}
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "evidence.save_batch")
	defer span.End()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin evidence transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, record := range records {
		if len(record.Payload) > 256<<10 || len(record.Parameters) > 2048 || len(record.Summary) > 512 || record.CollectedAt.IsZero() || record.IncidentID == "" {
			return fmt.Errorf("invalid evidence record")
		}
		_, err := tx.Exec(ctx, `INSERT INTO evidence (id, collection_id, incident_id, tool, source, collected_at, window_start, window_end, parameters, summary, resource_ref, trace_id, payload)
VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,NULLIF($12,''),$13::jsonb)`, record.ID, record.CollectionID, record.IncidentID, record.Tool, record.Source, record.CollectedAt, record.WindowStart, record.WindowEnd, record.Parameters, record.Summary, record.ResourceRef, record.TraceID, record.Payload)
		if err != nil {
			return fmt.Errorf("insert evidence %s: %w", record.Tool, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit evidence: %w", err)
	}
	return nil
}

func validateEvidenceBatchSize(size int) error {
	if size < 1 || size > maxEvidenceBatchSize {
		return fmt.Errorf("evidence batch must contain 1 to %d records", maxEvidenceBatchSize)
	}
	return nil
}

func (store *Postgres) ListByIncident(ctx context.Context, incidentID string, limit int) ([]evidence.Record, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("evidence limit must be 1 to 100")
	}
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "evidence.list")
	defer span.End()
	rows, err := store.pool.Query(ctx, `SELECT id::text,collection_id::text,incident_id::text,tool,source,collected_at,window_start,window_end,parameters,summary,resource_ref,COALESCE(trace_id,''),payload FROM evidence WHERE incident_id=$1::uuid ORDER BY collected_at DESC,id DESC LIMIT $2`, incidentID, limit)
	if err != nil {
		return nil, fmt.Errorf("query evidence: %w", err)
	}
	defer rows.Close()
	results := make([]evidence.Record, 0)
	for rows.Next() {
		record, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence: %w", err)
	}
	return results, nil
}

func scanEvidence(row pgx.Row) (evidence.Record, error) {
	var record evidence.Record
	var windowStart, windowEnd *time.Time
	err := row.Scan(&record.ID, &record.CollectionID, &record.IncidentID, &record.Tool, &record.Source, &record.CollectedAt, &windowStart, &windowEnd, &record.Parameters, &record.Summary, &record.ResourceRef, &record.TraceID, &record.Payload)
	if err != nil {
		return evidence.Record{}, fmt.Errorf("scan evidence: %w", err)
	}
	record.WindowStart = windowStart
	record.WindowEnd = windowEnd
	return record, nil
}
