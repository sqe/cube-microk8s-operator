package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const schema = `
CREATE TABLE IF NOT EXISTS kubernetes_telemetry (
  collected_at timestamptz NOT NULL,
  cluster_name text NOT NULL,
  namespace text NOT NULL,
  resource_kind text NOT NULL,
  resource_name text NOT NULL,
  phase text NOT NULL,
  ready boolean NOT NULL,
  restart_count bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS kubernetes_telemetry_collected_at_idx
  ON kubernetes_telemetry (collected_at DESC);
CREATE INDEX IF NOT EXISTS kubernetes_telemetry_resource_idx
  ON kubernetes_telemetry (resource_kind, namespace, resource_name);
`

type Collector struct {
	Kubernetes *kubernetes.Clientset
	Database   *pgxpool.Pool
	Cluster    string
	Interval   time.Duration
	Retention  time.Duration
}

func New(ctx context.Context) (*Collector, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	database, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	interval, err := durationFromEnvironment("COLLECTION_INTERVAL", time.Minute)
	if err != nil {
		database.Close()
		return nil, err
	}
	retention, err := durationFromEnvironment("RETENTION", 7*24*time.Hour)
	if err != nil {
		database.Close()
		return nil, err
	}
	cluster := os.Getenv("CLUSTER_NAME")
	if cluster == "" {
		cluster = "kubernetes"
	}
	return &Collector{Kubernetes: kube, Database: database, Cluster: cluster, Interval: interval, Retention: retention}, nil
}

func durationFromEnvironment(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("parse %s: duration must be positive", name)
	}
	return duration, nil
}

func (c *Collector) Run(ctx context.Context) error {
	defer c.Database.Close()
	for {
		if err := c.Collect(ctx); err != nil {
			slog.Error("telemetry collection failed", "error", err)
		} else {
			slog.Info("telemetry collected", "cluster", c.Cluster)
		}
		timer := time.NewTimer(c.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (c *Collector) Collect(ctx context.Context) error {
	if _, err := c.Database.Exec(ctx, schema); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	nodes, err := c.Kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	pods, err := c.Kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}

	rows := make([][]any, 0, len(nodes.Items)+len(pods.Items))
	now := time.Now().UTC()
	for _, node := range nodes.Items {
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				ready = condition.Status == corev1.ConditionTrue
				break
			}
		}
		rows = append(rows, []any{now, c.Cluster, "", "Node", node.Name, string(node.Status.Phase), ready, int64(0)})
	}
	for _, pod := range pods.Items {
		ready, restarts := false, int64(0)
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady {
				ready = condition.Status == corev1.ConditionTrue
				break
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			restarts += int64(status.RestartCount)
		}
		rows = append(rows, []any{now, c.Cluster, pod.Namespace, "Pod", pod.Name, string(pod.Status.Phase), ready, restarts})
	}

	tx, err := c.Database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin telemetry transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	columns := []string{"collected_at", "cluster_name", "namespace", "resource_kind", "resource_name", "phase", "ready", "restart_count"}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"kubernetes_telemetry"}, columns, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("write telemetry: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM kubernetes_telemetry WHERE collected_at < now() - $1::interval", strconv.FormatFloat(c.Retention.Hours(), 'f', -1, 64)+" hours"); err != nil {
		return fmt.Errorf("prune telemetry: %w", err)
	}
	return tx.Commit(ctx)
}
