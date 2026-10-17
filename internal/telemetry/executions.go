package telemetry

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const executionSchema = `
CREATE TABLE IF NOT EXISTS agent_executions (
  event_id text PRIMARY KEY,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz NOT NULL,
  ended_at timestamptz NOT NULL,
  tenant_id text NOT NULL,
  cluster_name text NOT NULL,
  namespace text NOT NULL,
  trace_id text NOT NULL,
  run_id text NOT NULL,
  parent_id text NOT NULL,
  agent_name text NOT NULL,
  kind text NOT NULL,
  name text NOT NULL,
  status text NOT NULL,
  duration_ms double precision NOT NULL,
  input_tokens bigint NOT NULL,
  output_tokens bigint NOT NULL,
  cost_usd numeric NOT NULL,
  payload jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_executions_run_idx ON agent_executions (tenant_id, run_id);
CREATE INDEX IF NOT EXISTS agent_executions_time_idx ON agent_executions (ended_at);
`

// Execution is an immutable, completed span. IDs connect spans without requiring
// arrival order or storing prompts, credentials, or private model reasoning.
type Execution struct {
	EventID      string    `json:"eventId"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt"`
	TenantID     string    `json:"tenantId"`
	Cluster      string    `json:"cluster"`
	Namespace    string    `json:"namespace"`
	TraceID      string    `json:"traceId"`
	RunID        string    `json:"runId"`
	ParentID     string    `json:"parentId"`
	Agent        string    `json:"agent"`
	Kind         string    `json:"kind"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	SkillVersion string    `json:"skillVersion"`
	Tool         string    `json:"tool"`
	TargetAgent  string    `json:"targetAgent"`
	ArtifactURI  string    `json:"artifactUri"`
	ArtifactType string    `json:"artifactType"`
	Validation   string    `json:"validation"`
	Model        string    `json:"model"`
	Provider     string    `json:"provider"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	CostUSD      float64   `json:"costUsd"`
	ErrorType    string    `json:"errorType"`
}

func (e Execution) validate(now time.Time, retention time.Duration) error {
	for name, value := range map[string]string{
		"eventId": e.EventID, "tenantId": e.TenantID, "cluster": e.Cluster,
		"namespace": e.Namespace, "traceId": e.TraceID, "runId": e.RunID,
		"agent": e.Agent, "kind": e.Kind, "name": e.Name, "status": e.Status,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if e.StartedAt.IsZero() || e.EndedAt.IsZero() || e.EndedAt.Before(e.StartedAt) || e.EndedAt.After(now.Add(5*time.Minute)) || e.EndedAt.Before(now.Add(-retention)) {
		return errors.New("timestamps must be ordered, within retention, and not in the future (5m clock tolerance)")
	}
	if e.Status != "succeeded" && e.Status != "failed" && e.Status != "cancelled" {
		return errors.New("status must be succeeded, failed, or cancelled")
	}
	if e.InputTokens < 0 || e.OutputTokens < 0 || e.CostUSD < 0 {
		return errors.New("tokens and cost must be nonnegative")
	}
	if e.ParentID == e.EventID {
		return errors.New("parentId cannot reference the event itself")
	}
	switch e.Kind {
	case "run":
	case "skill":
		if e.SkillVersion == "" {
			return errors.New("skill requires skillVersion")
		}
	case "tool":
		if e.Tool == "" {
			return errors.New("tool requires tool")
		}
	case "handoff":
		if e.TargetAgent == "" {
			return errors.New("handoff requires targetAgent")
		}
	case "artifact":
		if e.ArtifactURI == "" || e.ArtifactType == "" {
			return errors.New("artifact requires artifactUri and artifactType")
		}
	case "validation":
		if e.Validation != "passed" && e.Validation != "failed" && e.Validation != "skipped" {
			return errors.New("validation requires passed, failed, or skipped validation")
		}
	case "model":
		if e.Model == "" || e.Provider == "" {
			return errors.New("model requires model and provider")
		}
	default:
		return errors.New("kind must be run, skill, tool, handoff, artifact, validation, or model")
	}
	if e.Kind != "model" && (e.InputTokens != 0 || e.OutputTokens != 0 || e.CostUSD != 0) {
		return errors.New("only model spans may report tokens/cost; do not double-count run totals")
	}
	return nil
}

type executionAPI struct {
	database  *pgxpool.Pool
	token     string
	retention time.Duration
}

func (a *executionAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.Method == http.MethodGet && r.URL.Path == "/readyz" {
		if err := a.database.Ping(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Path != "/v1/executions" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	expected := sha256.Sum256([]byte("Bearer " + a.token))
	if a.token == "" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var event Execution
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "invalid execution JSON (maximum 64 KiB)", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "expected one JSON object", http.StatusBadRequest)
		return
	}
	if err := event.validate(time.Now(), a.retention); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Normalize timestamps so equivalent offsets produce identical replay payloads.
	event.StartedAt, event.EndedAt = event.StartedAt.UTC(), event.EndedAt.UTC()
	payload, err := json.Marshal(event)
	if err != nil {
		http.Error(w, "invalid execution", http.StatusBadRequest)
		return
	}
	var matches bool
	err = a.database.QueryRow(ctx, `INSERT INTO agent_executions (
event_id, started_at, ended_at, tenant_id, cluster_name, namespace, trace_id, run_id,
parent_id, agent_name, kind, name, status, duration_ms, input_tokens, output_tokens, cost_usd, payload
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT (event_id) DO UPDATE SET event_id = agent_executions.event_id
RETURNING payload = $18::jsonb`, event.EventID, event.StartedAt, event.EndedAt,
		event.TenantID, event.Cluster, event.Namespace, event.TraceID, event.RunID,
		event.ParentID, event.Agent, event.Kind, event.Name, event.Status,
		event.EndedAt.Sub(event.StartedAt).Seconds()*1000, event.InputTokens, event.OutputTokens,
		event.CostUSD, payload).Scan(&matches)
	if err != nil {
		slog.Error("execution persistence failed", "error", err)
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	if !matches {
		http.Error(w, "eventId already exists with a different payload", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunExecutions runs independently of Kubernetes access. Producers authenticate
// with a separate ingestion credential, never with the Cube signing secret.
func RunExecutions(ctx context.Context) error {
	url, token := os.Getenv("DATABASE_URL"), os.Getenv("AGENT_INGEST_TOKEN")
	if url == "" || len(token) < 32 {
		return errors.New("DATABASE_URL and AGENT_INGEST_TOKEN (at least 32 characters) are required")
	}
	retention, err := durationFromEnvironment("RETENTION", 7*24*time.Hour)
	if err != nil {
		return err
	}
	database, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer database.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = database.Exec(startup, executionSchema)
	cancel()
	if err != nil {
		return fmt.Errorf("initialize execution schema: %w", err)
	}
	address := os.Getenv("AGENT_INGEST_ADDRESS")
	if address == "" {
		address = ":8082"
	}
	server := &http.Server{Addr: address, Handler: &executionAPI{database: database, token: token, retention: retention}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return server.Shutdown(shutdown)
		case <-ticker.C:
			prune, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := database.Exec(prune, "DELETE FROM agent_executions WHERE ended_at < $1", time.Now().Add(-retention))
			cancel()
			if err != nil {
				slog.Error("execution retention failed", "error", err)
			}
		}
	}
}
