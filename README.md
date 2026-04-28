# livellm-browser-operator

Kubernetes operator that manages **livellm browser** instances via Custom Resources.

Each `Browser` CR results in:
- a **Deployment** (one Chrome pod per browser)
- a **PVC** (persistent profile data)
- a **Service** (launcher API access)

The operator discovers the CDP WebSocket URL from the in-pod launcher and
writes it to `Browser` status. To connect the **livellm controller**, create a
`Controller` CR in the same namespace — it deploys the controller and registers
running browsers via `POST /parser/browsers`.

---

## Architecture

```
 kubectl apply ──────► ┌────────────────────┐     ┌─────────────────────┐
                        │   Browser CRD      │     │  Controller CRD     │
                        └────────┬───────────┘     └──────────┬──────────┘
                                 │ watch                      │ watch
                        ┌────────▼────────────────────────────▼──────────┐
                        │              Operator (this project)            │
                        └──┬───────────────────────────────┬───────────────┘
                           │                               │
                ┌──────────▼────────┐            ┌─────────▼──────────┐
                │ Browser Deployment│            │ Controller Deploy  │
                │  (Chrome + API)   │            │  (Playwright API)  │
                └───────────────────┘            └────────────────────┘
```

## Prerequisites

- Go 1.22+
- A Kubernetes cluster (kind, minikube, EKS, GKE, …)
- `kubectl` configured for your cluster
- Docker (for building images)

## Quick Start

```bash
# 1. Install the CRD
make install-crd

# 2. Run the operator locally (for development)
make run

# 3. In another terminal — create a browser
kubectl apply -f deploy/examples/browser.yaml

# 4. Check status
kubectl get browsers
kubectl get br my-browser -o wide          # shows WS URL + Pod IP
kubectl get br my-browser -o yaml          # full status
```

## Deploy to Cluster

```bash
# Build the operator image
make docker-build IMG=myregistry/livellm-browser-operator:latest

# Push it
docker push myregistry/livellm-browser-operator:latest

# Update deploy/operator.yaml with your image, then:
make deploy
```

## Usage

### Create a browser

```yaml
apiVersion: livellm.io/v1alpha1
kind: Browser
metadata:
  name: my-browser
spec:
  profileUid: "default"
  storage: "1Gi"
  shmSize: "4Gi"
  resources:
    requests:
      cpu: "500m"
      memory: "2Gi"
    limits:
      cpu: "1"
      memory: "4Gi"
  reclaimPolicy: Retain    # Retain | Delete
```

### With proxy

```yaml
apiVersion: livellm.io/v1alpha1
kind: Browser
metadata:
  name: us-browser
spec:
  profileUid: "us-east"
  proxy:
    server: "http://proxy:8080"
    username: "user"
    password: "pass"
```

### Connecting the controller

Deploy a `Controller` CR (same namespace as your browsers). The operator creates
the controller workload, which discovers browsers via Redis (`livellm:browsers`)
and reconciles drift on a 10-second sync loop. See `deploy/examples/controller.yaml`.

### Tuning the Node.js heap

The **controller** pod is Node-only (no Chrome). The operator auto-sizes
`NODE_OPTIONS=--max-old-space-size=N` from the pod's memory limit:
`N = min(limit / 2, 4096)` MiB, floored at 512 MiB. So a 2 GiB controller
gets 1024, a 4 GiB gets 2048, anything ≥ 8 GiB caps at 4096. Override via
`spec.env` (or `DEFAULT_CONTROLLER_ENV`) only when the auto value is
unsuitable — duplicate env entries are last-write-wins.

The **browser** pod hosts Chrome itself, so the operator deliberately does
**not** set `NODE_OPTIONS` there — Chrome must keep the bulk of the pod's
memory budget. Set it explicitly via `spec.env` only if you have measured
that the in-pod Node driver, not Chrome, is the memory consumer.

```yaml
spec:
  env:
    - name: NODE_OPTIONS
      value: "--max-old-space-size=6144"
```

### Default resources

The chart exposes `browser.resources` and `controller.resources` (passed to
the operator as `DEFAULT_BROWSER_RESOURCES` / `DEFAULT_CONTROLLER_RESOURCES`).
Precedence on each pod: the CR's `spec.resources` wins → chart default →
operator built-in fallback.

### Desired state via Redis

The operator writes a per-browser desired state to Redis (`livellm:desired:browsers`)
containing `extensions`, `cookies`, and `proxy`. The browser pod polls every 10s
and applies any drift through a profile-preserving Chrome restart. Adding extensions,
rotating cookies, or changing proxy at runtime is therefore non-destructive.

---

## Development

### Updating the CRD

When you change types in `api/v1alpha1/browser_types.go`:

```bash
# 1. Regenerate DeepCopy + CRD manifest in one command
make gen

# This runs:
#   controller-gen object paths="./api/..."        → zz_generated.deepcopy.go
#   controller-gen crd paths="./api/..." ...       → deploy/crd.yaml

# 2. Apply the updated CRD to your cluster
make install-crd

# 3. Rebuild the operator
make build
```

### Individual generation targets

```bash
make generate    # DeepCopy only  →  api/v1alpha1/zz_generated.deepcopy.go
make manifests   # CRD only       →  deploy/crd.yaml
```

### Build & test

```bash
make tidy        # go mod tidy
make build       # compile to bin/operator
make run         # run locally (no leader election)
make vet         # go vet
```

### Makefile reference

| Target           | Description                                      |
|------------------|--------------------------------------------------|
| `make build`     | Compile the operator binary                      |
| `make run`       | Run locally with `--leader-elect=false`           |
| `make gen`       | Regenerate DeepCopy + CRD (**run after editing types**) |
| `make generate`  | Regenerate DeepCopy only                         |
| `make manifests` | Regenerate CRD YAML only                         |
| `make docker-build` | Build Docker image                            |
| `make deploy`    | `kubectl apply -k deploy/`                       |
| `make undeploy`  | `kubectl delete -k deploy/`                      |
| `make install-crd` | Apply just the CRD                             |
| `make tidy`      | `go mod tidy`                                    |

---

## Project Structure

```
├── main.go                              # Entry point
├── api/v1alpha1/
│   ├── browser_types.go                 # CRD Go types  ← edit this
│   ├── groupversion_info.go             # GVK registration
│   └── zz_generated.deepcopy.go         # generated — do not edit
├── internal/controller/
│   ├── browser_controller.go            # Reconciler
│   └── resources.go                     # PVC / Deployment / Service builders
├── deploy/
│   ├── crd.yaml                         # generated CRD manifest
│   ├── rbac.yaml                        # ServiceAccount + ClusterRole
│   ├── operator.yaml                    # Operator Deployment
│   ├── namespace.yaml                   # livellm-system namespace
│   ├── kustomization.yaml               # kustomize entry point
│   └── examples/
│       └── browser.yaml                 # Sample Browser CRs
├── Dockerfile                           # Multi-stage distroless build
├── Makefile
├── go.mod
└── go.sum
```
