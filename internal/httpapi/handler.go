package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"incidentpilot/internal/evidence"
	"incidentpilot/internal/incident"
	"incidentpilot/internal/remediation"
	"incidentpilot/internal/telemetry"
)

const maxWebhookBytes = 256 << 10
const maxRemediationBytes = 32 << 10

var (
	fingerprintPattern = regexp.MustCompile(`^[0-9a-fA-F]{16}$`)
	namePattern        = regexp.MustCompile(`^[A-Za-z_:][A-Za-z0-9_:]*$`)
	dnsLabelPattern    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

type webhookPayload struct {
	Version         string `json:"version"`
	TruncatedAlerts int    `json:"truncatedAlerts"`
	Alerts          []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Fingerprint string            `json:"fingerprint"`
		StartsAt    time.Time         `json:"startsAt"`
		EndsAt      time.Time         `json:"endsAt"`
	} `json:"alerts"`
}

func Handler(store incident.Store, webhookToken string, logger *slog.Logger) http.Handler {
	return HandlerWithServices(store, nil, nil, nil, webhookToken, "", logger)
}

func HandlerWithEvidence(store incident.Store, evidenceStore evidence.Store, collector *evidence.Collector, webhookToken string, logger *slog.Logger) http.Handler {
	return HandlerWithServices(store, evidenceStore, collector, nil, webhookToken, "", logger)
}

type Remediator interface {
	Request(context.Context, remediation.Request) (remediation.Result, error)
	Get(context.Context, string) (remediation.Result, error)
}

func HandlerWithServices(store incident.Store, evidenceStore evidence.Store, collector *evidence.Collector, remediator Remediator, webhookToken, remediationToken string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	ingested, _ := otel.Meter("incidentpilot/api").Int64Counter("incidentpilot_alert_signals_total")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "incident storage is not configured", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.Ping(ctx); err != nil {
			http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/v1/alerts/alertmanager", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer("incidentpilot/api").Start(r.Context(), "incident.ingest")
		defer span.End()
		if !authorized(r, webhookToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if store == nil {
			http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
			return
		}
		signals, err := decodeWebhook(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for _, signal := range signals {
			traceContext := telemetry.Capture(ctx)
			signal.TraceID = telemetry.TraceID(ctx)
			signal.TraceParent = traceContext.TraceParent
			signal.TraceState = traceContext.TraceState
			logger.InfoContext(ctx, "incident signal received", "fingerprint", signal.Fingerprint, "trace_id", signal.TraceID)
			result, err := store.Upsert(ctx, signal)
			if err != nil {
				logger.ErrorContext(ctx, "incident ingestion failed", "error", err)
				http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
				return
			}
			ingested.Add(ctx, 1, metric.WithAttributes(attribute.String("status", string(signal.Status))))
			logger.InfoContext(ctx, "incident signal ingested", "incident_id", result.ID, "alert_name", result.AlertName, "status", result.Status)
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(signals)})
	})
	mux.HandleFunc("GET /api/v1/incidents", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, webhookToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if store == nil {
			http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
			return
		}
		limit := 20
		if value := r.URL.Query().Get("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		results, err := store.List(ctx, limit)
		if err != nil {
			logger.ErrorContext(ctx, "list incidents failed", "error", err)
			http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"incidents": results})
	})
	mux.HandleFunc("GET /api/v1/incidents/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, webhookToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if store == nil {
			http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		if _, err := uuid.Parse(id); err != nil {
			http.Error(w, "invalid incident ID", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		result, err := store.Get(ctx, id)
		if errors.Is(err, incident.ErrNotFound) {
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "get incident failed", "error", err)
			http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /api/v1/incidents/{id}/evidence/collect", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, webhookToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if store == nil || collector == nil {
			http.Error(w, "evidence collection unavailable", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		if _, err := uuid.Parse(id); err != nil {
			http.Error(w, "invalid incident ID", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
		defer cancel()
		inc, err := store.Get(ctx, id)
		if errors.Is(err, incident.ErrNotFound) {
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "load incident for evidence failed", "error", err)
			http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
			return
		}
		report, err := collector.Collect(ctx, inc)
		if errors.Is(err, evidence.ErrWindowUnavailable) || errors.Is(err, evidence.ErrUnsupportedIncident) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "evidence collection failed", "incident_id", id, "error", err)
			http.Error(w, "evidence collection failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusCreated, report)
	})
	mux.HandleFunc("GET /api/v1/incidents/{id}/evidence", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, webhookToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if store == nil || evidenceStore == nil {
			http.Error(w, "evidence storage unavailable", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		if _, err := uuid.Parse(id); err != nil {
			http.Error(w, "invalid incident ID", http.StatusBadRequest)
			return
		}
		limit := 20
		if value := r.URL.Query().Get("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 100 {
				http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, err := store.Get(ctx, id); errors.Is(err, incident.ErrNotFound) {
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "incident storage unavailable", http.StatusServiceUnavailable)
			return
		}
		records, err := evidenceStore.ListByIncident(ctx, id, limit)
		if err != nil {
			logger.ErrorContext(ctx, "list evidence failed", "incident_id", id, "error", err)
			http.Error(w, "evidence storage unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"evidence": records})
	})
	mux.HandleFunc("POST /api/v1/remediations", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer("incidentpilot/api").Start(r.Context(), "remediation.receive")
		defer span.End()
		if !authorized(r, remediationToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if remediator == nil {
			http.Error(w, "remediation unavailable", http.StatusServiceUnavailable)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRemediationBytes))
		decoder.DisallowUnknownFields()
		var request remediation.Request
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid remediation JSON body", http.StatusBadRequest)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			http.Error(w, "unexpected data after remediation JSON body", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		result, err := remediator.Request(ctx, request)
		if errors.Is(err, remediation.ErrInvalidProposal) {
			http.Error(w, "invalid remediation proposal", http.StatusUnprocessableEntity)
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "remediation request failed", "incident_id", request.IncidentID, "error", err)
			http.Error(w, "remediation evaluation unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	})
	mux.HandleFunc("GET /api/v1/remediations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, remediationToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if remediator == nil {
			http.Error(w, "remediation unavailable", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		if _, err := uuid.Parse(id); err != nil {
			http.Error(w, "invalid remediation ID", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		result, err := remediator.Get(ctx, id)
		if errors.Is(err, incident.ErrNotFound) {
			http.Error(w, "remediation not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "remediation storage unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	return otelhttp.NewHandler(mux, "incidentpilot.api")
}

func authorized(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	provided := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	expected := sha256.Sum256([]byte("Bearer " + token))
	return subtle.ConstantTimeCompare(provided[:], expected[:]) == 1
}

func decodeWebhook(w http.ResponseWriter, r *http.Request) ([]incident.Signal, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	var payload webhookPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("invalid Alertmanager JSON body")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("unexpected data after Alertmanager JSON body")
	}
	if payload.Version != "4" || payload.TruncatedAlerts != 0 || len(payload.Alerts) == 0 || len(payload.Alerts) > 50 {
		return nil, errors.New("unsupported Alertmanager payload version or alert count")
	}
	signals := make([]incident.Signal, 0, len(payload.Alerts))
	for _, alert := range payload.Alerts {
		labels := alert.Labels
		if !fingerprintPattern.MatchString(alert.Fingerprint) ||
			len(labels["alertname"]) > 128 || !namePattern.MatchString(labels["alertname"]) ||
			len(labels["namespace"]) > 63 || !dnsLabelPattern.MatchString(labels["namespace"]) ||
			len(labels["service"]) > 63 || !dnsLabelPattern.MatchString(labels["service"]) ||
			(labels["severity"] != "warning" && labels["severity"] != "critical") ||
			alert.StartsAt.IsZero() {
			return nil, errors.New("invalid Alertmanager alert identity or labels")
		}
		signal := incident.Signal{
			Fingerprint: alert.Fingerprint, AlertName: labels["alertname"], Namespace: labels["namespace"],
			Service: labels["service"], Severity: labels["severity"], StartedAt: alert.StartsAt.UTC(),
		}
		switch alert.Status {
		case "firing":
			signal.Status = incident.StatusDetected
		case "resolved":
			if alert.EndsAt.IsZero() || alert.EndsAt.Before(alert.StartsAt) {
				return nil, errors.New("resolved alert needs a valid end time")
			}
			ended := alert.EndsAt.UTC()
			signal.ResolvedAt = &ended
			signal.Status = incident.StatusResolved
		default:
			return nil, errors.New("invalid Alertmanager alert status")
		}
		signals = append(signals, signal)
	}
	return signals, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
