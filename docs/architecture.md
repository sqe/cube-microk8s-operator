# Architecture and operations

## Runtime topology

```text
REST client ──▶ Service :4000 ──▶ Cube API Deployment (1..n)
                                      │
PostgreSQL ◀── telemetry collector    ├──▶ PostgreSQL telemetry table
     ▲                                └──▶ Cube Store :3030
     │                                           │
Kubernetes API (nodes, pods)          refresh worker ──▶ pre-aggregations
                                                  │
                                    local PVC (demo) or shared S3 (production)
```

The demo collector lists nodes and pods using read-only RBAC, records timestamped
health observations in PostgreSQL, and prunes records after its configured
retention. Cube's YAML model maps that table to dimensions and measures.

## Controller contract

Two separately packaged implementations share this contract:

- Go/controller-runtime (`config/default`) is the production default and uses
  Kubernetes Lease leader election.
- Python/Kopf (`config/kopf`, `python/`) is an optional one-replica standalone
  alternative with focused pure resource-builder tests.

Only one may run for a cluster or `CubeCluster` at a time. Both own the same
objects and write the same status; concurrent operation creates field/status
races. Stop the old controller before applying the other implementation.

`CubeCluster` references a model ConfigMap and configuration Secret in its own
namespace. Reconciliation creates or updates:

- API and refresh-worker Deployments and the API Service;
- one Cube Store Deployment/Service, or router and worker StatefulSets/Services;
- an API PodDisruptionBudget; and
- an ingress NetworkPolicy that limits Cube Store ports to this Cube instance.

Owner references provide garbage collection. Switching Cube Store mode deletes
the obsolete topology. The controller preserves Kubernetes-assigned Service IP,
IP-family, health-check port, and NodePort fields while applying desired state.
It validates references, replica counts, storage shape, scratch quantity, and
optional S3 credentials before creating workloads.

The `Ready` condition is true only when expected API, refresh worker, and Cube
Store replicas are ready. `status.endpoint` is the in-cluster API URL. API pods
use Cube Core's unprefixed `/readyz` and `/livez` probes. Clients use the default
`/cubejs-api/v1/meta` and `/cubejs-api/v1/load` REST paths.

## Cube Store storage

Scratch `/cube/data` is ephemeral and can be size-limited. Remote storage is
separate:

- `persistentVolume` mounts an existing claim at `/cube/remote`; the operator
  does not provision or back it up. For multiple Cube Store pods it must support
  the required concurrent access and shared-filesystem semantics.
- `s3` configures bucket and region. An optional Secret supplies Cube Store
  environment variables. Prefer workload identity over static keys where the
  platform supports it.

Cube's production architecture is API instances + a refresh worker + Cube Store.
Use clustered Cube Store with shared durable remote storage for significant
production concurrency; the single-node mode exists for constrained workloads
and the Kind demo.

## Security boundary

The operator has cluster-wide watch/write access only for the workload types it
owns, read-only access to referenced ConfigMaps/Secrets/PVCs, event access, and
Lease access for Go leader election. Workload containers drop Linux capabilities and disallow
privilege escalation. Cube Store has no external Service; its NetworkPolicy
allows ingress only from pods of the same Cube instance.

Add namespace egress policy, TLS ingress, external secret management, registry
policy, monitoring, and durable backups according to the target cluster. Do not
put database, Cube API, S3, DNS, or Proxmox credentials in Git or Terraform
variable files/state shared outside their trust boundary.

## Infrastructure boundary

Terraform clones Ubuntu cloud-init VMs in Proxmox and the bootstrap script forms
the MicroK8s HA cluster. It intentionally does not deploy the operator or Cube.
MetalLB is LAN exposure only; ESDDNS and edge forwarding remain explicit
operator responsibilities. This separation prevents transient Kubernetes
bootstrap failures from tainting VM resources and keeps cluster application
lifecycle independent from VM lifecycle.
