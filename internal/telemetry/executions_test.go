package telemetry

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExecutionAPI(t *testing.T) {
	api := &executionAPI{token: strings.Repeat("a", 32), retention: 7 * 24 * time.Hour}
	post := func(body, token string, want int) {
		t.Helper()
		request := httptest.NewRequest("POST", "/v1/executions", strings.NewReader(body))
		request.Header.Set("Authorization", token)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("HTTP %d, want %d: %s", response.Code, want, response.Body.String())
		}
	}
	credential := "Bearer " + api.token
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := Execution{EventID: "test-" + now.Format("20060102T150405.000000"), StartedAt: now.Add(-3 * time.Second), EndedAt: now,
		TenantID: "tenant-a", Cluster: "kind", Namespace: "demo", TraceID: "trace", RunID: "run", Agent: "worker", Kind: "run", Name: "analytics.usage", Status: "succeeded"}
	body := func(e Execution) string {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	t.Run("reject unauthenticated and malformed requests before storage", func(t *testing.T) {
		post(body(event), "", 401)
		post(body(event), "Bearer wrong", 401)
		for _, b := range []string{"null", "[]", "{}", body(event) + "{}", `{"prompt":"secret"}`, strings.Repeat("x", 65537)} {
			post(b, credential, 400)
		}
	})
	t.Run("reject invalid span contracts", func(t *testing.T) {
		mutations := []func(*Execution){
			func(e *Execution) { e.RunID = "" },
			func(e *Execution) { e.Status = "running" },
			func(e *Execution) { e.Kind = "unknown" },
			func(e *Execution) { e.EndedAt = e.StartedAt.Add(-time.Second) },
			func(e *Execution) { e.EndedAt = now.Add(6 * time.Minute) },
			func(e *Execution) { e.EndedAt = now.Add(-8 * 24 * time.Hour); e.StartedAt = e.EndedAt },
			func(e *Execution) { e.InputTokens = -1 },
			func(e *Execution) { e.InputTokens = 1 },
			func(e *Execution) { e.ParentID = e.EventID },
		}
		for _, kind := range []string{"skill", "tool", "handoff", "artifact", "validation", "model"} {
			e := event
			e.Kind = kind
			post(body(e), credential, 400)
		}
		for _, mutate := range mutations {
			e := event
			mutate(&e)
			post(body(e), credential, 400)
		}
	})
	t.Run("persist immutable spans and deduplicate retries", func(t *testing.T) {
		url := os.Getenv("TEST_DATABASE_URL")
		if url == "" {
			t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration coverage")
		}
		ctx := context.Background()
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if _, err := pool.Exec(ctx, executionSchema); err != nil {
			t.Fatal(err)
		}
		api.database = pool
		defer func() {
			_, _ = pool.Exec(ctx, "DELETE FROM agent_executions WHERE event_id = $1", event.EventID)
		}()
		post(body(event), credential, 204)
		post(body(event), credential, 204)
		equivalent := event
		equivalent.EndedAt = event.EndedAt.In(time.FixedZone("offset", 3600))
		post(body(equivalent), credential, 204)
		conflict := event
		conflict.TenantID = "tenant-b"
		post(body(conflict), credential, 409)
		var count, duration int
		var tenant string
		if err := pool.QueryRow(ctx, "SELECT count(*), max(duration_ms)::int, max(tenant_id) FROM agent_executions WHERE event_id = $1", event.EventID).Scan(&count, &duration, &tenant); err != nil {
			t.Fatal(err)
		}
		if count != 1 || duration != 3000 || tenant != "tenant-a" {
			t.Fatalf("persisted count/duration/tenant = %d/%d/%s", count, duration, tenant)
		}
		closed, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		closed.Close()
		api.database = closed
		post(body(event), credential, 503)
	})
}
