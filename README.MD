# Gopher-Log-Transporter
A custom log shipping agent built in Go that tails Kubernetes pod log files in real-time, parses them, and ships them to Grafana Loki.
---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Kind K8s Cluster                      │
│                                                         │
│  ┌─────────────┐     ┌──────────────────────────────┐  │
│  │ Busybox Pod │     │   Gopher-Log-Transporter Pod  │  │
│  │             │     │                              │  │
│  │ JSON log    │     │  ┌────────┐  ┌────────┐     │  │
│  │ every 5s    │     │  │ Reader │  │ Parser │     │  │
│  │      │      │     │  │  tail  │→ │  JSON  │     │  │
│  │      ↓      │     │  │ offset │  │ decode │     │  │
│  │   0.log ────┼─────┼→ │rotation│  └───┬────┘     │  │
│  │  on disk    │     │  └────────┘      │           │  │
│  └─────────────┘     │            ┌─────▼──────┐    │  │
│                      │            │   Sender   │    │  │
│                      │            │ batch+retry│    │  │
│                      │            └─────┬──────┘    │  │
│                      └──────────────────┼───────────┘  │
└─────────────────────────────────────────┼───────────────┘
                                          │ HTTP POST
                                          ▼
                              ┌─────────────────────┐
                              │    Grafana Loki      │
                              │   Log Aggregation    │
                              └──────────┬──────────┘
                                         │ LogQL
                                         ▼
                              ┌─────────────────────┐
                              │      Grafana         │
                              │    Dashboard         │
                              └─────────────────────┘
```

### Pipeline

```
Reader ──chan RawLine(1000)──► Parser ──chan LogEntry(1000)──► Sender ──HTTP──► Loki
```

3 goroutines running concurrently, connected by buffered channels.

---

## Tech Stack

| Layer | Technology |
|---|---|
| Language | Go 1.23 |
| Containerization | Docker (multi-stage build) |
| Orchestration | Kubernetes (Kind cluster) |
| Log Aggregation | Grafana Loki |
| Visualization | Grafana |
| OS | Ubuntu Server 22.04 (VMware) |

---

## Project Structure

```
gopher-log-transporter/
├── cmd/
│   └── transporter/
│       └── main.go           # Entry point — wires 3 modules together
├── internal/
│   ├── reader/
│   │   └── tailer.go         # File tailing, offset tracking, rotation detection
│   ├── parser/
│   │   └── parser.go         # CRI log format parsing, JSON decode
│   └── sender/
│       └── sender.go         # Batch buffering, HTTP push to Loki, retry
├── k8s/
│   ├── kind-config.yaml      # Kind cluster with /var/log/pods mount
│   ├── configmap.yaml        # App config (log path, Loki URL)
│   ├── busybox-deployment.yaml
│   ├── gopher-deployment.yaml
│   ├── loki-deployment.yaml
│   └── grafana-deployment.yaml
├── Dockerfile                # Multi-stage build (~15MB final image)
├── .dockerignore
├── .gitignore
└── go.mod
```

---

## Core Modules

### Reader `internal/reader/tailer.go`

- **Log tailing** — polls file every 500ms (polling over inotify for K8s overlay filesystem compatibility)
- **Offset persistence** — saves read position to state file (JSON), survives restarts
- **Rotation detection** — compares inode numbers via `os.Stat()`, seamlessly switches to new file
- **Truncation handling** — detects file size shrinkage, resets offset

### Parser `internal/parser/parser.go`

- **CRI format support** — handles containerd CRI log format:

```
2024-01-01T00:00:00Z stdout F {"level":"info","msg":"heartbeat","ts":123}
```

- **2-layer JSON decode** — unwraps containerd wrapper → app log
- **Label extraction** — parses pod path into Loki labels `{namespace, pod, container}`
- **Fallback** — plain text logs kept as raw string, never dropped

### Sender `internal/sender/sender.go`

- **Batch buffering** — groups up to 100 log entries before flushing
- **Flush interval** — forces flush every 5 seconds regardless of batch size
- **Retry with backoff** — exponential backoff (2s → 4s), max 3 attempts
- **Graceful shutdown** — flushes remaining buffer on SIGTERM

---

## Quick Start

### Prerequisites

- Docker
- Kind
- kubectl
- Go 1.23+

### 1. Clone & Build

```bash
git clone https://github.com/namsson/gopher-log-transporter
cd gopher-log-transporter
```

```bash
docker build -t gopher-log-transporter:latest .
```

### 2. Create Kind Cluster

```bash
# Creates 2-node cluster with /var/log/pods mounted
kind create cluster --config k8s/kind-config.yaml
```

```bash
# Load image into cluster (Kind doesn't pull from Docker Hub)
kind load docker-image gopher-log-transporter:latest --name gopher-cluster
```

### 3. Deploy

```bash
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/loki-deployment.yaml
kubectl apply -f k8s/grafana-deployment.yaml
kubectl apply -f k8s/busybox-deployment.yaml
```

```bash
# Get actual pod log path
kubectl get pods -o wide

# Update ConfigMap with actual path
kubectl patch configmap gopher-config \
  --type merge \
  -p '{"data":{"LOG_FILE_PATH":"/var/log/pods/<actual-pod-path>/busybox/0.log"}}'
```

```bash
kubectl apply -f k8s/gopher-deployment.yaml
```

### 4. Verify

```bash
# Check all pods running
kubectl get pods
```

```bash
# Check Gopher is shipping logs
kubectl logs -f deployment/gopher-log-transporter
```

Expected output:

```json
{"level":"INFO","msg":"tailer starting"}
{"level":"INFO","msg":"parser starting"}
{"level":"INFO","msg":"sender starting"}
{"level":"INFO","msg":"successfully pushed logs to Loki","count":100}
```

### 5. View in Grafana

```
http://<node-ip>:30000
Login: admin / admin
```

```
Configuration → Data Sources → Loki
URL: http://loki-service:3100
```

```
Explore → Query:
{namespace="default", pod=~"busybox.*"}
```

---

##  Configuration

| Env Var | Default | Description |
|---|---|---|
| `LOG_FILE_PATH` | `/var/log/pods/.../0.log` | Path to pod log file |
| `STATE_FILE_PATH` | `/tmp/gopher-tailer.state` | Offset persistence file |
| `LOKI_URL` | `http://loki-service:3100/...` | Loki push endpoint |


## Results

Pipeline successfully running on Kind cluster:

-  Real-time log tailing every 500ms
-  Zero log loss across restarts (offset persistence)
-  Automatic file rotation handling
-  Batch shipping to Loki (100 entries/batch)
-  Labels: namespace, pod, container, stream
-  Visible in Grafana with LogQL queries
