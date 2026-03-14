# cexec

**Run commands on multiple Linux servers at the same time — simply, from a single binary.**

`cexec` is a lightweight Go CLI tool that SSHes into multiple servers simultaneously and runs shell commands or multi-step playbooks across all of them concurrently. No agents. No Python. No complex YAML inventory servers. Just a single compiled binary you drop on your master machine and run.

---

## Table of Contents

- [What Problem Does This Solve?](#what-problem-does-this-solve)
- [What is cexec?](#what-is-cexec)
- [Not Just for HPC — Use It for Anything](#not-just-for-hpc--use-it-for-anything)
- [Key Features](#key-features)
- [How It Compares to Ansible](#how-it-compares-to-ansible)
- [Project Structure](#project-structure)
- [Requirements](#requirements)
- [Build Instructions](#build-instructions)
- [Configuration: cluster.env](#configuration-clusterenv)
- [Setting Up Your Inventory (Node List)](#setting-up-your-inventory-node-list)
- [SSH Authentication](#ssh-authentication)
- [Usage Examples](#usage-examples)
- [Playbook Format](#playbook-format)
- [Template Variables](#template-variables)
- [Hash-Based Caching (Step State)](#hash-based-caching-step-state)
- [Error Handling and Logging](#error-handling-and-logging)
- [All Flags Reference](#all-flags-reference)
- [Adding More Nodes Later](#adding-more-nodes-later)
- [Common Questions (FAQ)](#common-questions-faq)

---

## What Problem Does This Solve?

Imagine you have 10 Linux servers and you need to install the same software on all of them, or restart a service, or push a config file. You have a few options:

1. **Do it manually, one-by-one** — SSH into each server, run the command, repeat. Painful, slow, and error-prone.
2. **Write a shell script with loops** — Better, but you have to handle SSH, errors, timeouts, retries, and ordering yourself. Hard to get right.
3. **Use Ansible** — Powerful, but requires Python on every server, an inventory file in a specific format, learning a large ecosystem, and a non-trivial setup.
4. **Use `cexec`** — Single binary, no dependencies on target servers, minimal config, runs everything in parallel automatically.

`cexec` was originally built to automate setting up a Beowulf HPC (High Performance Computing) cluster — installing MPI, NFS, Python, configuring SSH keys across all nodes, syncing `/etc/hosts`, etc. But the tool is general-purpose: anything you would run in a terminal on one server, you can run on 50 servers at once with `cexec`.

---

## What is cexec?

`cexec` lets you:

- **Run a one-off command** on all your servers in parallel: `./cexec -- uptime`
- **Run a multi-step playbook** (a YAML file) across all your servers, with steps ordered by dependencies
- **Target specific groups** (e.g. only worker nodes, only the master)
- **Skip already-completed steps automatically** using a SHA256-based cache
- **Retry failed commands** with exponential backoff
- **Dry-run** to preview what would happen before actually running anything

Everything happens over standard SSH — the target servers need nothing installed except an SSH server (which every Linux server already has).

---

## Not Just for HPC — Use It for Anything

Even though `cexec` was built for HPC cluster setup, it works for any situation where you need to run commands on multiple Linux servers. Here are some examples:

| Use Case | Example Command |
|---|---|
| Check disk space across all servers | `./cexec -- df -h` |
| Restart a web service everywhere | `./cexec -- systemctl restart nginx` |
| Pull latest Docker image on all nodes | `./cexec -- docker pull myapp:latest` |
| Apply security patches | `./cexec --sudo -- apt-get upgrade -y` |
| Collect uptime / load stats | `./cexec -- uptime` |
| Reboot all worker nodes | `./cexec --nodes compute -- reboot` |
| Update a cron job on all machines | `./cexec --playbook update-crons.yaml` |
| Set up a new dev/test cluster from scratch | `./cexec --playbook bootstrap.yaml` |
| Run database migrations across shards | `./cexec --nodes db-shards -- ./migrate.sh` |
| Check running processes | `./cexec -- ps aux \| grep myapp` |

If you can SSH into it and run a shell command, `cexec` can automate it across all your nodes.

---

## Key Features

### Concurrent Execution
Commands run on all nodes **at the same time**, not one after another. If you have 20 nodes and a command takes 30 seconds, the total wall-clock time is still ~30 seconds — not 10 minutes.

### Playbook Mode
Write a YAML file listing steps (commands) to run. Steps can have `depends_on` to enforce ordering — a step will not start until all of its dependencies have finished successfully across all relevant nodes.

### Role-Based Steps
Tag steps with `roles: [control]` to run only on the master node, or `roles: [compute]` to run only on worker nodes. Mix and match in the same playbook.

### Hash-Based Caching
Completed steps are tracked by a SHA256 hash of `(nodeName, stepName, command)`. Re-running the playbook skips steps that already succeeded. Change the command and the hash changes, so the step re-runs automatically. No stale skips.

### Template Variables
Use `{{master_pubkey}}` and `{{hosts_sync_cmds}}` in your playbook commands. They are expanded at runtime with the actual SSH public key and hosts-sync shell commands.

### Auto-Inventory from /etc/hosts
Point `cexec` at your `/etc/hosts` file and it automatically discovers all nodes matching the naming pattern (`master`, `node1`, `node2`, ...). No manual inventory YAML required.

### Single Config File
All defaults live in `cluster.env`. This file is gitignored, so your password never ends up in version control.

### Retry with Backoff
Configure `--retries` and `--backoff` for flaky commands. Supports both exponential backoff (default) and fixed backoff (`--backoff-fixed`).

### JSON Logs
Every run writes structured JSON logs to the `logs/` directory. You get per-node output, timestamps, success/failure status, and a summary.

### Dry-Run Mode
`--dry-run` shows you exactly which commands would run on which nodes, without executing anything.

### Single-Command Mode
No playbook needed — just append `-- <your command>` and cexec runs it on all nodes immediately.

---

## How It Compares to Ansible

| Feature | cexec | Ansible |
|---|---|---|
| Agent required on target? | No | No |
| Python required on target? | No | Yes |
| Inventory format | `/etc/hosts` or simple YAML | INI or YAML |
| Config language | Minimal YAML | Full Ansible DSL |
| Installation | Single binary | pip install (Python ecosystem) |
| Learning curve | Low | Medium-High |
| Idempotency | Hash-based cache | Module-level idempotency |
| Best for | Fast, simple automation | Large-scale enterprise automation |
| Built-in modules (file, template, etc.) | No | Yes (hundreds) |
| Retry / backoff | Yes | Yes |
| Dry run | Yes (`--dry-run`) | Yes (`--check`) |

`cexec` is not trying to replace Ansible for complex enterprise environments. It is trying to be the simplest possible tool for the 80% of cases where you just need to run some commands on a bunch of servers.

---

## Project Structure

```
cexec/
├── cmd/
│   └── cexec/
│       └── main.go              # Entry point: flag parsing, playbook runner, main loop
├── internal/
│   ├── ssh/
│   │   └── client.go            # SSH connection management + command execution
│   ├── executor/
│   │   └── executor.go          # Concurrent runner, retry logic, backoff
│   ├── playbook/
│   │   └── playbook.go          # YAML playbook loader and validator
│   ├── inventory/
│   │   └── inventory.go         # Node struct, YAML loader, node selector
│   ├── hosts/
│   │   └── hosts.go             # /etc/hosts parser → auto inventory builder
│   ├── state/
│   │   └── state.go             # Hash-based step completion cache (SHA256)
│   ├── logging/
│   │   └── logger.go            # JSON run logs, summary counters, output formatting
│   └── errors/
│       └── classify.go          # Error classifier (auth failure, timeout, DNS, etc.)
├── hpc-setup.yaml               # Example playbook: full Beowulf HPC cluster setup
├── inventory.yaml               # Example manual inventory file (optional)
├── cluster.env.example          # Template config file — copy to cluster.env and fill in
├── go.mod
└── go.sum
```

Each internal package has a single responsibility:

- **`ssh/client.go`** — Opens SSH sessions, handles key and password auth, streams stdout/stderr
- **`executor/executor.go`** — Launches goroutines for each node, manages the concurrency limit, handles retries
- **`playbook/playbook.go`** — Parses YAML, validates step names are unique, resolves `depends_on` ordering
- **`inventory/inventory.go`** — Represents a node (name, host, user, port, groups) and filters nodes by group/name
- **`hosts/hosts.go`** — Reads `/etc/hosts` and extracts entries whose hostname matches `master` or `nodeN`
- **`state/state.go`** — Reads/writes `.cexec_state.json`, computes SHA256 hashes, checks and records step completion
- **`logging/logger.go`** — Writes structured JSON log files per run, prints terminal output, tracks counts
- **`errors/classify.go`** — Inspects error messages and exit codes to assign a human-readable category

---

## Requirements

### To Build cexec

| Requirement | Version |
|---|---|
| Go | 1.21 or newer |
| OS | Linux, macOS, or Windows (for building) |
| Network | Internet access (first build only, to download Go modules) |

### On Target Servers (Nodes)

| Requirement | Notes |
|---|---|
| SSH server | `openssh-server` — already on most Linux distros |
| bash / sh | For running commands |
| Password or SSH key auth | See [SSH Authentication](#ssh-authentication) |

That is it. No Go runtime, no Python, no agents, nothing extra needs to be installed on your servers.

---

## Build Instructions

### Step 1 — Install Go 1.21+

First, check if Go is already installed:

```bash
go version
```

If not installed, download and install it:

```bash
# Download the Go installer for Linux amd64
wget https://go.dev/dl/go1.21.0.linux-amd64.tar.gz

# Extract it to /usr/local
sudo tar -C /usr/local -xzf go1.21.0.linux-amd64.tar.gz

# Add Go to your PATH
export PATH=$PATH:/usr/local/go/bin
```

Add the `export PATH` line to your `~/.bashrc` or `~/.zshrc` so it persists after you close the terminal:

```bash
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
```

Confirm Go is working:

```bash
go version
# Expected output: go version go1.21.0 linux/amd64
```

### Step 2 — Get the Source Code

```bash
git clone <repo-url>
cd cexec
```

### Step 3 — Download Dependencies

```bash
go mod download
```

This downloads the two external dependencies:
- `golang.org/x/crypto/ssh` — SSH connections
- `gopkg.in/yaml.v3` — YAML parsing

After this first download, everything works offline. The downloaded modules are cached on your system.

### Step 4 — Build

**Build for your current machine (the machine you are on right now):**

```bash
go build -o cexec ./cmd/cexec/
```

This produces a single executable file called `cexec` in the current directory. No installer, no dependencies to ship — just this one file.

**Cross-compile for Linux amd64 (most common server architecture):**

```bash
GOOS=linux GOARCH=amd64 go build -o cexec-linux-amd64 ./cmd/cexec/
```

Use this if you are building on a Mac or Windows machine but deploying to Linux servers.

**Cross-compile for Linux arm64 (Raspberry Pi, AWS Graviton, Apple Silicon servers):**

```bash
GOOS=linux GOARCH=arm64 go build -o cexec-linux-arm64 ./cmd/cexec/
```

**Build with a version string embedded in the binary (optional):**

```bash
go build -ldflags="-X main.version=1.0.0" -o cexec ./cmd/cexec/
```

### Step 5 — Run Tests

```bash
go test ./...
```

This runs all tests in all packages. A passing output looks like:

```
ok      github.com/youruser/cexec/internal/inventory   0.003s
ok      github.com/youruser/cexec/internal/state       0.001s
ok      github.com/youruser/cexec/internal/playbook    0.002s
```

### Step 6 — Make It Accessible

The binary is already executable after `go build`. Optionally move it somewhere in your system PATH so you can run it from any directory:

```bash
sudo mv cexec /usr/local/bin/cexec
```

Or keep it in the project directory and always run it as `./cexec`.

### Quick One-Liner (All Steps Combined)

```bash
git clone <repo-url> && cd cexec && go mod download && go build -o cexec ./cmd/cexec/
```

---

## Configuration: cluster.env

`cluster.env` is the main config file. It stores your defaults so you do not have to type the same flags every time. **This file is gitignored — it is safe to put your SSH password here.**

### Setting Up cluster.env

Copy the provided example file and fill it in:

```bash
cp cluster.env.example cluster.env
nano cluster.env
```

### All Available Options

```bash
# cluster.env

# SSH password for all nodes (used as fallback if SSH key auth fails)
CLUSTER_PASSWORD=yourpassword

# Path to /etc/hosts (for auto-inventory mode — discovers nodes automatically)
CLUSTER_HOSTS_FILE=/etc/hosts

# SSH username to use when connecting to nodes
CLUSTER_USER=hpc

# Default playbook to run (used when --playbook flag is not passed)
CLUSTER_PLAYBOOK=hpc-setup.yaml

# Directory to write JSON log files
CLUSTER_LOG_DIR=logs

# File to store completed step hashes (the cache)
CLUSTER_STATE_FILE=.cexec_state.json
```

### How Config Values Work

- Any flag you pass on the command line **overrides** the value in `cluster.env`.
- If `CLUSTER_HOSTS_FILE` is set, cexec will auto-discover nodes from that file without needing `inventory.yaml`.
- `CLUSTER_PASSWORD` is only used as a fallback — SSH key auth is always tried first.
- If a value is not set in `cluster.env` and not passed as a flag, the built-in default is used (shown in the [Flags Reference](#all-flags-reference) table).

---

## Setting Up Your Inventory (Node List)

An "inventory" is just the list of servers cexec should connect to. There are two ways to provide it.

### Option A — Auto-Inventory from /etc/hosts (Recommended)

Set `CLUSTER_HOSTS_FILE=/etc/hosts` in your `cluster.env`. cexec will read that file and automatically pick up any entry whose hostname matches the pattern `master`, `node1`, `node2`, ..., `node20`, etc.

Example `/etc/hosts` on your master machine:

```
127.0.0.1       localhost
192.168.1.10    master
192.168.1.11    node1
192.168.1.12    node2
192.168.1.13    node3
192.168.1.14    node4
```

cexec discovers: `master` (assigned group: `control`), `node1` through `node4` (assigned group: `compute`). No other config needed.

**Why this is recommended:** You probably already keep your `/etc/hosts` up-to-date with your cluster layout. Using it as the source of truth means you do not have a separate inventory file to maintain. Adding a new node is as simple as adding a line to `/etc/hosts`.

You can also pass it directly as a flag without setting it in `cluster.env`:

```bash
./cexec --auto-hosts /etc/hosts --hosts-user hpc -- hostname
```

### Option B — Manual inventory.yaml

Create an `inventory.yaml` file:

```yaml
nodes:
  - name: master
    host: 192.168.1.10
    user: hpc
    port: 22
    groups: [control]

  - name: node1
    host: 192.168.1.11
    user: hpc
    port: 22
    groups: [compute]

  - name: node2
    host: 192.168.1.12
    user: hpc
    port: 22
    groups: [compute]

  - name: node3
    host: 192.168.1.13
    user: hpc
    port: 22
    groups: [compute]
```

Then run with:

```bash
./cexec --inventory inventory.yaml --playbook hpc-setup.yaml
```

### Node Fields Explained

| Field | Required | Description |
|---|---|---|
| `name` | Yes | Friendly name used in logs, `--nodes`, `--exclude`, and `depends_on` |
| `host` | Yes | IP address or hostname to SSH into |
| `user` | Yes | SSH username |
| `port` | No | SSH port (default: 22) |
| `groups` | No | List of group names (e.g. `[control]`, `[compute]`, `[db]`) |

---

## SSH Authentication

cexec handles SSH authentication automatically. Here is the order it tries:

1. **SSH key — `~/.ssh/id_ed25519`** (preferred — modern, fast, secure)
2. **SSH key — `~/.ssh/id_rsa`** (fallback if ed25519 key is not found)
3. **Password from `CLUSTER_PASSWORD`** in `cluster.env`

### First-Time Setup (Before SSH Keys Are Distributed)

When you first set up a cluster, the nodes only have password authentication enabled. cexec handles this gracefully: it connects with password auth, runs your playbook step that pushes the master's SSH public key, and from that point forward all connections use key-based auth automatically.

### Generating an SSH Key (If You Do Not Have One)

```bash
ssh-keygen -t ed25519 -C "cluster-master"
# Press Enter to accept the default path (~/.ssh/id_ed25519)
# Set a passphrase or leave it empty for automated non-interactive use
```

### Typical First-Run Workflow

```bash
# Step 1: Make sure all nodes have password auth enabled (the default on most Linux distros)

# Step 2: Set CLUSTER_PASSWORD in cluster.env with the SSH password for your nodes

# Step 3: Run the playbook — cexec uses password auth for the first connection
./cexec --playbook hpc-setup.yaml

# The playbook's "push master ssh key" step will add ~/.ssh/id_ed25519.pub
# to ~/.ssh/authorized_keys on every node.

# Step 4: All future runs use key auth automatically — no password needed
./cexec --playbook hpc-setup.yaml
```

---

## Usage Examples

### Run a Single Command on All Nodes

```bash
# Check hostname on every node
./cexec -- hostname

# Check disk space
./cexec -- df -h

# Check memory usage
./cexec -- free -m

# Check uptime and load
./cexec -- uptime

# See who is logged in
./cexec -- who
```

### Run a Playbook

```bash
# Run a specific playbook
./cexec --playbook hpc-setup.yaml

# Dry run — see what would happen without actually running anything
./cexec --playbook hpc-setup.yaml --dry-run

# Force re-run all steps, ignoring the cache
./cexec --playbook hpc-setup.yaml --force
```

### Target Specific Groups or Nodes

```bash
# Only run on compute nodes (nodes in the "compute" group)
./cexec --nodes compute -- apt-get update

# Only run on the master (nodes in the "control" group)
./cexec --nodes control -- systemctl status nfs-server

# Run on specific named nodes
./cexec --nodes master,node2 -- systemctl restart nginx

# Run on all nodes EXCEPT node3
./cexec --exclude node3 -- reboot

# Target a group in a playbook
./cexec --nodes compute --playbook worker-setup.yaml
```

### Run with Elevated Privileges

```bash
# Run command with sudo on all nodes
./cexec --sudo -- apt-get upgrade -y

# Install a package everywhere
./cexec --sudo -- apt-get install -y htop

# Restart a system service on all workers
./cexec --nodes compute --sudo -- systemctl restart slurmd
```

### Control Concurrency and Timeouts

```bash
# Limit to 5 parallel SSH connections at a time
./cexec --concurrency 5 -- apt-get update

# Set a 10-minute timeout per command (useful for long installs)
./cexec --timeout 10m -- ./long-running-script.sh

# Retry up to 3 times, waiting 5 seconds between attempts
./cexec --retries 3 --backoff 5s -- ping -c1 8.8.8.8

# Use fixed backoff (always 2s) instead of exponential (2s, 4s, 8s, ...)
./cexec --retries 5 --backoff 2s --backoff-fixed -- curl http://myservice/health
```

### Quiet Mode and Logging

```bash
# Suppress per-node output, show only the summary at the end
./cexec --quiet -- uptime

# Write logs to a custom directory
./cexec --log-dir /var/log/cexec -- hostname
```

### Override Config File Location

```bash
# Use a different env config file (e.g. for a different cluster)
./cexec --env-file /etc/cexec/prod.env --playbook setup.yaml

# Use auto-hosts from a specific file with a specific SSH user
./cexec --auto-hosts /etc/hosts --hosts-user ubuntu -- hostname
```

### Use a Custom State (Cache) File

```bash
# Use a different cache file — useful if you manage multiple clusters
./cexec --state-file .cexec_state_prod.json --playbook prod-setup.yaml

# Start fresh with a clean cache file
./cexec --state-file .cexec_state_fresh.json --playbook hpc-setup.yaml
```

---

## Playbook Format

A playbook is a YAML file that lists a sequence of steps (commands) to run across your nodes. You write it once; cexec handles running each step on the right nodes in the right order.

### Basic Structure

```yaml
steps:
  - name: "update package list"
    command: "apt-get update -y"
    sudo: true

  - name: "install htop"
    command: "apt-get install -y htop"
    sudo: true
    depends_on: ["update package list"]

  - name: "check htop version"
    command: "htop --version"
    depends_on: ["install htop"]
```

### All Step Fields

| Field | Required | Type | Description |
|---|---|---|---|
| `name` | Yes | string | Unique name for this step. Used in logs, caching, and `depends_on`. |
| `command` | Yes | string | The shell command to run. Supports template variables. |
| `sudo` | No | bool | If `true`, run with `sudo`. Default: `false`. |
| `roles` | No | list | Only run on nodes in these groups. Omit to run on all nodes. |
| `depends_on` | No | list | List of step names that must complete before this step starts. |

### Role-Based Steps

Use `roles` to restrict a step to specific node groups:

```yaml
steps:
  # This step only runs on the master (nodes in the "control" group)
  - name: "configure NFS server"
    command: "apt-get install -y nfs-kernel-server"
    sudo: true
    roles: [control]

  # This step only runs on worker nodes (nodes in the "compute" group)
  - name: "configure NFS client"
    command: "apt-get install -y nfs-common"
    sudo: true
    roles: [compute]

  # This step runs on ALL nodes (no roles field = no filter)
  - name: "install common tools"
    command: "apt-get install -y curl wget git"
    sudo: true
```

### Dependency Ordering with depends_on

`depends_on` tells cexec "do not start this step until these other steps have finished." This is how you enforce a safe order of operations:

```yaml
steps:
  - name: "install MPI"
    command: "apt-get install -y mpich"
    sudo: true

  - name: "verify MPI installation"
    command: "mpirun --version"
    depends_on: ["install MPI"]     # Will not start until "install MPI" is done

  - name: "run MPI hello world"
    command: "mpirun -np 4 hostname"
    roles: [control]
    depends_on: ["verify MPI installation"]   # Chain of dependencies
```

### Full Real-World Example Playbook

```yaml
steps:
  # Run on ALL nodes first
  - name: "update packages"
    command: "apt-get update -y"
    sudo: true

  - name: "install base tools"
    command: "apt-get install -y curl wget git htop nmap"
    sudo: true
    depends_on: ["update packages"]

  # Run only on master — set up the NFS server
  - name: "install NFS server"
    command: "apt-get install -y nfs-kernel-server"
    sudo: true
    roles: [control]
    depends_on: ["update packages"]

  # Run only on workers — mount NFS
  - name: "install NFS client"
    command: "apt-get install -y nfs-common"
    sudo: true
    roles: [compute]
    depends_on: ["update packages"]

  # Push master's SSH key to ALL nodes (uses template variable)
  - name: "authorize master ssh key"
    command: "mkdir -p ~/.ssh && echo '{{master_pubkey}}' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
    depends_on: ["install base tools"]

  # Sync /etc/hosts to all nodes via the master (uses template variable)
  - name: "sync hosts file"
    command: "{{hosts_sync_cmds}}"
    roles: [control]
    depends_on: ["authorize master ssh key"]
```

---

## Template Variables

You can use special placeholder tokens inside `command` fields in your playbook. cexec expands them to their real values at runtime, just before sending the command to each node.

### `{{master_pubkey}}`

**Expands to:** The full text content of your master machine's SSH public key file.

cexec looks for the key in this order:
1. `~/.ssh/id_ed25519.pub`
2. `~/.ssh/id_rsa.pub`

**Example use:**

```yaml
- name: "authorize master ssh key"
  command: "mkdir -p ~/.ssh && echo '{{master_pubkey}}' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
```

After this step runs on all nodes, the master can SSH into every node without a password — key-based auth works cluster-wide.

**Why the hash-based cache handles key rotation:** If you ever regenerate your SSH key, `{{master_pubkey}}` produces a different string, which changes the SHA256 hash of this step, which means cexec will automatically re-run it on all nodes. You do not need to manually clear the cache.

### `{{hosts_sync_cmds}}`

**Expands to:** A shell command (or series of piped commands) that pushes the cluster's `/etc/hosts` entries from the master to every compute node.

**Example use:**

```yaml
- name: "sync /etc/hosts to all nodes"
  command: "{{hosts_sync_cmds}}"
  roles: [control]
```

This ensures every node in the cluster can resolve every other node by hostname — which is required for MPI, NFS mounts, and inter-node SSH.

---

## Hash-Based Caching (Step State)

One of cexec's most useful features is that it remembers which steps have already completed successfully. This means you can safely re-run your playbook at any time without worry — completed steps are skipped instantly, and only pending or changed steps actually run.

### How It Works

Every time a step succeeds on a node, cexec computes:

```
SHA256(nodeName + "|" + stepName + "|" + command)
```

It stores the first 8 bytes of that hash (as a hex string) in `.cexec_state.json`. The next time you run the same playbook, cexec checks each step against the cache before running it. If the hash matches, the step is skipped with a `SKIP (cached)` message.

### Example .cexec_state.json

```json
{
  "completed": {
    "a1b2c3d4e5f6a7b8": true,
    "b2c3d4e5f6a7b8c9": true,
    "c3d4e5f6a7b8c9d0": true
  }
}
```

### What Triggers a Re-Run?

A step re-runs automatically (its hash changes, so the cache misses) if:

- The **command text changes** — even a single character
- The **step name changes**
- The **node name changes** (e.g. you rename a node in inventory)
- A **template variable changes** — if `{{master_pubkey}}` points to a new key after you rotate SSH keys, every step using it re-runs automatically

### Practical Benefits

```bash
# First run: all 20 steps run on all 8 nodes (160 total executions)
./cexec --playbook hpc-setup.yaml

# Add node5 to /etc/hosts, then re-run:
./cexec --playbook hpc-setup.yaml
# node1-node4 skip all 20 steps (cached)
# node5 runs all 20 steps fresh (not in cache yet)

# Edit the command in step 15, then re-run:
./cexec --playbook hpc-setup.yaml
# All nodes skip steps 1-14 (cached, command unchanged)
# All nodes re-run steps 15-20 (hash changed, must re-run)
```

### Force Re-Run Everything (Ignore Cache)

```bash
./cexec --playbook hpc-setup.yaml --force
```

### Use a Different Cache File

```bash
# Keep separate caches for separate clusters
./cexec --state-file .cexec_state_prod.json --playbook hpc-setup.yaml
./cexec --state-file .cexec_state_dev.json  --playbook hpc-setup.yaml
```

### Clear the Cache Manually

```bash
rm .cexec_state.json
```

---

## Error Handling and Logging

### Error Categories

When a step fails, cexec classifies the error into a human-readable category so you know immediately what went wrong:

| Category | What It Means |
|---|---|
| `ssh_auth_failed` | SSH key auth and password auth were both rejected by the server |
| `host_unreachable` | Could not open a TCP connection to the server at all |
| `connection_timeout` | SSH connection timed out (server too slow or unreachable) |
| `sudo_auth_failed` | The `sudo` password was wrong or sudo is not configured |
| `non_zero_exit` | The command ran but exited with a non-zero status code (indicates failure) |
| `dns_resolution_failed` | The hostname could not be resolved to an IP address |
| `command_not_found` | The command does not exist on the target node |

### Terminal Output

For each step and node, you see real-time output:

```
[node1] STEP: install htop
[node1] OK (0.84s)

[node2] STEP: install htop
[node2] OK (0.91s)

[node3] STEP: install htop
[node3] FAILED: non_zero_exit — E: Package 'htop' has no installation candidate
```

Failed steps show the last line of stderr and the error category. They do not abort the other nodes — cexec keeps going and reports everything at the end.

### Run Summary

At the end of every run, cexec prints a summary:

```
=============================
Run Summary
=============================
Total nodes:    8
Steps run:      160
Steps skipped:  0   (cached)
Succeeded:      158
Failed:         2
Duration:       45.3s
=============================
```

### JSON Log Files

Every run writes a structured log file to the `logs/` directory (configurable with `--log-dir`). Filenames include a timestamp and a random run ID so you have a complete history:

```
logs/run_20250312T143022_a1b2c3d4e5f6.json
```

Example log entry for a single node's step:

```json
{
  "run_id": "20250312T143022_a1b2c3d4e5f6",
  "timestamp": "2025-03-12T14:30:22Z",
  "node": "node3",
  "step": "install htop",
  "command": "apt-get install -y htop",
  "status": "failed",
  "error_category": "non_zero_exit",
  "stderr": "E: Package 'htop' has no installation candidate",
  "duration_ms": 1240
}
```

---

## All Flags Reference

### Playbook Flags

| Flag | Default | Description |
|---|---|---|
| `--playbook` | (from `CLUSTER_PLAYBOOK`) | Path to the YAML playbook file |
| `--dry-run` | `false` | Show what would run without executing anything |
| `--force` | `false` | Ignore the step cache and re-run all steps |
| `--state-file` | `.cexec_state.json` | Path to the step completion cache file |

### Node Selection Flags

| Flag | Default | Description |
|---|---|---|
| `--inventory` | `inventory.yaml` | Path to the manual inventory YAML file |
| `--auto-hosts` | — | Path to a hosts file to auto-build inventory from |
| `--hosts-user` | `hpc` | SSH user to assign when using `--auto-hosts` |
| `--nodes` | `all` | Target: `all`, a group name, or comma-separated node names (e.g. `master,node2`) |
| `--exclude` | — | Comma-separated node names to exclude from the run |

### SSH and Execution Flags

| Flag | Default | Description |
|---|---|---|
| `--sudo` | `false` | Run the command with `sudo` |
| `--timeout` | `5m` | Per-command timeout (e.g. `30s`, `10m`, `1h`) |
| `--concurrency` | `0` (unlimited) | Max number of parallel SSH connections at one time |
| `--retries` | `0` | How many times to retry a failed command before giving up |
| `--backoff` | `2s` | Base wait duration between retries (e.g. `2s`, `10s`) |
| `--backoff-fixed` | `false` | Use fixed backoff (always `--backoff` duration) instead of exponential (2s, 4s, 8s, ...) |

### Config and Output Flags

| Flag | Default | Description |
|---|---|---|
| `--env-file` | `cluster.env` | Path to the env config file |
| `--log-dir` | `logs` | Directory to write JSON log files into |
| `--quiet` | `false` | Suppress per-node output; print only the final summary |

---

## Adding More Nodes Later

Adding a new server to your cluster requires zero changes to cexec config. Just:

**Step 1 — Add the new node to `/etc/hosts` on the master:**

```
192.168.1.15   node5
192.168.1.16   node6
```

**Step 2 — Re-run the playbook:**

```bash
./cexec --playbook hpc-setup.yaml
```

**What happens automatically:**

- `node1` through `node4` — every step is skipped instantly (already in the cache)
- `node5` and `node6` — every step runs fresh from the beginning (new nodes, not in cache)

You do not need to touch any config files, inventory files, or the cache file. cexec handles it.

---

## Common Questions (FAQ)

### Do the target servers need anything installed?

No. The only requirement on target servers is a running SSH server — `openssh-server` — which is installed by default on virtually every Linux distribution. No Go runtime, no Python, no agents, nothing.

### How do I run cexec on Windows?

You can build a Windows executable from the same source:

```powershell
# In PowerShell
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -o cexec.exe ./cmd/cexec/
```

Or cross-compile from Linux/macOS:

```bash
GOOS=windows GOARCH=amd64 go build -o cexec.exe ./cmd/cexec/
```

The SSH connections go to Linux servers regardless — Windows is just where the binary runs.

### What happens if a node is offline when I run cexec?

cexec attempts to connect, fails with a `host_unreachable` or `connection_timeout` error, logs the failure, and continues with the rest of the nodes. If `--retries` is set, it retries before giving up. The offline node's steps are not marked as complete in the cache, so they will be attempted again on the next run. Other nodes are not affected.

### Can I run cexec on a node that is itself one of the targets?

Yes. If the master node is in your inventory (e.g. named `master`), cexec SSHes into `localhost` just like any other node. This is intentional — it keeps execution consistent across all nodes, including the master.

### What if two steps have the same name?

The playbook validator rejects it immediately with an error before running anything. Step names must be unique because they are used as identifiers in the cache and in `depends_on` references.

### How do I clear the step cache?

**Option 1 — Delete the cache file:**

```bash
rm .cexec_state.json
```

**Option 2 — Use `--force` to ignore the cache for one run (does not delete the file):**

```bash
./cexec --playbook hpc-setup.yaml --force
```

**Option 3 — Use a fresh cache file for a clean run:**

```bash
./cexec --state-file .cexec_state_clean.json --playbook hpc-setup.yaml
```

### Can I run multiple playbooks?

Yes — run cexec multiple times with different `--playbook` arguments. They share the same cache file (unless you specify different `--state-file` values for each). Steps completed in one playbook run are still cached for subsequent runs.

### How do I debug a failed step?

1. **Check terminal output** — the last line of stderr and the error category are printed for every failed step.
2. **Check the JSON logs** in the `logs/` directory — full details including timestamps, duration, and complete stderr output.
3. **Use `--dry-run` first** to confirm the right command would run on the right nodes.
4. **SSH manually** into the failing node and run the command yourself to see the complete error interactively.

### What does --concurrency do exactly?

By default (`--concurrency 0`), cexec launches one goroutine (parallel worker) per node — all nodes start at exactly the same time. With 100 nodes, 100 SSH connections open simultaneously. `--concurrency 10` caps it so only 10 connections are open at any moment, processing nodes in a rolling batch. Use this if your network or SSH server struggles with many simultaneous connections.

### Is cluster.env really gitignored?

Yes. The `.gitignore` in this repo includes `cluster.env`. The file `cluster.env.example` (with placeholder values and no real secrets) is tracked by git as a template. Never commit `cluster.env` with real credentials to a public repository.

### Can I use cexec with nodes that have different SSH users or ports?

Yes — in `inventory.yaml`, each node entry has its own `user` and `port` fields. When using auto-inventory from `/etc/hosts`, all discovered nodes use the same user (from `CLUSTER_USER` or `--hosts-user`) and port 22. For mixed environments, use `inventory.yaml`.

### The playbook has 30 steps. Can I start from step 15?

Not with a direct flag, but hash-based caching handles this automatically. If steps 1 through 14 already ran successfully, they are in the cache and skip instantly. cexec picks up from wherever it left off. Alternatively, use `--force` combined with commenting out earlier steps in the YAML for a one-off partial run.

### Can I use a jump host / bastion server?

Not natively yet — cexec connects directly to each node's IP. The recommended approach is to run cexec from a machine that has direct network access to all nodes (typically the master node itself, inside the cluster network).

### What Go version do I need?

Go 1.21 or newer. Check with:

```bash
go version
```

If you are on an older version, download the latest from [go.dev/dl](https://go.dev/dl/).

### How do I contribute?

Open an issue or pull request on the repository. Useful contributions include new error categories in `internal/errors/classify.go`, additional template variables, new backoff strategies, improved terminal output, or additional test coverage.

---

## Tech Stack

| Component | Library |
|---|---|
| SSH connections | [`golang.org/x/crypto/ssh`](https://pkg.go.dev/golang.org/x/crypto/ssh) |
| YAML parsing | [`gopkg.in/yaml.v3`](https://pkg.go.dev/gopkg.in/yaml.v3) |
| Everything else | Go standard library |

The minimal dependency footprint is intentional. Fewer dependencies means fewer security vulnerabilities, simpler builds, faster compile times, and a smaller binary that is easy to audit.

---

## License

See `LICENSE` in the repository root.

---

*Built to make cluster automation boring — in the best way possible.*
