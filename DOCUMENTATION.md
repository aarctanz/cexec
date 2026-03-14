# cexec — Complete Technical Documentation

> **Purpose**: This document is a comprehensive reference for the author to deeply understand, maintain, and replicate the `cexec` project from scratch. It covers every architectural decision, internal package, concurrency model, caching strategy, SSH flow, real failures encountered during development, and future scope.

---

## Table of Contents

1. [Background & Motivation](#1-background--motivation)
2. [Tech Stack & Rationale](#2-tech-stack--rationale)
3. [Project Structure](#3-project-structure)
4. [Internal Package Details](#4-internal-package-details)
   - 4.1 [internal/ssh/client.go](#41-internalsshclientgo)
   - 4.2 [internal/executor/executor.go](#42-internalexecutorexecutorgo)
   - 4.3 [internal/playbook/playbook.go](#43-internalplaybookplaybookgo)
   - 4.4 [internal/inventory/inventory.go](#44-internalinventoryinventorygo)
   - 4.5 [internal/hosts/hosts.go](#45-internalhostshostsgo)
   - 4.6 [internal/state/state.go](#46-internalstatestategogo)
   - 4.7 [internal/logging/logger.go](#47-internalloggingloggergo)
   - 4.8 [internal/errors/classify.go](#48-internalerrorsclaissifygo)
5. [cmd/cexec/main.go — Entry Point](#5-cmdcexecmaingo--entry-point)
6. [Concurrency Model](#6-concurrency-model)
7. [Hash-Based Caching (Idempotency)](#7-hash-based-caching-idempotency)
8. [Template Variable System](#8-template-variable-system)
9. [SSH Authentication Flow](#9-ssh-authentication-flow)
10. [Full Process Flow Walkthrough](#10-full-process-flow-walkthrough)
11. [Real Failures Encountered & How They Were Fixed](#11-real-failures-encountered--how-they-were-fixed)
12. [Configuration Reference](#12-configuration-reference)
13. [The HPC Playbook (hpc-setup.yaml)](#13-the-hpc-playbook-hpc-setupyaml)
14. [Future Scope](#14-future-scope)
15. [Replication Guide](#15-replication-guide)

---

## 1. Background & Motivation

### What is a Beowulf HPC Cluster?

A Beowulf cluster is a network of commodity Linux machines configured to run parallel computing workloads using MPI (Message Passing Interface). Unlike supercomputers, Beowulf clusters are built from standard off-the-shelf hardware: rackmounted servers, desktop PCs, or even Raspberry Pis — all connected via a standard Ethernet switch.

The typical topology:

```
          ┌─────────────────────────────────────────┐
          │           Beowulf Cluster LAN            │
          │                                          │
          │  ┌──────────┐       ┌──────────────┐     │
          │  │  master   │       │   NFS Share  │     │
          │  │ (head)    │◄─────►│  /shared     │     │
          │  │           │       └──────────────┘     │
          │  └─────┬─────┘                            │
          │        │ SSH + MPI                        │
          │   ┌────┴─────────────────────┐            │
          │   │                          │            │
          │  ┌▼─────────┐       ┌────────▼──┐        │
          │  │  node1    │       │   node2   │  ...   │
          │  │ (compute) │       │ (compute) │        │
          │  └───────────┘       └───────────┘        │
          └─────────────────────────────────────────────┘
```

### What Needs to Happen During Setup

Setting up such a cluster from bare Ubuntu installs involves:

| Task | Target Nodes | Notes |
|------|-------------|-------|
| Sync `/etc/hosts` across all nodes | All | Every node must resolve every other node by hostname |
| Distribute master SSH key | Compute nodes | Enables passwordless MPI communication |
| Install OpenMPI | All | The MPI implementation |
| Install NFS server | Master | Serves the shared filesystem |
| Install NFS client | Compute nodes | Mounts the shared filesystem |
| Configure `/etc/exports` | Master | Defines NFS export rules |
| Start/enable NFS services | Master | rpcbind, nfs-server |
| Mount NFS share | Compute nodes | After master NFS is ready |
| Install build tools | All (optional) | gcc, make for compiling MPI programs |

### The Original Manual Approach

Before `cexec`, the process was:

1. SSH into master manually
2. Run commands one by one
3. SSH into node1, repeat
4. SSH into node2, repeat
5. ... for every node

**Problems with this approach:**
- **Slow**: On a 10-node cluster, every step takes 10x as long
- **Error-prone**: Typos, forgotten steps, wrong node
- **Not idempotent**: Running `apt install nfs-server` twice causes no harm, but manually appending to `/etc/hosts` twice creates duplicate entries, which can confuse DNS resolution
- **No visibility**: When something fails at step 7 on node3, there's no log, no trace, no easy way to resume
- **No dependency ordering**: NFS clients can't mount before the NFS server is configured — but when SSHing manually, it's easy to forget this ordering

### Why cexec Was Built

`cexec` solves all of these problems:

- **One binary, one command**: `cexec --playbook hpc-setup.yaml --auto-hosts /etc/hosts`
- **Concurrent**: All nodes run their steps simultaneously (subject to `depends_on` constraints)
- **Idempotent**: SHA256 hash tracking means completed steps are never re-run unless the command changes
- **Dependency-aware**: `depends_on: [step_name]` ensures ordering across roles
- **Visible**: Structured JSON logs, per-step per-node output, summary statistics
- **Resumable**: State persists across crashes; rerun picks up where it left off

---

## 2. Tech Stack & Rationale

### Go 1.21

**Why Go?**

| Consideration | Go | Alternative (Python/Shell) |
|--------------|----|-----------------------|
| Deployment | Single static binary, scp to master, done | Requires Python interpreter + pip packages on target |
| Concurrency | Native goroutines, channels | Threading in Python is limited by GIL; shell subprocesses are heavy |
| SSH client | `golang.org/x/crypto/ssh` — pure Go, no libssh2 dep | paramiko (Python) — works but non-trivial install |
| Compilation | `GOOS=linux GOARCH=amd64 go build` — cross-compiles trivially | N/A |
| Error handling | Explicit, no exceptions | Exception-based; silent failures common in shell |
| Standard library | Extensive: crypto, context, sync, os/exec, net | Good but fragmented |

The key win: **no runtime on target nodes**. The compiled binary is scp'd to the master node and executed. No Go installation required anywhere else.

### golang.org/x/crypto/ssh

The `x/crypto/ssh` package is a pure Go implementation of the SSH protocol. It handles:

- TCP connection and SSH handshake
- Public key authentication (parsing PEM-encoded private keys)
- Password authentication
- Session management (open session, run command, collect stdout/stderr)
- Signal sending (e.g., `ssh.SIGKILL` on context cancellation)

The package is part of the official Go extended standard library, maintained by the Go team. It does not shell out to `ssh` binary — it speaks SSH protocol directly, which makes it portable and scriptable.

**Key choice**: `ssh.InsecureIgnoreHostKey()` is used as the host key callback. In a production environment, you would use `knownhosts.New(knownHostsFile)` instead. For a private lab cluster where all machines are trusted and controlled, ignoring host key verification eliminates the "host key mismatch" failure mode that repeatedly blocked manual setup.

### gopkg.in/yaml.v3

Used for parsing both the inventory file and the playbook file. `yaml.v3` (not `yaml.v2`) is used because:

- Better error messages with line/column info
- Supports `yaml.Node` for low-level tree manipulation if needed in future
- Actively maintained

The `yaml:"-"` struct tag is used critically on the `Password` field in the Node struct to prevent passwords from ever being marshalled to YAML output.

### No Agent on Target Nodes

Unlike Ansible (which can push Python code to nodes), `cexec` requires nothing on target nodes beyond:

1. An SSH daemon listening on port 22 (or configured port)
2. A valid user account
3. Optionally, `sudo` configured

All commands run through a standard SSH session, exactly as if you typed them manually in a terminal.

---

## 3. Project Structure

```
cexec/
├── cmd/
│   └── cexec/
│       └── main.go              # ~745 lines — CLI entry point, flag parsing,
│                                #   playbook orchestration, template vars
├── internal/
│   ├── ssh/
│   │   └── client.go            # SSH dial, auth, session, exec, result
│   ├── executor/
│   │   └── executor.go          # Concurrent runner, retry, backoff
│   ├── playbook/
│   │   └── playbook.go          # YAML loader + structural validator
│   ├── inventory/
│   │   └── inventory.go         # Node struct, YAML loader, selector logic
│   ├── hosts/
│   │   └── hosts.go             # /etc/hosts parser → inventory auto-build
│   ├── state/
│   │   └── state.go             # SHA256 step cache, atomic JSON persistence
│   ├── logging/
│   │   └── logger.go            # Structured JSON run logs
│   └── errors/
│       └── classify.go          # Error classification + retry policy
├── hpc-setup.yaml               # Production HPC cluster setup playbook
├── inventory.yaml               # Optional manual inventory
├── cluster.env.example          # Config template with all keys
└── DOCUMENTATION.md             # This file
```

### Module Boundary Philosophy

The `internal/` prefix in Go is a language-enforced boundary: these packages cannot be imported by any Go code outside the `cexec` module. This means:

- `internal/ssh` can only be used by `cmd/cexec/main.go` and other `internal/` packages
- External tools cannot depend on these internals without copying the code

This is intentional. The packages are not designed as a library — they are implementation details of `cexec`. If `cexec` is ever published as a library, the relevant packages would be moved to a top-level directory without `internal/`.

### Dependency Graph Between Packages

```
cmd/cexec/main.go
    ├── internal/inventory
    ├── internal/hosts
    ├── internal/playbook
    ├── internal/state
    ├── internal/executor
    │       └── internal/ssh
    │       └── internal/errors
    │       └── internal/logging
    └── internal/logging
```

No circular dependencies. `internal/ssh` and `internal/errors` are leaf packages with no internal imports. `internal/executor` depends on all three of those. `main.go` depends on everything.

---

## 4. Internal Package Details

### 4.1 internal/ssh/client.go

**File**: `internal/ssh/client.go`

This package is responsible for one thing: take a node description and a command string, open an SSH session, run the command, and return a structured result.

#### The Result Struct

```go
type Result struct {
    Node      inventory.Node
    Host      string
    Stdout    string
    Stderr    string
    ExitCode  int
    Start     time.Time
    End       time.Time
    Duration  time.Duration
    RawError  error
    Success   bool
}
```

Every field is populated by `RunCommand`. `RawError` is the raw Go error (e.g., `io.EOF`, a net.OpError). `Success` is true only when `RawError == nil` AND `ExitCode == 0`.

#### RunCommand

```go
func RunCommand(ctx context.Context, node inventory.Node, cmd string, sudo bool, sudoPass string) Result
```

**Step-by-step execution**:

1. Build `*ssh.ClientConfig` via `buildSSHConfig(node)`
2. Dial TCP to `host:port` — this is wrapped to respect `ctx`: if `ctx` is cancelled during dial, the connection attempt is abandoned
3. Perform SSH handshake (`ssh.NewClientConn`)
4. Open a new `ssh.Session`
5. If `sudo=true` and `sudoPass != ""`:
   - Wrap command as `echo 'PASSWORD' | sudo -S sh -c "COMMAND"`
   - The `-S` flag tells sudo to read the password from stdin
   - The password never appears in `ps aux` output because it's passed through a pipe, not as a command-line argument
   - The outer `sh -c` handles commands with shell metacharacters (pipes, redirects, semicolons)
6. If `sudo=true` and `sudoPass == ""`:
   - Wrap as `sudo sh -c "COMMAND"` — assumes `NOPASSWD` in sudoers
7. Run the command via `session.CombinedOutput()` or separate `session.Stdout`/`session.Stderr` pipes
8. Collect exit code from `*ssh.ExitError`
9. Return populated `Result`

#### Context Cancellation

The command is run inside a goroutine. The calling goroutine does:

```go
select {
case result := <-resultCh:
    return result
case <-ctx.Done():
    session.Signal(ssh.SIGKILL)
    return Result{RawError: ctx.Err(), ...}
}
```

`ssh.SIGKILL` is sent to the remote process. This is the SSH protocol's signal forwarding mechanism — it does NOT kill the SSH connection; it sends a signal to the process running on the remote side.

#### buildSSHConfig

```go
func buildSSHConfig(node inventory.Node) (*ssh.ClientConfig, error)
```

Auth methods are built in order:

1. Try to read `~/.ssh/id_ed25519`; parse with `ssh.ParsePrivateKey`; if successful, add `ssh.PublicKeys(signer)` to auth list
2. Try `~/.ssh/id_rsa` as fallback
3. If `node.Password != ""`, append `ssh.Password(node.Password)`

The resulting auth list might be `[PublicKeys, Password]` or just `[PublicKeys]` or just `[Password]` depending on what's available. The SSH server tries them in order.

**Host key verification**:

```go
HostKeyCallback: ssh.InsecureIgnoreHostKey(),
```

This accepts any host key without verification. It is intentionally insecure and appropriate only for trusted internal networks. The comment in the code explicitly warns against using this in production.

---

### 4.2 internal/executor/executor.go

**File**: `internal/executor/executor.go`

The executor abstracts "run a command on a set of nodes concurrently, with retry and backoff."

#### Options Struct

```go
type Options struct {
    Command        string
    Sudo           bool
    SudoPassEnvFn  func(nodeName string) string  // returns sudo password per node
    Timeout        time.Duration
    MaxConcurrency int      // 0 = unlimited
    MaxRetries     int      // 0 = no retry
    BackoffBase    time.Duration
    BackoffFixed   bool     // if true: fixed backoff; if false: exponential
}
```

`SudoPassEnvFn` is a function rather than a static string because different nodes might have different passwords (set via per-node env vars, see Configuration Reference section). The function is called per node at execution time.

#### Run

```go
func Run(ctx context.Context, nodes []inventory.Node, opts Options) []logging.NodeLog
```

**Implementation**:

```
if MaxConcurrency > 0:
    semaphore = make(chan struct{}, MaxConcurrency)

for each node:
    wg.Add(1)
    go func(node):
        if semaphore != nil:
            semaphore <- struct{}{}      // acquire slot
            defer func() { <-semaphore }()  // release slot

        result = executeWithRetry(ctx, node, opts)
        results <- result
        wg.Done()

go func():
    wg.Wait()
    close(results)

collect results into []NodeLog
```

The semaphore pattern (buffered channel used as counting semaphore) limits how many goroutines are actually executing SSH commands simultaneously. This is important when `cexec` is run against large clusters (50+ nodes) — too many simultaneous SSH connections can exhaust file descriptors or overwhelm the SSH server's MaxStartups limit.

#### executeWithRetry

```go
func executeWithRetry(ctx context.Context, node inventory.Node, opts Options) logging.NodeLog
```

```
for attempt := 0; attempt <= MaxRetries; attempt++:
    attemptCtx, cancel = context.WithTimeout(ctx, opts.Timeout)

    result = ssh.RunCommand(attemptCtx, node, opts.Command, opts.Sudo, sudoPass)
    cancel()

    if result.Success:
        return NodeLog{..., Retries: attempt}

    category = errors.ClassifyError(result.RawError, result.ExitCode)

    if !isRetryable(category):
        break

    if attempt < MaxRetries:
        backoff = computeBackoff(attempt, opts.BackoffBase, opts.BackoffFixed)
        select:
            case <-time.After(backoff):  // wait and retry
            case <-ctx.Done():           // cancelled, stop
                break

return NodeLog{..., Retries: attempt}
```

**Retryable error categories** (from `internal/errors/classify.go`):
- `ConnectionTimeout` — TCP dial timed out
- `CommandTimeout` — context deadline during command execution
- `SSHConnectionFailed` — TCP connection refused or reset
- `HostUnreachable` — no route to host
- `DNSResolutionFailed` — hostname not resolvable

**Non-retryable** (permanent failures — retrying won't help):
- `SSHAuthFailed` — wrong key or password
- `SudoAuthFailed` — wrong sudo password
- `SudoPermissionDenied` — user not in sudoers
- `RemoteCommandFailed` — command ran and returned non-zero
- `ShellNotFound` — `/bin/sh` missing (highly unusual)
- `BinaryNotFound` — command binary not found on PATH
- `NonZeroExit` — clean exit with failure code
- `UnknownError` — anything else

#### computeBackoff

```go
func computeBackoff(attempt int, base time.Duration, fixed bool) time.Duration
```

- `fixed=true`: always returns `base` (e.g., retry after 2s every time)
- `fixed=false`: exponential — `base * 2^(attempt-1)`, capped at 60 seconds

| Attempt | Exponential (base=2s) | Fixed (base=2s) |
|---------|----------------------|-----------------|
| 0 → 1   | 2s                   | 2s              |
| 1 → 2   | 4s                   | 2s              |
| 2 → 3   | 8s                   | 2s              |
| 3 → 4   | 16s                  | 2s              |
| 4 → 5   | 32s                  | 2s              |
| 5 → 6   | 60s (cap)            | 2s              |

#### RunSingle and DryRun

```go
func RunSingle(ctx context.Context, node inventory.Node, opts Options) logging.NodeLog
func DryRun(nodes []inventory.Node, opts Options)
```

`RunSingle` is the same as `Run` but for one node — used by the playbook runner where concurrency is managed by the playbook goroutines, not the executor. Calling `Run` with one node would work too but adds unnecessary overhead.

`DryRun` prints `WOULD RUN on <node>: <command>` without making any SSH connections. Used when `--dry-run` flag is set for single-command mode.

---

### 4.3 internal/playbook/playbook.go

**File**: `internal/playbook/playbook.go`

Handles loading and validating YAML playbook files.

#### Data Structures

```go
type Step struct {
    Name      string   `yaml:"name"`
    Command   string   `yaml:"command"`
    Sudo      bool     `yaml:"sudo"`
    Roles     []string `yaml:"roles"`      // only run on nodes whose group is in this list
    DependsOn []string `yaml:"depends_on"` // step names that must complete before this step
}

type Playbook struct {
    Name  string `yaml:"name"`
    Steps []Step `yaml:"steps"`
}
```

#### Load

```go
func Load(path string) (*Playbook, error)
```

Reads the YAML file, unmarshals into `Playbook`, then validates:

1. At least one step exists
2. Every step has a non-empty `name`
3. Every step has a non-empty `command`
4. Every step name referenced in any `depends_on` list corresponds to an actual step that exists in the playbook
5. (No cycle detection — this is noted as future work; a cycle currently causes deadlock)

Validation happens at load time — before any SSH connections are attempted. This means a malformed playbook is caught immediately rather than after connecting to 20 nodes and starting execution.

**Example valid playbook step**:

```yaml
- name: sync cluster hosts
  command: "{{hosts_sync_cmds}}"
  sudo: true
  roles: [all]
  depends_on: []

- name: mount nfs share
  command: "mount -t nfs master:/shared /shared"
  sudo: true
  roles: [compute]
  depends_on: [export nfs share]
```

**Example validation failure**:

```yaml
- name: install mpi
  depends_on: [setup nfs]   # ERROR: no step named "setup nfs" exists
```

This would fail with an error like: `step "install mpi": depends_on references unknown step "setup nfs"`.

---

### 4.4 internal/inventory/inventory.go

**File**: `internal/inventory/inventory.go`

Manages the list of nodes and provides filtering.

#### Node Struct

```go
type Node struct {
    Name     string   `yaml:"name"     json:"name"`
    Host     string   `yaml:"host"     json:"host"`
    User     string   `yaml:"user"     json:"user"`
    Port     int      `yaml:"port"     json:"port"`
    Groups   []string `yaml:"groups"   json:"groups"`
    Password string   `yaml:"-"        json:"-"`
}
```

**Critical design decision**: `Password` is tagged `yaml:"-" json:"-"`. This means:

- It is never serialized when a `Node` is marshalled to YAML or JSON
- It cannot be accidentally leaked into log files (which contain JSON-serialized node info)
- It cannot be accidentally written back to an inventory file

Passwords are injected into nodes at runtime in `main.go`, after the inventory is loaded, from environment variables.

#### Load

```go
func Load(path string) ([]Node, error)
```

Reads a YAML file in the format:

```yaml
nodes:
  - name: master
    host: 172.16.0.1
    user: hpc
    port: 22
    groups: [control]
  - name: node1
    host: 172.16.0.2
    user: hpc
    groups: [compute]
```

Applies defaults after unmarshalling:
- `Port` defaults to `22` if zero
- `User` defaults to `"root"` if empty

#### Select

```go
func Select(inv []Node, selector string, exclude []string) []Node
```

**Selector logic**:

| Selector value | Behavior |
|---------------|----------|
| `"all"` or `""` | Returns all nodes |
| A group name (e.g., `"compute"`) | Returns nodes where `node.Groups` contains that group |
| Comma-separated names (e.g., `"node1,node2"`) | Returns nodes whose `Name` is in the list |
| A single node name (e.g., `"master"`) | Returns the one matching node |

**Exclude logic**: `exclude` is a `[]string` of node names to remove from the result. The function builds an `excludeSet` (map[string]bool) for O(1) lookup, then filters.

Example:

```
Select(inv, "compute", []string{"node3"})
→ all compute nodes except node3
```

---

### 4.5 internal/hosts/hosts.go

**File**: `internal/hosts/hosts.go`

Parses a Linux `/etc/hosts` file and builds a `[]inventory.Node` automatically. This enables `cexec` to work without a manually-written `inventory.yaml` — it discovers nodes directly from the cluster's hosts file.

#### Cluster Host Regex

```go
var clusterHostRe = regexp.MustCompile(`^(master|node\d+)$`)
```

This matches:
- `"master"` — the head node
- `"node1"`, `"node2"`, ..., `"node99"`, `"node100"`, etc. — any compute node

It does NOT match:
- `"localhost"`, `"ip6-localhost"` — loopback aliases
- `"ubuntu"`, `"myserver"` — arbitrary hostnames
- `"node"` (without a number) — must have trailing digits

#### IsClusterHost

```go
func IsClusterHost(name string) bool {
    return clusterHostRe.MatchString(name)
}
```

Exported function used by `main.go`'s `buildHostsSyncCmds` to identify which lines from the hosts file are cluster entries that need to be synced to all nodes.

#### LoadInventory

```go
func LoadInventory(hostsFile string, user string, port int) ([]inventory.Node, error)
```

**Algorithm**:

```
open hostsFile
for each line:
    strip comments (everything after #)
    split on whitespace
    if fewer than 2 fields: skip (blank line or IP-only)
    ip = fields[0]
    for each hostname in fields[1:]:
        if IsClusterHost(hostname):
            if hostname == "master": group = "control"
            else: group = "compute"
            if hostname not in seen:
                append Node{Name: hostname, Host: ip, User: user, Port: port, Groups: [group]}
                seen[hostname] = true
```

Deduplication (`seen` map) handles the case where a hostname appears on multiple lines in `/etc/hosts` (e.g., with different aliases). Only the first occurrence is used.

**Example input** (`/etc/hosts`):

```
127.0.0.1       localhost
127.0.1.1       ubuntu  # skip — not a cluster host

172.16.0.1      master
172.16.0.2      node1
172.16.0.3      node2
172.16.0.4      node3
```

**Example output** (4 nodes):

```go
[]Node{
    {Name: "master", Host: "172.16.0.1", Groups: ["control"]},
    {Name: "node1",  Host: "172.16.0.2", Groups: ["compute"]},
    {Name: "node2",  Host: "172.16.0.3", Groups: ["compute"]},
    {Name: "node3",  Host: "172.16.0.4", Groups: ["compute"]},
}
```

---

### 4.6 internal/state/state.go

**File**: `internal/state/state.go`

Implements the hash-based idempotency cache. This is what allows `cexec` to skip steps that have already completed successfully.

#### Data Structures

```go
type Entry struct {
    Node      string    `json:"node"`
    StepName  string    `json:"step_name"`
    Hash      string    `json:"hash"`       // 8-byte hex = 16 chars
    Timestamp time.Time `json:"timestamp"`
}

type State struct {
    mu      sync.Mutex
    path    string
    entries map[string]Entry  // key: "nodeName:hash"
}
```

The `entries` map key is `"nodeName:hash"` rather than `"nodeName:stepName"` because the cache is keyed by CONTENT (what the command is) rather than IDENTITY (what the step is named). This means:

- Renaming a step does not invalidate its cache (good — the command is the same)
- Changing the command DOES invalidate its cache (correct — it needs to re-run)

#### Hash

```go
func Hash(nodeName, stepName, command string) string {
    data := []byte(nodeName + "|" + stepName + "|" + command)
    sum := sha256.Sum256(data)
    return hex.EncodeToString(sum[:8])  // first 8 bytes = 16 hex chars
}
```

Input components:
- `nodeName` — same command on different nodes produces different hashes
- `stepName` — included for human readability in the state file (not strictly needed for uniqueness since command already varies by content)
- `command` — the EXPANDED command (after template variable substitution)

Using only the first 8 bytes of SHA256 gives 2^64 possible values — collision probability is negligible for any realistic cluster size.

**Example**:

```
Hash("node1", "sync cluster hosts", "grep -qxF '172.16.0.1  master' /etc/hosts ...")
→ "a3f9c21b8e4d7f60"
```

#### Done and Mark

```go
func (s *State) Done(nodeName, hash string) bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    key := nodeName + ":" + hash
    _, exists := s.entries[key]
    return exists
}

func (s *State) Mark(nodeName, stepName, hash string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    key := nodeName + ":" + hash
    s.entries[key] = Entry{Node: nodeName, StepName: stepName, Hash: hash, Timestamp: time.Now()}
}
```

Both are mutex-protected because multiple goroutines (one per node) may call them concurrently.

#### Save — Atomic Write

```go
func (s *State) Save() error {
    s.mu.Lock()
    defer s.mu.Unlock()

    // Convert map to slice for JSON serialization
    entries := make([]Entry, 0, len(s.entries))
    for _, e := range s.entries {
        entries = append(entries, e)
    }

    data, err := json.MarshalIndent(entries, "", "  ")
    if err != nil { return err }

    // Write to temp file first
    tmpPath := s.path + ".tmp"
    if err := os.WriteFile(tmpPath, data, 0644); err != nil { return err }

    // Atomic rename
    return os.Rename(tmpPath, s.path)
}
```

The atomic write pattern (write to `.tmp`, then rename) is critical for crash safety. `os.Rename` is atomic on POSIX systems — it either completes fully or not at all. If `cexec` is killed while writing, the `.tmp` file is left behind but the main state file is intact.

`Save()` is called after EVERY step result (success or skip). This means state is durable after each individual step, so a crash mid-playbook loses at most one step's progress.

#### Load

```go
func Load(path string) (*State, error)
```

If the file doesn't exist, returns an empty `State` (no error). This is the normal case on first run. If the file exists but is corrupted (invalid JSON), returns an error.

---

### 4.7 internal/logging/logger.go

**File**: `internal/logging/logger.go`

Provides structured JSON logging for run results. This is separate from real-time console output (which is handled by the printer goroutine in `main.go`).

#### Data Structures

```go
type NodeLog struct {
    ssh.Result                           // embedded — all Result fields are promoted
    ErrorCategory errors.ErrorCategory  `json:"error_category,omitempty"`
    Retries       int                   `json:"retries"`
}

type RunLog struct {
    RunID     string        `json:"run_id"`
    Command   string        `json:"command"`
    Sudo      bool          `json:"sudo"`
    StartTime time.Time     `json:"start_time"`
    EndTime   time.Time     `json:"end_time"`
    Duration  time.Duration `json:"duration_ms"`
    Summary   Summary       `json:"summary"`
    Results   []NodeLog     `json:"results"`
}

type Summary struct {
    Total       int `json:"total"`
    Succeeded   int `json:"succeeded"`
    Failed      int `json:"failed"`
    Unreachable int `json:"unreachable"`
    TimedOut    int `json:"timed_out"`
    Retried     int `json:"retried"`
}
```

`RunID` is generated as a timestamp + random hex string: `20240315_143022_a3f9c2b1`. This ensures log filenames are sortable by time and unique.

#### BuildSummary

```go
func BuildSummary(logs []NodeLog) Summary
```

Categorization logic:

```
for each log:
    if log.Success: Succeeded++
    else if category in (HostUnreachable, SSHConnectionFailed, DNSResolutionFailed): Unreachable++
    else if category in (ConnectionTimeout, CommandTimeout): TimedOut++
    else: Failed++

    if log.Retries > 0: Retried++
```

This gives operators a clear breakdown: was it a network problem (Unreachable), a timeout, or an actual command failure?

#### WriteLog

```go
func WriteLog(logDir string, runLog RunLog) (string, error)
```

- Creates `logDir` if it doesn't exist (with `os.MkdirAll`)
- Serializes `RunLog` with `json.MarshalIndent` (human-readable)
- Writes to `logDir/run_<runID>.json`
- Returns the path of the written file

Log files are never rotated or cleaned up automatically. In the HPC cluster context, disk space is not a concern, and old logs are valuable for post-mortem analysis.

---

### 4.8 internal/errors/classify.go

**File**: `internal/errors/classify.go`

Translates raw Go errors and command exit codes into a typed enumeration.

#### ErrorCategory

```go
type ErrorCategory string

const (
    SSHConnectionFailed   ErrorCategory = "ssh_connection_failed"
    SSHAuthFailed         ErrorCategory = "ssh_auth_failed"
    HostUnreachable       ErrorCategory = "host_unreachable"
    DNSResolutionFailed   ErrorCategory = "dns_resolution_failed"
    ConnectionTimeout     ErrorCategory = "connection_timeout"
    CommandTimeout        ErrorCategory = "command_timeout"
    SudoAuthFailed        ErrorCategory = "sudo_auth_failed"
    SudoPermissionDenied  ErrorCategory = "sudo_permission_denied"
    RemoteCommandFailed   ErrorCategory = "remote_command_failed"
    ShellNotFound         ErrorCategory = "shell_not_found"
    BinaryNotFound        ErrorCategory = "binary_not_found"
    NonZeroExit           ErrorCategory = "non_zero_exit"
    UnknownError          ErrorCategory = "unknown_error"
)
```

#### ClassifyError

```go
func ClassifyError(rawErr error, exitCode int) ErrorCategory
```

Implementation uses **substring matching on the lowercased error string**:

```go
msg := strings.ToLower(rawErr.Error())

switch {
case strings.Contains(msg, "connection refused"):
    return SSHConnectionFailed
case strings.Contains(msg, "no route to host"):
    return HostUnreachable
case strings.Contains(msg, "no such host"):
    return DNSResolutionFailed
case strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "deadline exceeded"):
    return ConnectionTimeout
case strings.Contains(msg, "unable to authenticate"):
    return SSHAuthFailed
// ... etc
}

if exitCode != 0 {
    return NonZeroExit
}
return UnknownError
```

**Why string matching instead of type assertions?**

SSH errors come through several layers of wrapping (Go's `net.OpError`, `*ssh.ExitError`, etc.). Unwrapping them consistently across Go versions and SSH server implementations is fragile. String matching on the lowercased error message is more robust and covers variations in error wording from different SSH implementations.

**Retry policy** (in `executor.go`, based on this classification):

```go
func isRetryable(cat errors.ErrorCategory) bool {
    switch cat {
    case errors.ConnectionTimeout,
         errors.CommandTimeout,
         errors.SSHConnectionFailed,
         errors.HostUnreachable,
         errors.DNSResolutionFailed:
        return true
    }
    return false
}
```

Auth failures, permission denials, and command failures are never retried — they require human intervention.

---

## 5. cmd/cexec/main.go — Entry Point

**File**: `cmd/cexec/main.go` (~745 lines)

This is the brain of `cexec`. It ties together all internal packages, handles flag parsing, env file loading, template expansion, playbook orchestration, and signal handling.

### Pre-Flag-Parse Environment Loading

**Problem**: Go's `flag` package assigns default values at `flag.String(name, default, usage)` call time, before `flag.Parse()` is called. If defaults are supposed to come from an env file (`--env-file cluster.env`), there's a chicken-and-egg problem: the env file path is a flag, but env vars need to be set before other flags' defaults are evaluated.

**Solution**: Scan `os.Args` manually before calling `flag.Parse()`:

```go
// In init() or at the top of main(), before any flag.String() calls:
for i, arg := range os.Args[1:] {
    if arg == "--env-file" && i+1 < len(os.Args[1:]) {
        loadEnvFile(os.Args[i+2])
        break
    }
    if strings.HasPrefix(arg, "--env-file=") {
        loadEnvFile(strings.TrimPrefix(arg, "--env-file="))
        break
    }
}
```

This manual scan finds `--env-file` before `flag.Parse()`. After `loadEnvFile()` sets env vars, the subsequent `flag.String("timeout", envDefault("CLUSTER_TIMEOUT", "5m"), ...)` calls pick up the right defaults.

### loadEnvFile

```go
func loadEnvFile(path string) error
```

Parses a `KEY=VALUE` file:

```
# This is a comment — skipped
CLUSTER_USER=hpc
CLUSTER_PASSWORD="my secret password"   # quotes stripped
CLUSTER_LOG_DIR=logs

# Blank lines skipped
```

Rules:
1. Lines starting with `#` (after stripping leading whitespace) are skipped
2. Inline `#` comments are stripped
3. Surrounding single or double quotes are stripped from values
4. **If the env var is already set** (`os.Getenv(k) != ""`), the file value is ignored — real environment always wins

This last rule is important: it means you can always override a cluster.env setting with an actual env var without modifying the file.

### envDefault

```go
func envDefault(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

Used as the `default` argument to `flag.String()` and `flag.Int()`:

```go
timeout := flag.String("timeout", envDefault("CLUSTER_TIMEOUT", "5m"), "...")
```

This creates the priority chain: **CLI flag > env var > env file > hardcoded default**.

### buildHostsSyncCmds

```go
func buildHostsSyncCmds(hostsFile string) string
```

Reads the hosts file and generates a shell command that idempotently adds each cluster line to `/etc/hosts` on target nodes.

**For each cluster line** (where hostname matches `IsClusterHost`):

```
"grep -qxF '172.16.0.1  master' /etc/hosts 2>/dev/null || echo '172.16.0.1  master' >> /etc/hosts"
```

- `grep -qxF`: quiet (`-q`), exact line match (`-x`), fixed string (`-F`) — returns exit code 0 if line exists
- `|| echo ... >> /etc/hosts`: only appends if grep returned non-zero (line not found)
- `2>/dev/null`: suppresses grep's stderr if the file doesn't exist

Multiple such commands are joined with ` ; ` to form one compound command. This single compound command is the value of `{{hosts_sync_cmds}}`.

**Example output** (for a 3-node cluster):

```
grep -qxF '172.16.0.1  master' /etc/hosts 2>/dev/null || echo '172.16.0.1  master' >> /etc/hosts ; grep -qxF '172.16.0.2  node1' /etc/hosts 2>/dev/null || echo '172.16.0.2  node1' >> /etc/hosts ; grep -qxF '172.16.0.3  node2' /etc/hosts 2>/dev/null || echo '172.16.0.3  node2' >> /etc/hosts
```

### expandVars

```go
func expandVars(cmd string, vars map[string]string) string
```

Simple `strings.ReplaceAll` iteration over the vars map:

```go
for k, v := range vars {
    cmd = strings.ReplaceAll(cmd, "{{"+k+"}}", v)
}
return cmd
```

No template engine, no regex — just direct string replacement. This is intentional: template engines add complexity and potential injection vectors. The `{{key}}` syntax was chosen to be visually distinct from shell `$VAR` and Bash `${VAR}` syntax, reducing accidental conflicts.

**Template vars built in main.go**:

```go
vars := map[string]string{
    "master_pubkey":   masterPubKey,      // from readMasterPubKey()
    "hosts_sync_cmds": hostsSyncCmds,    // from buildHostsSyncCmds()
}
```

### readMasterPubKey

```go
func readMasterPubKey() (string, error)
```

Tries `~/.ssh/id_ed25519.pub`, then `~/.ssh/id_rsa.pub`. Returns the trimmed contents. If neither exists, returns an empty string and a warning (not a fatal error — not all playbooks use `{{master_pubkey}}`).

### Signal Handling

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

go func() {
    sig := <-sigCh
    log.Printf("Received %s, cancelling...", sig)
    cancel()  // cancel the context

    sig = <-sigCh  // second signal
    os.Exit(130)   // force exit
}()
```

First SIGINT (Ctrl+C): cancels the context, which propagates to all goroutines. They detect `ctx.Done()` and stop their `select`. The printer goroutine drains remaining results, then the program exits cleanly.

Second SIGINT: immediate hard exit. This handles the case where a goroutine is stuck and not responding to context cancellation.

Exit code 130 is the POSIX convention for "terminated by signal" (128 + signal number 2 for SIGINT).

### runPlaybook

```go
func runPlaybook(
    ctx context.Context,
    pb *playbook.Playbook,
    nodes []inventory.Node,
    st *state.State,
    vars map[string]string,
    opts executor.Options,
    logDir string,
    quiet bool,
) error
```

This is the most complex function in the codebase. Its detailed operation is described in Section 6 (Concurrency Model).

### dryRunPlaybook

```go
func dryRunPlaybook(pb *playbook.Playbook, nodes []inventory.Node, vars map[string]string)
```

Iterates the same step/node matrix as `runPlaybook` but prints `WOULD RUN` or `SKIP (role filter)` without making any SSH connections. Uses the same role-filtering logic so the dry run accurately reflects what would actually execute.

---

## 6. Concurrency Model

This is the most architecturally complex part of `cexec`. Understanding it fully is essential for replication.

### The Problem

Given:
- `N` nodes (master + compute nodes)
- `S` steps in the playbook
- Steps have `depends_on` constraints
- Steps have `roles` filters (only certain node groups run certain steps)

We want:
- All nodes execute their steps concurrently (no sequential node processing)
- But within a single node, steps run in playbook order
- Step B on a compute node cannot start until step A (a dependency) has completed on ALL applicable nodes

### The Channel-Based Solution

**Setup phase** (runs once, before any goroutines are launched):

```go
// 1. Build name-to-index map
stepIdx := make(map[string]int, len(pb.Steps))
for i, step := range pb.Steps {
    stepIdx[step.Name] = i
}

// 2. Pre-compute per-step signals and pending counts
signals := make([]chan struct{}, len(pb.Steps))
pending := make([]int32, len(pb.Steps))

for i, step := range pb.Steps {
    signals[i] = make(chan struct{})

    // Count nodes that will actually run this step
    for _, node := range nodes {
        if nodeMatchesRoles(node, step.Roles) {
            pending[i]++
        }
    }

    // Steps with zero applicable nodes: close signal immediately
    if pending[i] == 0 {
        close(signals[i])
    }
}
```

**Per-node goroutine** (one launched per node):

```go
go func(node inventory.Node) {
    for i, step := range pb.Steps {
        // 1. Wait for all dependencies
        for _, depName := range step.DependsOn {
            di := stepIdx[depName]
            select {
            case <-signals[di]:     // dependency completed on all its nodes
                // proceed
            case <-ctx.Done():
                return
            }
        }

        // 2. Role filter
        if !nodeMatchesRoles(node, step.Roles) {
            continue  // DON'T touch pending — this node was never counted
        }

        // 3. State cache check
        expandedCmd := expandVars(step.Command, vars)
        hash := state.Hash(node.Name, step.Name, expandedCmd)
        if st.Done(node.Name, hash) {
            resultsCh <- NodeLog{..., skipped: true}
            signalDone(i)
            continue
        }

        // 4. Execute
        result := executor.RunSingle(ctx, node, opts)
        st.Mark(node.Name, step.Name, hash)
        st.Save()
        resultsCh <- NodeLog{...}
        signalDone(i)
    }
}(node)
```

**signalDone**:

```go
var mu sync.Mutex

func signalDone(i int) {
    mu.Lock()
    defer mu.Unlock()
    pending[i]--
    if pending[i] == 0 {
        close(signals[i])
    }
}
```

Closing a channel broadcasts to all receivers. Every goroutine blocked in `select { case <-signals[i]: }` unblocks simultaneously when the channel is closed.

### Visual Walkthrough — 3-Node Cluster

Playbook steps (simplified):

```
Step 0: sync hosts          roles=[all]      depends_on=[]
Step 1: install nfs-server  roles=[control]  depends_on=[sync hosts]
Step 2: install nfs-client  roles=[compute]  depends_on=[sync hosts]
Step 3: export nfs share    roles=[control]  depends_on=[install nfs-server]
Step 4: mount nfs share     roles=[compute]  depends_on=[export nfs share]
```

**Pre-computation**:

```
pending[0] = 3  (master, node1, node2 all match "all")
pending[1] = 1  (only master matches "control")
pending[2] = 2  (node1, node2 match "compute")
pending[3] = 1  (only master matches "control")
pending[4] = 2  (node1, node2 match "compute")

signals[0..4]: all channels open (waiting)
```

**Execution timeline**:

```
TIME →

master goroutine:
  Step 0: sync hosts    ─────[SSH]─────► done; pending[0]=2;
  Step 1: nfs-server    wait sig[0]  ───────────────────────────[SSH]─────► done; pending[1]=0; CLOSE sig[1]
  Step 2: nfs-client    SKIP (role filter, not counted)
  Step 3: export nfs    wait sig[1]  ──────────────────────────────────────────────────[SSH]──► done; pending[3]=0; CLOSE sig[3]
  Step 4: mount         SKIP (role filter, not counted)

node1 goroutine:
  Step 0: sync hosts    ─────[SSH]─────► done; pending[0]=1;
  Step 1: nfs-server    SKIP (role filter, not counted)
  Step 2: nfs-client    wait sig[0]  ────────────────────────────[SSH]───────────────► done; pending[2]=1;
  Step 3: export nfs    SKIP (role filter, not counted)
  Step 4: mount nfs     wait sig[3]  ────────────────────────────────────────────────────────────[SSH]──►

node2 goroutine:
  Step 0: sync hosts    ─────[SSH]─────► done; pending[0]=0; CLOSE sig[0] ← unblocks master & node1 & node2's waits!
  Step 1: nfs-server    SKIP (role filter, not counted)
  Step 2: nfs-client    wait sig[0]  ────────────────────────────[SSH]───────────────► done; pending[2]=0; CLOSE sig[2]
  Step 3: export nfs    SKIP (role filter, not counted)
  Step 4: mount nfs     wait sig[3]  ────────────────────────────────────────────────────────────[SSH]──►
```

**Key observations**:

1. `sync hosts` runs on all 3 nodes simultaneously (no dependency)
2. `node1` and `node2` start `install nfs-client` as soon as `sync hosts` completes on ALL nodes — they don't wait for each other
3. `master` starts `install nfs-server` at the same time as `node1`/`node2` start `install nfs-client`
4. `mount nfs share` on node1 and node2 waits until master has completed `export nfs share`
5. The `pending` counter tracks how many nodes need to complete a step. The channel closes only when ALL applicable nodes are done.

### Why Channels vs Mutexes

Alternative: use a `sync.WaitGroup` per step. Problem: `WaitGroup.Wait()` is not `select`-able. You can't do:

```go
select {
case wg.Wait():   // INVALID — not a channel
case <-ctx.Done():
}
```

With a channel, the `select` is straightforward. The cost is slightly more memory (one channel per step) but channels are cheap in Go (a few hundred bytes each).

### Results Channel and Printer

```go
resultsCh := make(chan logging.NodeLog, len(nodes)*len(pb.Steps))
```

The buffer size is `nodes * steps` — large enough that no goroutine ever blocks on send (all results are buffered). This is a deliberate choice to prevent back-pressure from the printer from slowing down execution.

A separate goroutine reads from `resultsCh` and prints results:

```go
go func() {
    for log := range resultsCh {
        if !quiet {
            printResult(log)
        }
        allLogs = append(allLogs, log)
    }
    doneCh <- struct{}{}
}()
```

After all node goroutines finish (tracked by a `WaitGroup`), `resultsCh` is closed, which terminates the printer goroutine's range loop.

---

## 7. Hash-Based Caching (Idempotency)

### The Core Problem

Without caching, re-running `cexec --playbook hpc-setup.yaml` after a partial failure would re-run ALL steps from the beginning. For HPC setup, this means:
- Re-running `apt-get install openmpi-bin` (slow, 30+ seconds)
- Re-appending keys to `authorized_keys` (causes duplicate entries)
- Re-mounting NFS (fails with "already mounted" error)

### How It Works

For each (node, step) pair about to execute:

```
1. Expand template variables in the command
2. Compute: sha256(nodeName + "|" + stepName + "|" + expandedCommand)[0:8] → hex string
3. Check: is this hash in the state file for this node?
   YES → skip (print "SKIP [step] on [node]")
   NO  → execute; on completion, record hash; save state
```

### What the State File Looks Like

`.cexec_state.json`:

```json
[
  {
    "node": "master",
    "step_name": "sync cluster hosts",
    "hash": "a3f9c21b",
    "timestamp": "2024-03-15T14:32:01Z"
  },
  {
    "node": "node1",
    "step_name": "sync cluster hosts",
    "hash": "b7e4d209",
    "timestamp": "2024-03-15T14:32:01Z"
  },
  {
    "node": "master",
    "step_name": "install nfs-server",
    "hash": "f1a3c890",
    "timestamp": "2024-03-15T14:33:45Z"
  }
]
```

Note that `master` and `node1` have **different hashes** for the same step name "sync cluster hosts". This is because the hash includes `nodeName`, so each node gets its own cache entry.

### When the Cache Invalidates

| Scenario | Hash changes? | Effect |
|----------|--------------|--------|
| Rerun with no changes | No | Step skipped |
| Edit command text in playbook | Yes | Step re-runs |
| Add a new node to `/etc/hosts` | Yes (for steps using `{{hosts_sync_cmds}}`) | All nodes re-sync hosts |
| Rotate SSH key (`master_pubkey` changes) | Yes (for key distribution steps) | Key re-pushed to all nodes |
| Rename a step in playbook | No | Step skipped (hash is content-based, not name-based) |
| Change `nodeName` (rename in inventory) | Yes | Step runs as if new node |
| `--force` flag | N/A | State file ignored entirely |

### Forcing Re-Execution

```
cexec --playbook hpc-setup.yaml --force
```

The `--force` flag skips all state cache lookups. Every step runs unconditionally.

Alternatively, delete the state file:

```
rm .cexec_state.json
```

Or delete specific entries manually (it's plain JSON).

### State File Location

Default: `.cexec_state.json` in the current working directory. Override with:

```
cexec --state-file /var/lib/cexec/state.json --playbook ...
```

Or via env:

```
CLUSTER_STATE_FILE=/var/lib/cexec/state.json
```

---

## 8. Template Variable System

### Available Variables

| Variable | Source | Example Value |
|----------|--------|--------------|
| `{{master_pubkey}}` | `~/.ssh/id_ed25519.pub` or `~/.ssh/id_rsa.pub` | `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5... user@host` |
| `{{hosts_sync_cmds}}` | Generated from `CLUSTER_HOSTS_FILE` | Long grep-or-echo command chain |

### master_pubkey Usage

In the playbook, the step to distribute the master's public key looks like:

```yaml
- name: push master ssh key
  command: "mkdir -p ~/.ssh && echo '{{master_pubkey}}' >> ~/.ssh/authorized_keys && sort -u ~/.ssh/authorized_keys -o ~/.ssh/authorized_keys"
  sudo: false
  roles: [compute]
  depends_on: []
```

The `sort -u ... -o` at the end deduplicates the authorized_keys file in-place, so running this step twice doesn't accumulate duplicate entries.

### hosts_sync_cmds Expansion

Given `/etc/hosts` on the **master** (where `cexec` runs):

```
172.16.0.1    master
172.16.0.2    node1
172.16.0.3    node2
```

`buildHostsSyncCmds("/etc/hosts")` generates:

```
grep -qxF '172.16.0.1    master' /etc/hosts 2>/dev/null || echo '172.16.0.1    master' >> /etc/hosts ; grep -qxF '172.16.0.2    node1' /etc/hosts 2>/dev/null || echo '172.16.0.2    node1' >> /etc/hosts ; grep -qxF '172.16.0.3    node2' /etc/hosts 2>/dev/null || echo '172.16.0.3    node2' >> /etc/hosts
```

This runs on ALL cluster nodes (including master itself), ensuring every node has every other node in its `/etc/hosts`. The grep-before-echo pattern makes it safe to run multiple times.

### Why Not Use `envsubst` or Go `text/template`?

- **envsubst**: Requires the binary on target nodes; not always present
- **text/template**: Adds significant complexity for two variables; the `{{` delimiter conflicts with the template syntax if commands themselves contain `{`
- **Simple string replacement**: Zero dependencies, predictable, easy to debug

The current approach is trivially extensible: to add a new variable, add an entry to the `vars` map in `main.go` and document its `{{key}}` syntax.

---

## 9. SSH Authentication Flow

### Complete Auth Sequence

```
cexec binary (on dev/master machine)
    │
    ├─ buildSSHConfig(node)
    │       │
    │       ├─ try ~/.ssh/id_ed25519
    │       │       ├─ file exists + parseable → authMethods = [PublicKeys(signer)]
    │       │       └─ file missing/unparseable → try next
    │       │
    │       ├─ try ~/.ssh/id_rsa (fallback)
    │       │       ├─ file exists + parseable → authMethods = [PublicKeys(signer)]
    │       │       └─ file missing → no key auth
    │       │
    │       └─ node.Password != "" → authMethods += [Password(node.Password)]
    │
    ├─ net.DialTimeout("tcp", "host:port", timeout)
    │       └─ establishes TCP connection
    │
    ├─ ssh.NewClientConn(tcpConn, host, config)
    │       └─ SSH handshake; server presents host key
    │               └─ InsecureIgnoreHostKey(): accepted unconditionally
    │               └─ auth negotiation: tries authMethods in order
    │
    ├─ client.NewSession()
    │       └─ open SSH channel for command execution
    │
    └─ session.Run(command) or session.CombinedOutput(command)
```

### Sudo Wrapping

When `sudo=true` is set for a step:

**With password** (`sudoPass != ""`):

```bash
echo 'PASSWORD' | sudo -S sh -c "THE_ACTUAL_COMMAND"
```

- `-S`: read password from stdin
- `sh -c "..."`: execute command in a shell (handles pipes, redirects, semicolons in the command)
- Password is passed via stdin, not as a command-line argument, so it never appears in `ps aux`

**Without password** (NOPASSWD configured in sudoers):

```bash
sudo sh -c "THE_ACTUAL_COMMAND"
```

### Per-Node Password Override

Main.go password injection (happens after inventory load, before execution):

```go
for i := range nodes {
    // Check for per-node env var first (e.g., env var named "master" or "node1")
    if perNodePass := os.Getenv(nodes[i].Name); perNodePass != "" {
        nodes[i].Password = perNodePass
    } else if clusterPass != "" {
        nodes[i].Password = clusterPass
    }
}
```

This means:
- `CLUSTER_PASSWORD=secret cexec ...` → all nodes use "secret"
- `master=masterpass node1=node1pass cexec ...` → each node uses its own password
- `CLUSTER_PASSWORD=default master=override cexec ...` → master uses "override", others use "default"

---

## 10. Full Process Flow Walkthrough

### Command

```bash
cexec --playbook hpc-setup.yaml --auto-hosts /etc/hosts --env-file cluster.env
```

### Step-by-Step

```
Phase 1: Environment Setup
─────────────────────────
1. main() starts
2. Manually scan os.Args for "--env-file" → found "cluster.env"
3. loadEnvFile("cluster.env"):
   - Sets CLUSTER_PASSWORD, CLUSTER_USER, etc. as env vars
   - Skips keys already set in real environment
4. flag.Parse():
   - --playbook = "hpc-setup.yaml"
   - --auto-hosts = "/etc/hosts"
   - --timeout = envDefault("CLUSTER_TIMEOUT", "5m") = "5m"
   - --sudo = true (from flag)
   - etc.
5. Parse CLUSTER_PASSWORD from env → clusterPassword
6. Parse timeout string → time.Duration

Phase 2: Signal Handler
───────────────────────
7. Set up SIGINT/SIGTERM handler with cancel context
   - First signal: cancel()
   - Second signal: os.Exit(130)

Phase 3: Inventory Loading
──────────────────────────
8. --auto-hosts="/etc/hosts" is set → call hosts.LoadInventory("/etc/hosts", user, port)
9. Result: []Node{master, node1, node2, node3} (example: 4 nodes)
10. Inject passwords into nodes from CLUSTER_PASSWORD / per-node env vars
11. Apply --nodes selector and --exclude filter
    - Default: "all" nodes, no exclusions

Phase 4: Playbook Loading
─────────────────────────
12. playbook.Load("hpc-setup.yaml"):
    - Parse YAML
    - Validate: all steps have name+command, all depends_on refs exist
    - Return *Playbook with N steps
13. On validation error: print error, exit 1

Phase 5: State Loading
──────────────────────
14. state.Load(".cexec_state.json"):
    - If file exists: parse JSON into entries map
    - If not exists: empty state (normal for first run)
15. Print header:
    "Playbook: HPC Cluster Setup | Steps: 12 | Nodes: 4"

Phase 6: Template Variable Building
────────────────────────────────────
16. readMasterPubKey() → "ssh-ed25519 AAAAC3Nz..."
17. buildHostsSyncCmds("/etc/hosts") → long compound command
18. vars = {"master_pubkey": "...", "hosts_sync_cmds": "..."}

Phase 7: Playbook Orchestration
────────────────────────────────
19. Pre-compute stepIdx map: step name → array index
20. Pre-compute signals[]: one channel per step
21. Pre-compute pending[]: count of applicable nodes per step
22. Close channels for steps with zero applicable nodes
23. Create buffered resultsCh: capacity = nodes × steps
24. Launch printer goroutine (reads resultsCh, prints, accumulates)
25. Launch per-node goroutine × 4 (master, node1, node2, node3)
26. Each goroutine:
    for each step:
        wait for dependencies (select on signal channels)
        check role filter (skip if node not in step.Roles)
        check state cache (skip if hash found)
        execute via executor.RunSingle(ctx, node, opts)
        mark state, save state
        send NodeLog to resultsCh
        call signalDone(stepIndex)

Phase 8: Result Collection
──────────────────────────
27. After all node goroutines finish (WaitGroup.Wait()):
    close(resultsCh)
28. Printer goroutine's range loop terminates
29. Main goroutine waits on doneCh

Phase 9: Summary and Logging
──────────────────────────────
30. BuildSummary(allLogs) → Summary{Total:48, Succeeded:46, Failed:2, ...}
31. Print summary table
32. For each failed step: print step name, node, error, stdout/stderr
33. WriteLog("logs/", RunLog{...}) → "logs/run_20240315_143022_a3f9.json"

Phase 10: Exit
──────────────
34. If any failures: exit 1
35. If all success: exit 0
```

### State After Successful Run

```
.cexec_state.json     ← 48 entries (4 nodes × 12 steps)
logs/
  run_20240315_143022_a3f9.json   ← full run log
```

### Second Run (Idempotent)

All steps are in state cache → all 48 steps print "SKIP" → exits 0 in seconds.

---

## 11. Real Failures Encountered & How They Were Fixed

### Failure 1: No Go on Master Node

**Context**: Needed to build `cexec` but the HPC master node had no Go installation.

**Symptom**: `go: command not found`

**Root Cause**: The master node was a freshly installed Ubuntu server for compute workloads, not a development machine.

**Fix**: Cross-compile on the development machine:

```bash
GOOS=linux GOARCH=amd64 go build -o cexec ./cmd/cexec/
scp cexec hpc@172.16.0.1:~/
```

This produces a fully static binary (all Go dependencies compiled in) that runs on any Linux amd64 machine without any Go installation.

**Lesson**: Always plan for "binary runs on a machine that doesn't have the build tools." Go's cross-compilation support is a key advantage here.

---

### Failure 2: master→itself SSH Failed

**Context**: Running `cexec --nodes master` to test SSH connectivity.

**Symptom**: `ssh: connect to host master port 22: Connection refused` — even though SSH daemon was running.

**Root Cause**: Master's own public key was not in its `~/.ssh/authorized_keys`. When `cexec` tries to SSH to `172.16.0.1` (master's IP) from the master, the SSH server rejects the key.

**Fix** (one-time, run directly on master):

```bash
cat ~/.ssh/id_ed25519.pub >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
```

**Lesson**: `localhost` SSH auth is a separate concern from interactive login. A user can log in interactively but not SSH to themselves if authorized_keys isn't set up.

---

### Failure 3: Stale known_hosts Mismatch

**Context**: node1 was reinstalled with a fresh OS, generating a new host key.

**Symptom**: `ssh: handshake failed: ssh: host key mismatch`

**Root Cause**: The old host key for node1's IP was in `~/.ssh/known_hosts` on master. The new host key didn't match.

**Fix**: `cexec` uses `ssh.InsecureIgnoreHostKey()` — it accepts any host key without checking `known_hosts`. This is appropriate for a controlled lab environment where host key pinning isn't a security requirement.

**Production Alternative**: Use `golang.org/x/crypto/ssh/knownhosts`:

```go
knownHostsCallback, err := knownhosts.New(filepath.Join(homeDir, ".ssh", "known_hosts"))
config.HostKeyCallback = knownHostsCallback
```

**Lesson**: For lab/HPC use, `InsecureIgnoreHostKey` is pragmatic. For production, always pin host keys.

---

### Failure 4: master→node1 Permission Denied

**Context**: Running the full playbook for the first time.

**Symptom**: All steps on node1 failed with `ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain`

**Root Cause**: Master's public key was not in node1's `~/.ssh/authorized_keys`. The "push master ssh key" step was designed to fix this, but it requires an initial connection — chicken-and-egg.

**Fix**: From the **development machine** (which had password access to all nodes), manually push the master's key:

```bash
# On dev machine:
ssh-copy-id -i /path/to/master_key.pub hpc@node1
ssh-copy-id -i /path/to/master_key.pub hpc@node2
# ...
```

Or use `--sudo` with `CLUSTER_PASSWORD` set, if password authentication was enabled on the nodes.

**Lesson**: Key distribution must be bootstrapped manually or via an initial password-auth step. Document this as a prerequisite.

---

### Failure 5: --dry-run Silently Ignored in Playbook Mode

**Context**: Running `cexec --playbook hpc-setup.yaml --dry-run`.

**Symptom**: The `--dry-run` flag was correctly documented but the playbook runner (`runPlaybook`) was called regardless, executing real SSH commands.

**Root Cause**: The dry-run check was only in the code path for single-command execution (`cexec --cmd "..." --dry-run`). The playbook path had no dry-run gate.

**Fix**: Added explicit check in main.go before calling `runPlaybook`:

```go
if *dryRun {
    dryRunPlaybook(pb, filteredNodes, vars)
    return
}
runPlaybook(ctx, pb, filteredNodes, st, vars, opts, *logDir, *quiet)
```

And implemented `dryRunPlaybook()` with the same role-filtering logic as the real runner.

**Lesson**: Feature flags applied to one code path must be consciously applied to all code paths. A separate `dryRunPlaybook` function with its own logic (rather than a conditional inside `runPlaybook`) is cleaner.

---

### Failure 6: dpkg Lock Contention on Node1

**Context**: `apt-get install openmpi-bin` failing intermittently on node1.

**Symptom**:

```
E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 1234 (unattended-upgrad)
```

**Root Cause**: Ubuntu's `unattended-upgrades` service runs `apt` in the background to install security updates. It held the dpkg lock exactly when `cexec` tried to install packages.

**Fix — Two-part**:

1. Added "disable unattended-upgrades" as an early playbook step:

```yaml
- name: disable unattended upgrades
  command: "systemctl disable --now unattended-upgrades"
  sudo: true
  roles: [all]
  depends_on: []
```

2. Consolidated multiple `apt install` calls into single calls per role to minimize the lock acquisition window:

```yaml
# Before (bad — two lock acquisitions):
- name: install mpi
  command: "apt-get install -y openmpi-bin libopenmpi-dev"
- name: install build tools
  command: "apt-get install -y gcc make"

# After (good — one lock acquisition):
- name: install packages
  command: "apt-get install -y openmpi-bin libopenmpi-dev gcc make"
```

**Lesson**: Any step that requires holding a system-level lock should be aware of competing processes. Disabling automatic updates early in setup is a standard HPC cluster practice.

---

### Failure 7: NFS Mount "Resource Temporarily Unavailable"

**Context**: The "mount nfs share" step on node1 failed after apparent success of all prior steps.

**Symptom**: `mount: /shared: mounting master:/shared failed, reason given by server: Resource temporarily unavailable`

**Root Cause — Two separate issues**:

(a) **DNS resolution**: node1's `/etc/hosts` was missing the `master` entry. `mount -t nfs master:/shared /shared` couldn't resolve `master` to an IP. `cexec` was running the "sync cluster hosts" step, but it ran concurrently with the mount step being attempted.

*Actually*: The real issue was dependency ordering — "mount nfs share" had `depends_on: [export nfs share]` but "sync cluster hosts" was not in `depends_on`, so on node1, mount was attempted before hosts were synced.

**Fix for (a)**: Added "sync cluster hosts" to `depends_on` for all steps that require hostname resolution.

(b) **rpcbind not registered**: After fresh nfs-server installation, the NFS daemon registers with rpcbind. But on some Ubuntu versions, this registration doesn't happen until after a service restart.

**Fix for (b)**: Added explicit restart in the playbook:

```yaml
- name: restart nfs server
  command: "systemctl restart nfs-kernel-server"
  sudo: true
  roles: [control]
  depends_on: [export nfs share]
```

And the compute nodes' `depends_on` points to "restart nfs server" rather than "export nfs share".

**Lesson**: NFS setup is sensitive to ordering and service state. Always restart NFS after configuration changes. Always ensure `/etc/hosts` is synced before any step that relies on hostname resolution.

---

### Failure 8: Unicode Box-Drawing Characters in Comments

**Context**: During development, trying to edit `main.go` using text-editing tools.

**Symptom**: Edit operations failed because the file contained Unicode box-drawing characters (─, │, ┌, etc.) in ASCII art diagrams within comments.

**Root Cause**: Some editing tools operate on byte offsets and mishandle multi-byte UTF-8 characters in comments when trying to match exact strings.

**Fix**: Used Python byte-string replacement as a workaround:

```bash
python3 -c "
with open('main.go', 'rb') as f: data = f.read()
data = data.replace(b'\xe2\x94\x80', b'-')  # ─ → -
with open('main.go', 'wb') as f: f.write(data)
"
```

**Lesson**: Keep source code comments ASCII-only if the editing toolchain isn't guaranteed to be UTF-8 aware. ASCII art using `-`, `|`, `+` is universally safe.

---

### Failure 9: `hosts.IsClusterHost` Not Exported

**Context**: Writing `buildHostsSyncCmds` in `main.go`.

**Symptom**: Compile error: `undefined: hosts.isClusterHost` (capitalization mismatch).

**Root Cause**: The function was originally named `isClusterHost` (lowercase, unexported). `main.go` needed to call it to filter cluster lines from the hosts file.

**Fix**: Renamed to `IsClusterHost` (exported) in `internal/hosts/hosts.go`. Go's visibility rules require uppercase first letter for exported identifiers.

**Lesson**: When designing `internal` packages, consider which identifiers will be called from other packages (even within the same module's `cmd/` or other `internal/` packages). Export them proactively.

---

### Failure 10: Sequential Execution Too Slow

**Context**: Early implementation processed nodes one at a time, sequentially.

**Symptom**: On a 10-node cluster with a 15-step playbook, full setup took ~45 minutes. Each step averaged ~30 seconds, and nodes were not parallelized.

**Root Cause**: Original design: `for each node { for each step { execute } }` — O(nodes × steps) sequential time.

**Fix**: Redesigned to the channel-based concurrent model described in Section 6:
- One goroutine per node
- Dependency signaling via channels
- Nodes run in parallel; dependencies enforce ordering only when needed

**Result**: On the same 10-node cluster, setup time dropped to ~8 minutes (close to the time for the slowest single node).

**Lesson**: Concurrency is the core value proposition of `cexec`. The sequential design was a prototype; the channel-based design is the production architecture.

---

## 12. Which Files Do You Actually Need?

This is the most common source of confusion. Here is the definitive answer:

### The Minimum to Run cexec

You need **exactly three things**:

1. The `cexec` binary (compiled from source or downloaded)
2. A way to tell it what nodes exist (either `cluster.env` with `CLUSTER_HOSTS_FILE`, or `inventory.yaml`)
3. A command to run (either `-- hostname` style, or `--playbook somefile.yaml`)

That's it. Everything else is optional.

---

### File-by-File: What Is It, Do You Need It?

#### `cluster.env` — **Strongly recommended, not required**
- **What**: A plain text `KEY=VALUE` config file. Loaded automatically if it exists in the working directory.
- **What it does**: Sets defaults for password, hosts file path, playbook, log dir, state file. Saves you from typing long flags every run.
- **Do you need it?** No — you can pass everything as CLI flags instead. But without it you must type `--auto-hosts /etc/hosts` and the password would have no way to be set without this file or real env vars.
- **Gitignored?** Yes. Never commit it — it contains your password.
- **Template**: Copy `cluster.env.example` and fill in your values.

Example without cluster.env (everything as flags):
```bash
CLUSTER_PASSWORD=spring ./cexec --auto-hosts /etc/hosts --playbook hpc-setup.yaml
```
Example with cluster.env (just this):
```bash
./cexec --playbook hpc-setup.yaml
```

---

#### `inventory.yaml` — **Optional when `CLUSTER_HOSTS_FILE` is set**
- **What**: A YAML file explicitly listing every node (name, IP, user, port, groups).
- **What it does**: Tells cexec which machines exist and how to reach them.
- **Do you need it?** **No, if** `CLUSTER_HOSTS_FILE=/etc/hosts` is set in `cluster.env`. In that case cexec reads your `/etc/hosts` and auto-discovers `master`, `node1`, `node2`, etc. You never touch `inventory.yaml`.
- **Do you need it?** **Yes, if** you don't set `CLUSTER_HOSTS_FILE`, OR if your node names don't follow the `master`/`nodeN` pattern, OR if nodes have different SSH users/ports.
- **Priority**: `--auto-hosts` (or `CLUSTER_HOSTS_FILE`) always wins over `inventory.yaml`.

Decision guide:
```
Nodes are named master/node1/node2... and all have same SSH user/port?
  → Set CLUSTER_HOSTS_FILE=/etc/hosts in cluster.env
  → inventory.yaml not needed

Nodes have custom names (web1, db-primary, cache-01) or different ports/users?
  → Use inventory.yaml
  → Don't set CLUSTER_HOSTS_FILE
```

---

#### `hpc-setup.yaml` (or any `*.yaml` playbook) — **Only needed for multi-step setup**
- **What**: A YAML file defining an ordered list of steps to run across nodes.
- **What it does**: Lets you define complex multi-step workflows with role filtering (`roles: [control]`), dependency ordering (`depends_on:`), and idempotent re-runs via caching.
- **Do you need it?** Only for playbook mode (`--playbook`). For single ad-hoc commands (`./cexec -- hostname`) you don't need any playbook file.
- **Can you write your own?** Yes — `hpc-setup.yaml` is just an example. Write any YAML following the step format for your own use case.

---

#### `cluster.env.example` — **Reference only, never used directly**
- A documentation file showing what `cluster.env` can contain.
- cexec never reads this file. Copy it to `cluster.env` and edit it.
- Safe to commit to git (no real values).

---

#### `.cexec_state.json` — **Auto-created, never edit manually**
- Created automatically on first successful run.
- Stores the SHA256 hash of every completed step so re-runs skip them.
- Delete it to reset all caching: `rm .cexec_state.json`
- Gitignored.

---

#### `logs/` directory — **Auto-created, contains failure reports**
- Created automatically when a step fails.
- Each failed run writes a JSON file: `logs/run_<timestamp>_<random>.json`
- Contains stdout, stderr, exit code, duration for every node on that step.
- Gitignored.

---

### The Go Module Name (`github.com/aarctanz/cexec`)

This appears in `go.mod` and in every `import` statement across the codebase. **It does not mean the code is fetched from GitHub.** This is just the module's canonical name — Go uses it as a namespace to distinguish this project's internal packages from external dependencies.

When Go sees:
```go
import "github.com/aarctanz/cexec/internal/ssh"
```
It checks: does this import path start with the current module name (`github.com/aarctanz/cexec`)? Yes → resolve it **locally** at `./internal/ssh/`. No network call is made.

Only `golang.org/x/crypto` and `gopkg.in/yaml.v3` are fetched from the internet (they are real external libraries in `go.sum`).

To rename the module to match your own GitHub account before pushing:
```bash
# Replace all occurrences in Go source files
find . -name "*.go" | xargs sed -i 's|github.com/aarctanz/cexec|github.com/YOURUSERNAME/cexec|g'
# Update go.mod
sed -i 's|github.com/aarctanz/cexec|github.com/YOURUSERNAME/cexec|g' go.mod
# Verify nothing broke
go build ./...
```

---

## 12. Configuration Reference

### cluster.env File

Create from the template: `cp cluster.env.example cluster.env`

```bash
# cluster.env — values here are defaults; real env vars take precedence

# SSH/sudo password for all nodes (required if not using key auth)
CLUSTER_PASSWORD=

# Path to /etc/hosts or similar file for auto inventory generation
# When set, --auto-hosts uses this path if not provided on CLI
CLUSTER_HOSTS_FILE=

# Default SSH user for all nodes
CLUSTER_USER=hpc

# Default playbook path (used if --playbook not specified)
CLUSTER_PLAYBOOK=

# Directory for JSON run logs
CLUSTER_LOG_DIR=logs

# Path for state cache file
CLUSTER_STATE_FILE=.cexec_state.json
```

### Per-Node Password Override

Set environment variables named after the node:

```bash
export master=differentpassword
export node1=anotherpassword
export CLUSTER_PASSWORD=defaultpassword
cexec --playbook hpc-setup.yaml
# master gets "differentpassword", node1 gets "anotherpassword", rest get "defaultpassword"
```

### All CLI Flags

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--playbook` | `CLUSTER_PLAYBOOK` | — | Path to playbook YAML |
| `--inventory` | — | `inventory.yaml` | Path to manual inventory YAML |
| `--auto-hosts` | `CLUSTER_HOSTS_FILE` | — | Path to hosts file for auto-inventory |
| `--hosts-user` | `CLUSTER_USER` | `hpc` | SSH user for auto-inventory nodes |
| `--env-file` | — | — | Path to env file (loaded before flag parsing) |
| `--nodes` | — | `all` | Node selector: "all", group name, or comma-separated names |
| `--exclude` | — | — | Comma-separated node names to exclude |
| `--cmd` | — | — | Single command to run (no playbook) |
| `--sudo` | — | `false` | Run command with sudo |
| `--timeout` | `CLUSTER_TIMEOUT` | `5m` | Per-command timeout |
| `--concurrency` | — | `0` (unlimited) | Max concurrent SSH connections |
| `--retries` | — | `0` | Max retry attempts on transient failures |
| `--backoff` | — | `2s` | Base backoff duration |
| `--backoff-fixed` | — | `false` | Use fixed backoff instead of exponential |
| `--log-dir` | `CLUSTER_LOG_DIR` | `logs` | Directory for JSON run logs |
| `--state-file` | `CLUSTER_STATE_FILE` | `.cexec_state.json` | State cache file path |
| `--dry-run` | — | `false` | Print what would run without executing |
| `--force` | — | `false` | Ignore state cache; re-run all steps |
| `--quiet` | — | `false` | Suppress per-step output (only show summary) |

### Priority Order

```
CLI flag value
    ↓ (if not provided)
Environment variable (real env)
    ↓ (if not set)
cluster.env file value
    ↓ (if not in file)
Hardcoded default in main.go
```

### inventory.yaml Format

```yaml
nodes:
  - name: master
    host: 172.16.0.1
    user: hpc
    port: 22
    groups:
      - control
  - name: node1
    host: 172.16.0.2
    user: hpc
    # port defaults to 22
    groups:
      - compute
  - name: node2
    host: 172.16.0.3
    user: hpc
    groups:
      - compute
```

Note: `password` field is intentionally absent — passwords come from environment, never from files.

### playbook.yaml Format

```yaml
name: HPC Cluster Setup

steps:
  - name: disable unattended upgrades
    command: "systemctl disable --now unattended-upgrades || true"
    sudo: true
    roles:
      - all
    depends_on: []

  - name: sync cluster hosts
    command: "{{hosts_sync_cmds}}"
    sudo: true
    roles:
      - all
    depends_on:
      - disable unattended upgrades

  - name: update apt cache
    command: "apt-get update -qq"
    sudo: true
    roles:
      - all
    depends_on:
      - sync cluster hosts

  - name: install nfs server
    command: "apt-get install -y nfs-kernel-server"
    sudo: true
    roles:
      - control
    depends_on:
      - update apt cache

  - name: install nfs client
    command: "apt-get install -y nfs-common"
    sudo: true
    roles:
      - compute
    depends_on:
      - update apt cache

  - name: push master ssh key
    command: "mkdir -p ~/.ssh && echo '{{master_pubkey}}' >> ~/.ssh/authorized_keys && sort -u ~/.ssh/authorized_keys -o ~/.ssh/authorized_keys"
    sudo: false
    roles:
      - compute
    depends_on: []

  - name: create nfs export directory
    command: "mkdir -p /shared && chown nobody:nogroup /shared && chmod 777 /shared"
    sudo: true
    roles:
      - control
    depends_on:
      - install nfs server

  - name: configure nfs exports
    command: "grep -qxF '/shared *(rw,sync,no_subtree_check)' /etc/exports || echo '/shared *(rw,sync,no_subtree_check)' >> /etc/exports"
    sudo: true
    roles:
      - control
    depends_on:
      - create nfs export directory

  - name: export nfs share
    command: "exportfs -ra && systemctl enable --now nfs-kernel-server && systemctl restart nfs-kernel-server"
    sudo: true
    roles:
      - control
    depends_on:
      - configure nfs exports

  - name: mount nfs share
    command: "mkdir -p /shared && (mountpoint -q /shared || mount -t nfs master:/shared /shared)"
    sudo: true
    roles:
      - compute
    depends_on:
      - export nfs share
      - sync cluster hosts

  - name: install openmpi
    command: "apt-get install -y openmpi-bin libopenmpi-dev"
    sudo: true
    roles:
      - all
    depends_on:
      - update apt cache
```

---

## 13. The HPC Playbook (hpc-setup.yaml)

**File**: `hpc-setup.yaml`

This is the production playbook used to set up the Beowulf cluster. Key design decisions in this playbook:

### Step Ordering and Dependencies

The dependency graph (simplified):

```
disable-upgrades ──────────────────────────────────────► (no dependents)
        │
        ▼
sync-cluster-hosts ──────────────────────────────────► mount-nfs-share (compute)
        │
        ▼
update-apt-cache ──────────────────────────────────────► install-openmpi (all)
        │
        ├──────► install-nfs-server (control)
        │                │
        │                ▼
        │         create-export-dir ──► configure-exports ──► export-nfs-share ──► mount-nfs-share
        │
        └──────► install-nfs-client (compute)

push-master-key (no deps, runs immediately)
```

### `|| true` Pattern

Many steps use `|| true` to prevent failure from blocking the playbook:

```yaml
command: "systemctl disable --now unattended-upgrades || true"
```

If `unattended-upgrades` is not installed, `systemctl disable` returns non-zero. Without `|| true`, this fails the step. With `|| true`, the command always exits 0. This is appropriate for idempotent setup steps where "already done" is not an error.

### `mountpoint -q` Pattern

```yaml
command: "mkdir -p /shared && (mountpoint -q /shared || mount -t nfs master:/shared /shared)"
```

- `mountpoint -q /shared`: exits 0 if `/shared` is already mounted, non-zero if not
- `|| mount ...`: only runs mount if not already mounted
- `mkdir -p /shared`: creates mount point if it doesn't exist

This makes the mount step idempotent — safe to run multiple times.

---

## 14. Future Scope

### 1. Cycle Detection in depends_on

**Problem**: If Step A has `depends_on: [B]` and Step B has `depends_on: [A]`, both goroutines block forever on each other's signal channel. This is a deadlock.

**Solution**: Implement topological sort (Kahn's algorithm or DFS-based) at `playbook.Load()` time:

```go
// Kahn's algorithm
func detectCycle(steps []Step) error {
    inDegree := make(map[string]int)
    adj := make(map[string][]string)

    for _, step := range steps {
        inDegree[step.Name] = 0
    }
    for _, step := range steps {
        for _, dep := range step.DependsOn {
            adj[dep] = append(adj[dep], step.Name)
            inDegree[step.Name]++
        }
    }

    queue := []string{}
    for name, deg := range inDegree {
        if deg == 0 { queue = append(queue, name) }
    }

    visited := 0
    for len(queue) > 0 {
        node := queue[0]; queue = queue[1:]
        visited++
        for _, next := range adj[node] {
            inDegree[next]--
            if inDegree[next] == 0 { queue = append(queue, next) }
        }
    }

    if visited != len(steps) {
        return fmt.Errorf("cycle detected in depends_on graph")
    }
    return nil
}
```

### 2. `{{hosts_sync_cmds}}` Content Hash Caching

**Problem**: `{{hosts_sync_cmds}}` is regenerated from the hosts file on every run. If the hosts file hasn't changed, the generated command is identical, but since `expandVars` is called at runtime, the hash always matches the current command. If the hosts file HAS changed (new node added), the hash changes and all nodes re-sync — which is correct.

The current behavior is already correct. The "future scope" here is: add a separate hash of the hosts file itself to the state, and show a warning when re-running if the hosts file hash differs from the last run. This would help operators understand WHY certain steps are re-running.

### 3. Windows/macOS Target Node Support

Add `shell` field to Step:

```yaml
- name: install package
  command: "winget install openmpi"
  shell: powershell
  roles: [windows-nodes]
```

In `ssh/client.go`, the command wrapping would need to detect shell type:

```go
if shell == "powershell" {
    cmd = "powershell -Command \"" + cmd + "\""
} else {
    cmd = "sh -c \"" + cmd + "\""  // Unix default
}
```

### 4. Step-Level Retry Config

Add `retries` and `backoff` to the Step struct:

```yaml
- name: apt update
  command: "apt-get update"
  retries: 3
  backoff: 5s
```

The playbook runner would override the global `opts.MaxRetries` and `opts.BackoffBase` per step.

### 5. Conditional Steps

```yaml
- name: install gpu drivers
  command: "apt install nvidia-driver-535"
  when: "nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null | grep -q GPU"
```

Implementation: before executing the main command, run the `when` command on the node. If it exits non-zero, skip the step (don't count it as a failure). This would require a preliminary SSH command execution for each step with a `when` clause.

### 6. Terminal UI (TUI)

Replace line-by-line output with a live terminal UI using a library like `bubbletea` or `tcell`:

```
╔══════════════════════════════════════════════════════╗
║ cexec — HPC Cluster Setup                  [12/12s] ║
╠══════════════════════════════════════════════════════╣
║ Step: install openmpi                               ║
╠═══════════════╦══════════════════════════════════════╣
║ master        ║ [DONE] sync hosts, apt update, mpi  ║
║ node1         ║ [RUN ] openmpi ████████░░░░░ 62%     ║
║ node2         ║ [RUN ] openmpi █████░░░░░░░ 45%      ║
║ node3         ║ [WAIT] export nfs share              ║
╚═══════════════╩══════════════════════════════════════╝
```

### 7. Vault/Secrets Manager Integration

Replace `CLUSTER_PASSWORD` in `cluster.env` with a Vault lookup:

```yaml
# cluster.env
CLUSTER_PASSWORD_SOURCE=vault://secret/hpc/cluster_password
```

In `main.go`, detect the `vault://` prefix and call the Vault API to retrieve the secret at startup.

### 8. SSH Bastion/Jump Host Support

Add `bastion` field to inventory:

```yaml
nodes:
  - name: node1
    host: 10.0.0.1       # not directly reachable
    bastion: 172.16.0.1  # jump through this host
```

In `ssh/client.go`, implement jump host:

```go
bastionConn, _ := ssh.Dial("tcp", bastion+":22", bastionConfig)
nodeConn, _ := bastionConn.Dial("tcp", node.Host+":22")
client, _ := ssh.NewClient(nodeConn, node.Host, nodeConfig)
```

### 9. Multiple Playbook Composition

```yaml
name: Full HPC Setup

include:
  - base-setup.yaml
  - nfs-setup.yaml
  - mpi-setup.yaml
```

Implemented by flattening included playbooks into a single steps list at `Load()` time, with namespace prefixing for `depends_on` references.

### 10. Node Health Checks

Before running any step, attempt a lightweight SSH connection (or TCP ping) to each node:

```go
for _, node := range nodes {
    go func(n Node) {
        conn, err := net.DialTimeout("tcp", n.Host+":"+port, 3*time.Second)
        if err != nil {
            unreachable <- n
        } else {
            conn.Close()
            reachable <- n
        }
    }(node)
}
```

Unreachable nodes are marked and excluded from execution (but reported in summary as "Unreachable"). This prevents goroutines from hanging on SSH dial timeouts.

### 11. Parallel Step Execution Within a Node

Currently each node goroutine runs its steps sequentially (step 0, then step 1, etc.). If two consecutive steps have no dependency relationship between them, they could run concurrently even on the same node.

This would require building a per-node dependency DAG and using a worker pool per node. Significantly increases complexity; only worth implementing for playbooks with many independent long-running steps.

### 12. Inventory Groups with Variables

```yaml
groups:
  compute:
    vars:
      nfs_server: master
      shared_dir: /shared
```

Steps could reference `{{group.nfs_server}}` which expands per the group of the node executing the step. Enables writing role-specific configuration without duplicating steps.

### 13. `--tags` Filtering

```yaml
- name: install openmpi
  tags: [mpi, packages]
  command: "apt-get install -y openmpi-bin"
```

```bash
cexec --playbook hpc-setup.yaml --tags mpi
# Only runs steps tagged "mpi"

cexec --playbook hpc-setup.yaml --skip-tags packages
# Skips steps tagged "packages"
```

### 14. Native Sudo Password Prompting

Instead of requiring `CLUSTER_PASSWORD` in cluster.env, prompt interactively:

```go
fmt.Print("Sudo password: ")
bytePassword, _ := term.ReadPassword(int(syscall.Stdin))
password := string(bytePassword)
```

Using `golang.org/x/term` for terminal-aware password reading (no echo). This avoids storing passwords in files entirely.

---

## 15. Replication Guide

This section describes how to rebuild `cexec` from scratch using this document as the sole reference.

### Prerequisites

- Go 1.21+
- A Linux machine with SSH access to target nodes
- Target nodes running Ubuntu 20.04+ (for the HPC playbook)

### Module Setup

```bash
mkdir cexec && cd cexec
go mod init github.com/yourusername/cexec
go get golang.org/x/crypto/ssh
go get gopkg.in/yaml.v3
```

### Build Order (Dependency-Safe)

Build packages in this order (each package only depends on previously built ones):

1. `internal/errors/classify.go` — no internal deps
2. `internal/inventory/inventory.go` — no internal deps
3. `internal/ssh/client.go` — depends on `inventory`, `errors`
4. `internal/logging/logger.go` — depends on `ssh`, `errors`
5. `internal/executor/executor.go` — depends on `ssh`, `logging`, `errors`, `inventory`
6. `internal/hosts/hosts.go` — depends on `inventory`
7. `internal/state/state.go` — no internal deps (uses only stdlib)
8. `internal/playbook/playbook.go` — no internal deps
9. `cmd/cexec/main.go` — depends on everything

### Key Implementation Checklist

When replicating, ensure these non-obvious details are implemented correctly:

- [ ] `Password` field in Node struct has `yaml:"-" json:"-"` tags
- [ ] `loadEnvFile` is called from a manual `os.Args` scan, BEFORE `flag.Parse()`
- [ ] State file uses atomic write (`.tmp` + `os.Rename`)
- [ ] `state.Save()` is called after EVERY step, not just at the end
- [ ] `pending[i]` counts only nodes that pass the role filter (not all nodes)
- [ ] Steps with `pending[i] == 0` have their signal channel closed immediately
- [ ] `signalDone` checks `pending[i] == 0` under mutex before closing channel
- [ ] `computeBackoff` caps exponential backoff at 60 seconds
- [ ] Sudo command wraps as `echo 'PASS' | sudo -S sh -c "CMD"` (stdin pipe for password)
- [ ] Context cancellation sends `ssh.SIGKILL` to the remote session
- [ ] `hosts.IsClusterHost` is exported (uppercase I) for use from `main.go`
- [ ] `expandVars` is called on the command BEFORE computing the state hash
- [ ] Both `--dry-run` checks exist: one for single-command mode, one for playbook mode

### Compilation and Deployment

```bash
# Development build
go build -o cexec ./cmd/cexec/

# Production build (optimized, stripped)
go build -ldflags="-s -w" -o cexec ./cmd/cexec/

# Cross-compile for remote Linux amd64 target
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o cexec-linux-amd64 ./cmd/cexec/

# Deploy to master node
scp cexec-linux-amd64 hpc@172.16.0.1:~/cexec
ssh hpc@172.16.0.1 chmod +x ~/cexec
```

### Quick Test Sequence

```bash
# 1. Verify SSH connectivity (dry run on all nodes)
./cexec --auto-hosts /etc/hosts --cmd "hostname" --dry-run

# 2. Test actual SSH (run hostname on all nodes)
./cexec --auto-hosts /etc/hosts --cmd "hostname"

# 3. Dry run full playbook
./cexec --playbook hpc-setup.yaml --auto-hosts /etc/hosts --dry-run

# 4. Run full playbook
./cexec --playbook hpc-setup.yaml --auto-hosts /etc/hosts --env-file cluster.env

# 5. Verify idempotency (second run should all skip)
./cexec --playbook hpc-setup.yaml --auto-hosts /etc/hosts --env-file cluster.env

# 6. Force full re-run (ignores state)
./cexec --playbook hpc-setup.yaml --auto-hosts /etc/hosts --env-file cluster.env --force
```

---

*End of documentation. This document covers the complete architecture and implementation of `cexec` as of March 2026. All file paths are relative to the project root (`cexec/`). For any implementation questions not answered here, the source of truth is the source code in `cmd/cexec/main.go` and the `internal/` packages.*
