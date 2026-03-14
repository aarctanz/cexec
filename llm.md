# cexec

cexec is a lightweight Go tool that runs commands on multiple cluster nodes from a master node.

It is built for lab HPC environments where the same setup and fix commands must be executed across many machines, while still making it easy to detect which nodes failed differently and need manual attention.

## Problem Statement

While setting up and maintaining a small HPC cluster, many operations must be repeated on all nodes:
- setting up passwordless SSH,
- installing the same software stack,
- applying common fixes,
- checking service state,
- validating configuration.

Doing this manually is slow and error-prone.

In practice:
- many nodes fail with the same issue and can be fixed with the same command,
- some nodes fail with different issues and require manual intervention,
- the operator needs a clear record of what happened on every node.

The tool should let the master node execute the same command on all selected nodes in parallel, collect per-node output, classify failures, and write structured logs for later review.

## Goal

Build a Go-based CLI tool that:
- runs from the master node,
- connects to worker nodes over SSH,
- executes commands on all selected nodes in parallel,
- treats each node execution as independent,
- shows per-node output clearly,
- stores execution results in JSON logs,
- optionally retries failed executions with backoff.

This is not a provisioning framework or workflow engine.

This is a fast, reliable remote execution tool for cluster setup and maintenance.

## Core Guarantees

### 1. Parallel and independent execution
- Every target node execution must be independent.
- Failure on one node must not block or cancel execution on other nodes by default.
- All selected nodes should be processed in parallel, with optional concurrency control if needed.

### 2. Per-node visibility
- The operator must be able to see output for each node separately.
- stdout and stderr must be captured independently.
- The result for each node must include status, exit code, timing, and error classification.

### 3. Structured logging
- Every run must be logged to a JSON file.
- Logs must be reviewable after execution.
- Logs should preserve both raw errors and normalized error categories.

### 4. Optional retry with backoff
- Retry must be disabled by default.
- If enabled by flags, failed node executions should be retried independently.
- Retry count and backoff policy must be configurable.

### 5. Selective targeting
- The tool must support running against:
  - all nodes,
  - named groups,
  - explicit node lists.
- The tool must also support excluding selected nodes through a flag.

### 6. Sudo support
- Commands may optionally be executed with sudo.
- The tool must support remote sudo execution when requested.
- Credentials must never be printed in logs or terminal output.

## Non-Goals

V1 does **not** need:
- resume support,
- multi-step workflow execution,
- playbooks,
- remote agents,
- GUI dashboards,
- full configuration management,
- package orchestration,
- provisioning logic.

V1 should remain focused on one-shot command execution.

## Functional Requirements

### Command execution
- Execute a single shell command on multiple nodes from the master node.
- Support execution on all selected nodes in parallel.
- Allow optional concurrency limit through a flag.
- Allow optional per-command timeout.

### Target selection
- Load target nodes from an inventory file.
- Support group-based selection.
- Support explicit node selection.
- Support excluding nodes with a dedicated flag.

Example:
- run on `all`,
- run on `compute`,
- run on `node01,node02,node05`,
- run on `all` except `node03,node07`.

### Output handling
- Show output for every node separately.
- Capture:
  - stdout,
  - stderr,
  - exit code,
  - start time,
  - end time,
  - duration.
- Output should remain readable even when executions happen in parallel.

### Failure handling
- The tool must not stop the entire run because one node fails.
- Each node result must be recorded independently.
- The final summary should clearly show:
  - total targeted nodes,
  - successful nodes,
  - failed nodes,
  - unreachable nodes,
  - timed out nodes,
  - retried nodes.

### Retry and backoff
- Default behavior: no retry.
- If retry is enabled:
  - only failed node executions should be retried,
  - each node retry cycle should be independent,
  - retry count should be configurable,
  - backoff strategy should be configurable.
- Initial supported backoff can be simple exponential backoff or fixed delay.

### Sudo execution
- Support a `--sudo` mode.
- Commands executed with sudo should work for software installation and administrative fixes.
- Sudo password handling must be secure.
- Secrets must not appear in:
  - console output,
  - JSON logs,
  - debug logs.

## Error Model

The tool should not claim to understand every possible Linux error message semantically.

Instead, it should classify execution outcomes into clear operational categories while still preserving the raw message.

### Required error categories
At minimum, classify failures as:

- `ssh_connection_failed`
- `ssh_auth_failed`
- `host_unreachable`
- `dns_resolution_failed`
- `connection_timeout`
- `command_timeout`
- `sudo_auth_failed`
- `sudo_permission_denied`
- `remote_command_failed`
- `shell_not_found`
- `binary_not_found`
- `non_zero_exit`
- `unknown_error`

### Important rule
- Always store the raw error text.
- Also store a normalized error category.
- If classification is uncertain, use `unknown_error`.

This gives both machine-readable reporting and human-readable debugging.

## Inventory Requirements

The tool should read nodes from a simple inventory file.

Each node entry may include:
- name,
- hostname or IP,
- SSH user,
- SSH port,
- group or tags.

Example inventory structure:

```yaml
nodes:
  - name: master
    host: 192.168.1.10
    user: lab
    port: 22
    groups: [control]

  - name: node01
    host: 192.168.1.11
    user: lab
    port: 22
    groups: [compute]

  - name: node02
    host: 192.168.1.12
    user: lab
    port: 22
    groups: [compute]
```
### Sudo password handling

In controlled local cluster environments, cexec may read the sudo password
from an environment variable on the master node, for example:

`node1=password`

This is supported as a convenience mechanism for lab use, not as a strong secret
management solution.

Rules:
- The password must never be stored in source code or inventory files.
- The password must never be printed in console output or logs.
- The password should be provided only for the current execution session.
- Inventory must never contain plaintext sudo passwords.
