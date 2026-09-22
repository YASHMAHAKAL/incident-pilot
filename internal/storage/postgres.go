package storage

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"

	"incidentpilot/internal/incident"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Postgres struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Postgres, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	store := &Postgres{pool: pool}
	for {
		err := store.Ping(ctx)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("connect to PostgreSQL: %w", errors.Join(ctx.Err(), err))
		case <-time.After(time.Second):
		}
	}
	if err := store.Migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate PostgreSQL: %w", err)
	}
	return store, nil
}

func (store *Postgres) Close() { store.pool.Close() }

func (store *Postgres) Ping(ctx context.Context) error { return store.pool.Ping(ctx) }

func (store *Postgres) Migrate(ctx context.Context) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(9083101)"); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY)"); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	for version, name := range []string{"migrations/001_incidents.sql", "migrations/002_evidence.sql", "migrations/003_investigations.sql", "migrations/004_remediation.sql", "migrations/005_github_remediation.sql"} {
		version++
		var applied bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			return fmt.Errorf("record migration %d: %w", version, err)
		}
	}
	return tx.Commit(ctx)
}

const incidentColumns = "id::text, fingerprint, alert_name, namespace, service, severity, started_at, detected_at, resolved_at, status"

func scanIncident(row pgx.Row) (incident.Incident, error) {
	var result incident.Incident
	var status string
	err := row.Scan(&result.ID, &result.Fingerprint, &result.AlertName, &result.Namespace, &result.Service, &result.Severity, &result.StartedAt, &result.DetectedAt, &result.ResolvedAt, &status)
	result.Status = incident.Status(status)
	return result, err
}

func (store *Postgres) Upsert(ctx context.Context, signal incident.Signal) (incident.Incident, error) {
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "incident.upsert")
	defer span.End()
	var resolvedAt *time.Time
	if signal.Status == incident.StatusResolved {
		resolvedAt = signal.ResolvedAt
	}
	row := store.pool.QueryRow(ctx, `
INSERT INTO incidents (fingerprint, alert_name, namespace, service, severity, started_at, resolved_at, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (fingerprint, started_at) DO UPDATE
SET status = CASE WHEN incidents.status = 'RESOLVED' THEN 'RESOLVED' ELSE EXCLUDED.status END,
    resolved_at = COALESCE(incidents.resolved_at, EXCLUDED.resolved_at),
    updated_at = now()
RETURNING `+incidentColumns,
		signal.Fingerprint, signal.AlertName, signal.Namespace, signal.Service, signal.Severity, signal.StartedAt, resolvedAt, signal.Status)
	result, err := scanIncident(row)
	if err != nil {
		return incident.Incident{}, fmt.Errorf("upsert incident: %w", err)
	}
	return result, nil
}

func (store *Postgres) List(ctx context.Context, limit int) ([]incident.Incident, error) {
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "incident.list")
	defer span.End()
	rows, err := store.pool.Query(ctx, "SELECT "+incidentColumns+" FROM incidents ORDER BY detected_at DESC, id DESC LIMIT $1", limit)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()
	results := make([]incident.Incident, 0)
	for rows.Next() {
		result, err := scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate incidents: %w", err)
	}
	return results, nil
}

func (store *Postgres) Get(ctx context.Context, id string) (incident.Incident, error) {
	ctx, span := otel.Tracer("incidentpilot/storage").Start(ctx, "incident.get")
	defer span.End()
	result, err := scanIncident(store.pool.QueryRow(ctx, "SELECT "+incidentColumns+" FROM incidents WHERE id = $1::uuid", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return incident.Incident{}, incident.ErrNotFound
	}
	if err != nil {
		return incident.Incident{}, fmt.Errorf("get incident: %w", err)
	}
	return result, nil
}
