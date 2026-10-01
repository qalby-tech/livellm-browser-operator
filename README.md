# livellm-browser-operator

Kubernetes operator that manages **livellm browser** instances via Custom Resources.

Each `Browser` CR results in:
- a **Deployment** (one Chrome pod per browser)
- a **PVC** (persistent profile data)
- a **Service** (launcher API access)

The operator writes each browser's deterministic CDP WebSocket URL
(`ws://<name>.<namespace>.svc.cluster.local:9222/devtools/browser/<profileUid>`)
to `Browser` status. To connect the **livellm controller**, create a
`Controller` CR in the same namespace — it deploys the controller and hands it
the namespace's browsers through a registry Secret.

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

### Pinning to nodes

Both kinds take `spec.nodeSelector`, a plain passthrough to the pod's
`nodeSelector` (empty by default, so the scheduler picks). Changing it rolls
the pod.

| Kind | Field | Effect |
|---|---|---|
| `Browser` | `spec.nodeSelector` | node labels the browser pod must match |
| `Controller` | `spec.nodeSelector` | node labels the controller pods must match |

```yaml
spec:
  nodeSelector:
    kubernetes.io/hostname: node-a
```

### Connecting the controller

Deploy a `Controller` CR (same namespace as your browsers). The operator creates
the controller workload and writes a `<controller>-browsers` Secret mapping
each ready browser's `profileUid` to its deterministic Service ws_url; the
controller mounts it at `BROWSERS_CONFIG` and resolves `X-Browser-Id` (and the
`/browsers/<id>/` path) against it. Two browsers with the same id: the first
(by name) is used and `status.message` says so.

- `autodiscover` unset or `true`: every ready browser in the namespace
  (filtered by `browserSelector`). `false`: only `browsers` and
  `externalBrowsers`.
- `externalBrowsers[].authHeader` (or `authHeaderSecretRef: {name, key}`, which
  wins; only a Secret labelled `livellm.io/remote-browser-auth: "true"` is
  read, any other counts as missing): `"Name: value"` when the text before the first `:` is a header name
  (letters, digits, `-`), otherwise the whole value is sent as
  `Authorization: <value>` (so `Bearer abc` works).
- `status.registeredBrowsers[]` carries each browser's `openTabs` and
  `pageCount` (sessions), read from the controller's `GET /parser/browsers`
  every minute. `openTabs` is left out when the controller could not be
  asked (unknown, not zero). It carries no addresses.

See `deploy/examples/controller.yaml`.

### Tuning the Node.js heap

The **controller** pod is Node-only (no Chrome). The operator auto-sizes
`NODE_OPTIONS=--max-old-space-size=N` from the pod's memory limit:
`N = max(limit / 2, limit - 2 GiB)` MiB, clamped to 512-8192. So a 2 GiB
controller gets 1024, a 4 GiB gets 2048, an 8 GiB gets 6144. Override via
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

### Desired state (declarative)

The operator passes a browser's `extensions` and `proxy` to its pod as env, and
mounts the `cookies` ConfigMap/Secret as a file (`BROWSER_COOKIES_FILE`); the
browser applies them to the default browser at startup. Changing extensions,
cookies, or proxy updates the pod spec and rolls the browser.

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
│   ├── browser_types.go                 # Browser CRD Go types  ← edit this
│   ├── controller_types.go              # Controller CRD Go types  ← edit this
│   ├── groupversion_info.go             # GVK registration
│   └── zz_generated.deepcopy.go         # generated — do not edit
├── internal/controller/
│   ├── browser_controller.go            # Browser reconciler
│   ├── controller_controller.go         # Controller reconciler + browser registry Secret
│   ├── resources.go                     # Browser PVC / Deployment / Service builders
│   ├── controller_resources.go          # Controller Deployment / Service builders
│   ├── pagecounts.go                    # Per-browser page counts from the controller API
│   └── resources_test.go
├── deploy/
│   ├── crd.yaml                         # generated CRD manifests (both CRDs)
│   ├── rbac.yaml                        # ServiceAccount + ClusterRole
│   ├── operator.yaml                    # Operator Deployment
│   ├── namespace.yaml                   # livellm-system namespace
│   ├── kustomization.yaml               # kustomize entry point
│   └── examples/
│       ├── browser.yaml                 # Sample Browser CR
│       └── controller.yaml              # Sample Controller CR
├── Dockerfile                           # Multi-stage distroless build
├── Makefile
├── go.mod
└── go.sum
```
