# Gopher-Log-Transporter 
A custom log shipping agent built in Go that tails Kubernetes pod log files in real-time, parses them, and ships them to Grafana Loki.
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


### Pipeline
Reader ──chan RawLine(1000)──► Parser ──chan LogEntry(1000)──► Sender ──HTTP──► Loki
3 goroutines running concurrently, connected by buffered channels.

## Tech Stack

| Layer | Technology |
| Language | Go 1.23 |
| Containerization | Docker (multi-stage build) |
| Orchestration | Kubernetes (Kind cluster) |
| Log Aggregation | Grafana Loki |
| Visualization | Grafana |
| OS | Ubuntu Server 22.04 (VMware) |

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

# Build Docker image
docker build -t gopher-log-transporter:latest .
### 2. Create Kind Cluster
# Creates 2-node cluster with /var/log/pods mounted
kind create cluster --config k8s/kind-config.yaml

# Load image into cluster
kind load docker-image gopher-log-transporter:latest --name gopher-cluster
### 3. Deploy
```bash
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/loki-deployment.yaml
kubectl apply -f k8s/grafana-deployment.yaml
kubectl apply -f k8s/busybox-deployment.yaml

# Update LOG_FILE_PATH with actual pod path
POD_LOG_PATH=$(kubectl exec -it $(kubectl get pod -l app=busybox-app -o jsonpath='{.items[0].metadata.name}') \
  -- sh -c "ls /var/log/pods/ | grep busybox")

kubectl patch configmap gopher-config \
  --type merge \
  -p "{\"data\":{\"LOG_FILE_PATH\":\"/var/log/pods/${POD_LOG_PATH}/busybox/0.log\"}}"

kubectl apply -f k8s/gopher-deployment.yaml
### 4. Verify

# Check all pods running
kubectl get pods

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

http://<node-ip>:30000
Login: admin / admin

Configuration → Data Sources → Loki
URL: http://loki-service:3100

Explore → Query:
{namespace="default", pod=~"busybox.*"}
## Configuration

| Env Var | Default | Description |
|---|---|---|
| `LOG_FILE_PATH` | `/var/log/pods/.../0.log` | Path to pod log file |
| `STATE_FILE_PATH` | `/tmp/gopher-tailer.state` | Offset persistence file |
| `LOKI_URL` | `http://loki-service:3100/...` | Loki push endpoint |
## Results

Pipeline successfully running on Kind cluster:
- Real-time log tailing every 500ms
- Zero log loss across restarts (offset persistence)
- Automatic file rotation handling
- Batch shipping to Loki (100 entries/batch)
- Labels: namespace, pod, container, stream
- Visible in Grafana with LogQL queries
