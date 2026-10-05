# livellm-browser-operator

Kubernetes operator that manages **livellm browser** instances via Custom Resources.

Each `Browser` CR results in:
- a **Deployment** (one browser pod per browser: Chrome, or Camoufox with `spec.engine: camoufox`)
- a **PVC** (persistent profile data)
- a **Service** (launcher API access)

The operator writes each browser's deterministic automation WebSocket URL
(Chrome: `ws://<name>.<namespace>.svc.cluster.local:9222/devtools/browser/<profileUid>`;
Camoufox: `ws://<name>.<namespace>.svc.cluster.local:9222/playwright/default`)
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

A username or password set in `spec.proxy` reaches the browser container as
env, so anyone who can connect to the browser can read it. To keep proxy
logins out of the browser container, use the control sidecar below and point
`spec.proxy.server` at its relay.

### Control sidecar

`spec.control` adds a control sidecar to the browser pod. It holds
proxy logins and relays the browser's traffic (`127.0.0.1:3128`), rotates
between proxies, and takes profile snapshots. It reads its key and settings
from the named Secret, which only the sidecar mounts.

```yaml
spec:
  control:
    secretName: my-browser-control
  proxy:
    server: "http://127.0.0.1:3128"   # the sidecar's relay; no login here
```

With `spec.control` set the operator renders:

| What | Value |
|---|---|
| sidecar | native sidecar (`initContainers`, `restartPolicy: Always`), same image as the browser (its sidecar binary), port 9300 |
| sidecar security | uid/gid 1000, read-only root filesystem, no privilege escalation, all capabilities dropped |
| sidecar resources | requests 25m / 48Mi, limits 300m / 192Mi |
| sidecar mounts | the profile volume, the Secret (optional, mode 0440) and a memory `emptyDir` (8Mi); the last two in the sidecar only |
| sidecar env | the launcher address; with `spec.proxy.server` set, the relay is required (it refuses traffic until it has settings) |
| sidecar probes | startup: TCP 9300 every 1s, 30 tries; liveness: `GET /healthz` every 20s; no readiness probe |
| browser container | no privilege escalation and all capabilities dropped (so `sudo` doesn't work in it) |
| Service | an extra port 9300 (never exposed by an Ingress) |

The browser image must contain the sidecar binary (livellm-browser 2.3.0 or
later). Without `spec.control` the pod and Service are rendered exactly as
before.

### Engines (Chrome, Camoufox)

`spec.engine` picks the browser engine: `chrome` (absent means chrome) or
`camoufox` (Firefox-based, driven with Playwright). The engine is set when the
browser is made and doesn't change afterwards. A chrome (or engine-less)
browser renders exactly as before the field existed.

| | Chrome | Camoufox |
|---|---|---|
| image (no `spec.image`) | `DEFAULT_BROWSER_IMAGE` | `DEFAULT_CAMOUFOX_IMAGE` |
| automation port (container + Service) | `cdp` 9222, env `CDP_PORT` | `playwright` 9222, env `AUTOMATION_PORT` |
| `status.wsUrl` | `ws://<name>.<ns>.svc.cluster.local:9222/devtools/browser/<profileUid>` | `ws://<name>.<ns>.svc.cluster.local:9222/playwright/default` |
| control sidecar | as described above | the same, set to serve a Camoufox profile |
| `spec.extensions` | installed | not installed (`status.message` says so) |

The pull policy is `DEFAULT_CAMOUFOX_PULL_POLICY`. With no camoufox image
configured and no `spec.image`, a camoufox Browser renders nothing and its
`status.message` reads "Camoufox isn't offered on this platform". One that
already runs keeps the image it runs and stays managed (stop, edits), with a
note in `status.message`.

A `Controller` has no engine: one controller image (`DEFAULT_CONTROLLER_IMAGE`)
drives browsers of both engines, and one pool may hold both. Its pod renders
the same whatever engines its members run. Each registry entry follows its
own browser's engine:

| member | registry entry |
|---|---|
| Chrome Browser | `"<id>": "<wsUrl>"` |
| Camoufox Browser | `"<id>": {"wsUrl": "<wsUrl>", "engine": "camoufox"}` |
| remote browser (`externalBrowsers`, Chrome) | `"<id>": "<wsUrl>"`, or `{"wsUrl": ..., "headers": {...}}` with a sign-in value |

A Camoufox member needs a controller image that reads the `engine` entry
(controller 2.6.0 or later); an older one leaves only that member unhealthy.
Autoscaled browsers (`autoscaleBrowser`) are Chrome browsers.

The engine can't change after creation: the Browser CRD refuses an update
that changes `spec.engine` (absent counts as chrome). A camoufox browser's
profile disk carries the annotation `livellm.io/engine: camoufox`; a Browser
without the camoufox engine on such a disk is never rendered as chrome. Its
Deployment and Service are left as they are (only `spec.running=false` still
scales it to zero), `status.wsUrl` is cleared, and `status.message` says why.

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

- `autodiscover` unset or `true`: every ready browser in the namespace, of
  either engine (filtered by `browserSelector`). `false`: only `browsers`
  (either engine) and `externalBrowsers`.
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
