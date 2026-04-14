# Cortex Load Tester

A distributed load testing tool written in Go that supports HTTP/HTTPS and SMTP protocols with various load patterns.

## Features

- **Distributed Architecture**: Master-worker architecture for scalable load testing
- **Multiple Protocols**: HTTP/HTTPS and SMTP support
- **Load Patterns**: Constant, ramp-up, spike, and step load patterns
- **Real-time Dashboard**: Web-based dashboard for monitoring tests
- **WebSocket Communication**: Real-time metrics aggregation from workers
- **Secure Authentication**: Token-based worker authentication

## Installation

### Prerequisites

- Go 1.25.8 or higher
- Git

### Install from GitHub

#### Option 1: Using `go install` (Recommended)

```bash
# Install the master node
go install github.com/pankaj-kumar34/cortex/cmd/cortex@latest

# Install the worker node
go install github.com/pankaj-kumar34/cortex/cmd/cortex-worker@latest
```

The binaries will be installed to `$GOPATH/bin` (typically `~/go/bin`). Make sure this directory is in your `PATH`.

## Quick Start

### 1. Start the Master Node

```bash
cortex
```

On first run, this will:

- Create a configuration file at `~/.cortex/config.yaml`
- Generate a secure authentication token
- Start the master server on port 8080
- Start the dashboard on port 3001

**Important**: Save the authentication token displayed in the output - you'll need it for worker nodes.

Example output:

```
INFO Starting Cortex Load Tester - Master Node
INFO Created new configuration file path=~/.cortex/config.yaml master_port=8080 dashboard_port=3001
INFO Worker authentication token (save this for worker nodes) token=abc123...
INFO Server ready - Dashboard: http://localhost:3001
```

### 2. Access the Dashboard

Open your browser and navigate to:

```
http://localhost:3001
```

### 3. Start Worker Nodes

On the same or different machines:

```bash
cortex-worker -master localhost:8080 -token YOUR_TOKEN_HERE -id worker-1
```

Or using environment variables:

```bash
export MASTER_ADDR=localhost:8080
export TOKEN=YOUR_TOKEN_HERE
export WORKER_ID=worker-1
cortex-worker
```

### 4. Configure and Run Tests

Use the web dashboard to:

1. Create a new test configuration
2. Set the target URL/SMTP server
3. Configure load pattern and duration
4. Start the test
5. Monitor real-time metrics

## Configuration

### Master Configuration

The master node configuration is stored at `~/.cortex/config.yaml`:

```yaml
master:
  port: 8080
  dashboard_port: 3001
  token: "your-secure-token"
active_test_id: ""
tests: {}
```

### Command-line Options

#### Master Node (`cortex`)

```bash
cortex [options]

Options:
  -config string
        Path to configuration file (default "~/.cortex/config.yaml")
  -master-port int
        Override master port when bootstrapping (default 8080)
  -dashboard-port int
        Override dashboard port (default 3001)
  -log-level string
        Log level: trace, debug, info, warn, error, fatal, panic (default "info")
```

#### Worker Node (`cortex-worker`)

```bash
cortex-worker [options]

Options:
  -master string
        Master server address (e.g., localhost:8080)
  -token string
        Shared authentication token
  -id string
        Worker ID (must be unique)
  -log-level string
        Log level: trace, debug, info, warn, error, fatal, panic (default "info")
```

### Test Configuration Example

HTTP Load Test:

```yaml
tests:
  my-http-test:
    protocol: http
    duration: 5m
    http:
      target_url: https://example.com/api
      method: POST
      headers:
        Content-Type: application/json
      body: '{"test": "data"}'
      timeout: 30s
      follow_redirects: true
      load_pattern: constant
      pattern_config:
        requests_per_second: 100
```

SMTP Load Test:

```yaml
tests:
  my-smtp-test:
    protocol: smtp
    duration: 2m
    smtp:
      host: smtp.example.com
      port: 587
      from: test@example.com
      to:
        - recipient@example.com
      subject: Load Test
      body: Test email body
      use_tls: true
      username: user@example.com
      password: password
      load_pattern: ramp-up
      pattern_config:
        start_rps: 10
        target_rps: 100
        ramp_up_duration: 1m
```

## Load Patterns

### Constant

Maintains a steady request rate:

```yaml
load_pattern: constant
pattern_config:
  requests_per_second: 100
```

### Ramp-Up

Gradually increases load:

```yaml
load_pattern: ramp-up
pattern_config:
  start_rps: 10
  target_rps: 100
  ramp_up_duration: 2m
  ramp_down_duration: 1m
```

### Spike

Sudden load increases:

```yaml
load_pattern: spike
pattern_config:
  base_rps: 50
  spike_rps: 500
  spike_duration: 30s
```

### Step

Incremental load increases:

```yaml
load_pattern: step
pattern_config:
  start_rps: 10
  step_increment: 20
  step_duration: 1m
  step_count: 5
```

## Architecture

```
┌─────────────┐
│  Dashboard  │ (Port 3001)
│   (HTTP)    │
└──────┬──────┘
       │
┌──────▼──────┐
│   Master    │ (Port 8080)
│   Server    │
└──────┬──────┘
       │ WebSocket
       │
   ┌───┴───┬───────┬───────┐
   │       │       │       │
┌──▼──┐ ┌──▼──┐ ┌──▼──┐ ┌──▼──┐
│ W-1 │ │ W-2 │ │ W-3 │ │ W-N │
└─────┘ └─────┘ └─────┘ └─────┘
Workers (Distributed)
```

## Development

### Building from Source

```bash
# Clone the repository
git clone https://github.com/pankaj-kumar34/cortex.git
cd cortex

# Install dependencies
go mod download

# Run tests
go test ./...

# Build
go build -o cortex ./cmd/cortex
go build -o cortex-worker ./cmd/cortex-worker
```

### Project Structure

```
cortex/
├── cmd/
│   ├── cortex/          # Master node entry point
│   └── cortex-worker/   # Worker node entry point
├── internal/
│   ├── auth/            # Authentication
│   ├── config/          # Configuration management
│   ├── logger/          # Logging utilities
│   ├── master/          # Master server logic
│   ├── models/          # Data models
│   └── worker/          # Worker client logic
├── go.mod
└── go.sum
```

## Troubleshooting

### Workers Can't Connect

1. Verify the master is running: `curl http://localhost:8080/health`
2. Check the token matches between master and worker
3. Ensure firewall allows connections on port 8080
4. For remote workers, use the master's IP instead of `localhost`

### Configuration Issues

- Configuration file location: `~/.cortex/config.yaml`
- Reset configuration: Delete the file and restart the master
- View logs with: `cortex -log-level debug`

### Port Already in Use

Change ports when starting:

```bash
cortex -master-port 8081 -dashboard-port 3002
```

## License

[Add your license here]

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## Author

Pankaj Kumar ([@pankaj-kumar34](https://github.com/pankaj-kumar34))
