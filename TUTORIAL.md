# cexec Tutorial: Hands-On Guide to Parallel SSH Execution

`cexec` is a Go CLI tool that SSHes into multiple Linux servers simultaneously and runs commands on all of them concurrently. It handles both one-off ad-hoc commands and multi-step YAML playbooks with dependency ordering and hash-based caching. This tutorial walks you through real scenarios from first boot to production fleet management.

---

## Table of Contents

1. [Build and Install](#build-and-install)
2. [Tutorial 1: Your First Run — 2 Node Cluster](#tutorial-1-your-first-run--2-node-cluster)
3. [Tutorial 2: Testing Without Running Anything (--dry-run)](#tutorial-2-testing-without-running-anything----dry-run)
4. [Tutorial 3: Re-running After a Failure (--force and Caching)](#tutorial-3-re-running-after-a-failure----force-and-caching)
5. [Tutorial 4: Adding Node 3 Through Node 20](#tutorial-4-adding-node-3-through-node-20)
6. [Tutorial 5: Running Ad-hoc Commands](#tutorial-5-running-ad-hoc-commands)
7. [Tutorial 6: Writing Your Own Playbook](#tutorial-6-writing-your-own-playbook)
8. [Tutorial 7: Role-based Steps](#tutorial-7-role-based-steps)
9. [Tutorial 8: Debugging Failures](#tutorial-8-debugging-failures)
10. [Tutorial 9: Concurrency and Performance](#tutorial-9-concurrency-and-performance)
11. [Tutorial 10: Beyond HPC — General Use Cases](#tutorial-10-beyond-hpc--general-use-cases)
12. [Tips and Tricks](#tips-and-tricks)
13. [Troubleshooting](#troubleshooting)

---

## Build and Install

### Prerequisites

- Go 1.21 or later (`go version` to check)
- SSH access to your target servers (password or key-based)
- Git

### Step 1: Clone the Repository

```bash
git clone <repo-url>
cd cexec
```

### Step 2: Build for Your Local Machine

```bash
go build -o cexec ./cmd/cexec/
```

This produces a single binary called `cexec` in the current directory. No runtime dependencies, no package manager, no virtual environment — it is a self-contained Go binary.

### Step 3: Cross-Compile for Remote Linux Servers

If you are on macOS or Windows but your servers run Linux AMD64, build a Linux binary:

```bash
GOOS=linux GOARCH=amd64 go build -o cexec-linux-amd64 ./cmd/cexec/
```

Go's cross-compilation is built in. You just set two environment variables and the compiler does the rest. Common targets:

| Target | GOOS | GOARCH |
|---|---|---|
| Linux 64-bit (most servers) | linux | amd64 |
| Linux ARM (Raspberry Pi, AWS Graviton) | linux | arm64 |
| macOS Intel | darwin | amd64 |
| macOS Apple Silicon | darwin | arm64 |

### Step 4: Deploy the Binary to a Control Node (Optional)

If you want to run `cexec` from your HPC master node rather than your laptop:

```bash
scp cexec-linux-amd64 user@master:~/cexec
ssh user@master "chmod +x ~/cexec"
```

From that point forward, log into `master` and run `./cexec` from there.

### Verify the Build

```bash
./cexec --help
```

You should see the usage text with all available flags. If you see this, the binary is working.

---

## Tutorial 1: Your First Run — 2 Node Cluster

### What you'll learn
- How cexec discovers nodes from `/etc/hosts`
- How to configure `cluster.env`
- How to run the HPC setup playbook
- How to read the output and verify results

### Scenario

You have two fresh Ubuntu servers:

| Role | Hostname | IP |
|---|---|---|
| Control (master) | master | 192.168.1.10 |
| Compute (worker) | node1 | 192.168.1.11 |

You want to configure them as an HPC cluster: shared filesystem, SSH trust between nodes, hostname resolution, and so on.

### Step 1: Set Up /etc/hosts on Your Control Machine

On the machine where you are running `cexec` (your laptop or the master node), add entries so the hostnames resolve:

```
# /etc/hosts  (add these lines)
192.168.1.10   master
192.168.1.11   node1
```

**Why this matters:** cexec reads `/etc/hosts` (or whatever file you point it at) to build its node inventory. The hostnames must be reachable by SSH. If you can `ssh ubuntu@master` and `ssh ubuntu@node1` successfully, cexec can reach them too.

### Step 2: Verify SSH Connectivity

Before touching cexec, confirm basic SSH access manually:

```bash
ssh ubuntu@master "hostname"
ssh ubuntu@node1 "hostname"
```

Both commands should return the hostname without errors. If they hang or fail, fix the SSH issue first — cexec cannot help with unreachable nodes.

### Step 3: Configure cluster.env

Copy the example configuration file:

```bash
cp cluster.env.example cluster.env
```

Now edit `cluster.env`:

```bash
CLUSTER_HOSTS_FILE=/etc/hosts
CLUSTER_USER=ubuntu
CLUSTER_PLAYBOOK=hpc-setup.yaml

# For a uniform cluster (one username, one password):
CLUSTER_PASSWORD=yourpassword

# For mixed usernames, use this instead (covers both SSH login and sudo):
# CLUSTER_USER_PASSWORDS=ubuntu:yourpassword,lab1:lab1password
```

**What each field does:**

- `CLUSTER_PASSWORD` — the SSH and sudo password for all nodes. Use `CLUSTER_USER_PASSWORDS` instead if your nodes have different usernames or passwords.
- `CLUSTER_HOSTS_FILE` — the file cexec parses for hostnames and IPs. Lines with `master` become the `control` group; lines with `node1`, `node2`, etc. become the `compute` group.
- `CLUSTER_USER` — the SSH username on all nodes.
- `CLUSTER_PLAYBOOK` — the default playbook file (you can override this with `--playbook`).

### Step 4: Run a Dry Run First

Before running anything for real, preview what would happen:

```bash
./cexec --playbook hpc-setup.yaml --dry-run
```

You will see output like:

```
[DRY RUN] master  →  install-deps       WOULD RUN: apt-get install -y nfs-kernel-server
[DRY RUN] master  →  configure-nfs      WOULD RUN: echo '/data *(rw,sync)' >> /etc/exports
[DRY RUN] node1   →  install-nfs-client WOULD RUN: apt-get install -y nfs-common
[DRY RUN] node1   →  mount-nfs          WOULD RUN: mount master:/data /mnt/data
...
```

No SSH connections have been made. No commands have run. This is your checklist.

### Step 5: Run the Playbook

```bash
./cexec --playbook hpc-setup.yaml
```

Watch the output scroll by. Each line shows the node, the step name, and the result. Something like:

```
[master]  install-deps        ✓  done  (12.3s)
[node1]   install-nfs-client  ✓  done  (11.8s)
[master]  configure-nfs       ✓  done  (0.4s)
[node1]   mount-nfs           ✓  done  (1.1s)
...

Summary: 8 steps ran, 0 failed, 0 skipped
```

Steps that can run in parallel (no dependency between them) run at the same time. Steps with `depends_on` wait for their prerequisites.

### Step 6: Verify the Result

SSH into each node and confirm the setup took effect:

```bash
# On master: check NFS is exporting
ssh ubuntu@master "exportfs -v"

# On node1: check NFS mount is active
ssh ubuntu@node1 "df -h | grep data"

# Check nodes can reach each other
ssh ubuntu@master "ping -c 2 node1"
```

**What happened:** cexec read your `/etc/hosts`, found `master` (control group) and `node1` (compute group), SSHed into both simultaneously, and ran each playbook step on the appropriate nodes, respecting the dependency order you defined. Every successful step was saved to `.cexec_state.json` so re-running is safe.

---

## Tutorial 2: Testing Without Running Anything (--dry-run)

### What you'll learn
- Why dry-run is essential before touching production
- How to read dry-run output
- The difference between `WOULD RUN` and `SKIP (cached)`

### Why Dry Run Matters

Imagine you have 20 nodes and a playbook with 15 steps. That is 300 individual operations. Running it blind is dangerous. Dry run lets you answer three questions before touching anything:

1. Which nodes will be affected?
2. Which steps will actually run (vs. skip because they are cached)?
3. Is my playbook logic correct?

### Step 1: Run --dry-run on a Fresh Setup

On a cluster where nothing has been done yet:

```bash
./cexec --playbook hpc-setup.yaml --dry-run
```

All steps show `WOULD RUN`:

```
[DRY RUN] master  →  install-deps        WOULD RUN
[DRY RUN] master  →  configure-nfs       WOULD RUN
[DRY RUN] node1   →  install-nfs-client  WOULD RUN
[DRY RUN] node1   →  mount-nfs           WOULD RUN
```

### Step 2: Run It For Real, Then Dry-Run Again

```bash
./cexec --playbook hpc-setup.yaml
./cexec --playbook hpc-setup.yaml --dry-run
```

Now the dry-run output looks different:

```
[DRY RUN] master  →  install-deps        SKIP (cached)
[DRY RUN] master  →  configure-nfs       SKIP (cached)
[DRY RUN] node1   →  install-nfs-client  SKIP (cached)
[DRY RUN] node1   →  mount-nfs           SKIP (cached)
```

Every step is `SKIP (cached)` because the SHA256 fingerprint of each `(nodeName, stepName, command)` tuple was recorded after it succeeded. Running again would do nothing new.

### Step 3: Use --dry-run to Audit Before Changes

You modified `hpc-setup.yaml` to add a new step. Before running it on production:

```bash
./cexec --playbook hpc-setup.yaml --dry-run
```

Output:

```
[DRY RUN] master  →  install-deps         SKIP (cached)
[DRY RUN] master  →  configure-nfs        SKIP (cached)
[DRY RUN] master  →  configure-firewall   WOULD RUN      ← new step
[DRY RUN] node1   →  install-nfs-client   SKIP (cached)
[DRY RUN] node1   →  mount-nfs            SKIP (cached)
```

Only the new step shows `WOULD RUN`. Everything else is untouched. This tells you exactly what will change — and only that will change — when you run for real.

### Step 4: Preview a Forced Re-run

Combine `--dry-run` and `--force` to see what a complete re-run would look like without doing it:

```bash
./cexec --playbook hpc-setup.yaml --dry-run --force
```

All steps show `WOULD RUN` because `--force` tells cexec to ignore the cache. Use this when you want to understand the blast radius of a forced re-run before committing.

**What happened:** `--dry-run` never makes SSH connections. It reads your playbook, checks the cache, and reports intent. Think of it as `terraform plan` for your cluster configuration.

---

## Tutorial 3: Re-running After a Failure (--force and Caching)

### What you'll learn
- How the hash-based cache protects against re-running completed work
- When to use `--force` and when not to
- The difference in output between a cached run and a fresh run

### Understanding the Cache

Every time a step completes successfully, cexec records:

```
SHA256(nodeName + "|" + stepName + "|" + command)  →  "done"
```

This fingerprint is stored in `.cexec_state.json`. On the next run, before executing any step, cexec checks if its fingerprint is in the cache. If yes, the step is skipped instantly. If no, it runs.

This has a powerful implication: **if a 10-step playbook fails on step 7, re-running it skips steps 1–6 and picks up from step 7**. You never redo work that already succeeded.

### Step 1: Simulate a Partial Failure

Run the playbook on a cluster where node1 has a disk full condition:

```bash
./cexec --playbook hpc-setup.yaml
```

Output:

```
[master]  install-deps        ✓  done  (11.2s)
[master]  configure-nfs       ✓  done  (0.3s)
[node1]   install-nfs-client  ✗  FAILED: no space left on device
[node1]   mount-nfs           SKIPPED (depends_on failed)

Summary: 2 steps succeeded, 1 failed, 1 skipped
```

Steps that succeeded on `master` are cached. The failed step on `node1` is NOT cached (it didn't complete successfully). The step that depended on the failed one is also not cached.

### Step 2: Fix the Problem and Re-run

Free up disk space on `node1`, then:

```bash
./cexec --playbook hpc-setup.yaml
```

Output this time:

```
[master]  install-deps        SKIP (cached)
[master]  configure-nfs       SKIP (cached)
[node1]   install-nfs-client  ✓  done  (10.1s)
[node1]   mount-nfs           ✓  done  (0.9s)

Summary: 2 steps ran, 0 failed, 2 skipped
```

The master's steps were skipped. Only the previously failed steps ran. This is the cache working for you — faster recovery and no risk of re-applying already-applied changes idempotently.

### Step 3: Force Everything to Re-run

Sometimes you want to re-run every step regardless of cache. For example: you changed a configuration value inside a command string, which changes the SHA256 hash. But the command string itself looks similar. Use `--force`:

```bash
./cexec --playbook hpc-setup.yaml --force
```

Output:

```
[master]  install-deps        ✓  done  (11.5s)   (was cached, forced)
[master]  configure-nfs       ✓  done  (0.4s)    (was cached, forced)
[node1]   install-nfs-client  ✓  done  (10.8s)   (was cached, forced)
[node1]   mount-nfs           ✓  done  (1.0s)    (was cached, forced)
```

`--force` ignores the cache entirely and re-runs everything. After the run, the cache is updated with fresh entries.

### When to Use --force vs Not

| Situation | Recommendation |
|---|---|
| Step failed, you fixed the issue | Just re-run. Cache handles it. |
| You changed the command in a step | Just re-run. New command = new hash = runs automatically. |
| You changed config files outside the playbook | Use `--force` to re-apply. |
| You suspect the state file is stale | Use `--force`. |
| New nodes added to cluster | Just re-run. New nodes have no cache entries. |
| You want a guaranteed clean slate | Use `--force` or delete `.cexec_state.json`. |

**What happened:** The cache is a local `.cexec_state.json` file. It is cheap to inspect (`cat .cexec_state.json | jq .`) and cheap to delete (`rm .cexec_state.json`) if you want a full reset. `--force` is the safer option — it re-runs and repopulates the cache correctly.

---

## Tutorial 4: Adding Node 3 Through Node 20

### What you'll learn
- How to expand your cluster without re-running steps on existing nodes
- Why cexec automatically handles new vs. existing nodes
- How to read output when only some nodes are doing work

### Scenario

You started with `master` and `node1`. Your cluster is growing. You now have 18 more compute nodes to add.

### Step 1: Add New Nodes to /etc/hosts

```bash
echo '192.168.1.12   node2' | sudo tee -a /etc/hosts
echo '192.168.1.13   node3' | sudo tee -a /etc/hosts
# ... repeat for each new node up to node19
```

No inventory files to update manually. On the next cexec run, any node found in `/etc/hosts` but not in `inventory.yaml` is auto-appended to `inventory.yaml` with group `[compute]` (or `[control]` for master). You will see a line like:

```
Info: synced node2 (192.168.1.12) → inventory.yaml  [groups: compute]
```

### Step 2: Verify Connectivity to New Nodes

```bash
for i in $(seq 2 19); do
  ssh ubuntu@node$i "hostname" || echo "node$i unreachable"
done
```

Fix any connectivity issues before proceeding.

### Step 3: Dry Run to Preview What Will Happen

```bash
./cexec --playbook hpc-setup.yaml --dry-run
```

You will see something like:

```
[DRY RUN] master  →  install-deps        SKIP (cached)
[DRY RUN] master  →  configure-nfs       SKIP (cached)
[DRY RUN] node1   →  install-nfs-client  SKIP (cached)
[DRY RUN] node1   →  mount-nfs           SKIP (cached)
[DRY RUN] node2   →  install-nfs-client  WOULD RUN
[DRY RUN] node2   →  mount-nfs           WOULD RUN
[DRY RUN] node3   →  install-nfs-client  WOULD RUN
...
[DRY RUN] node19  →  mount-nfs           WOULD RUN
```

`master` and `node1` are all cached. The 18 new nodes show `WOULD RUN` for every step because they have never been configured — their SHA256 fingerprints are not in `.cexec_state.json`.

### Step 4: Run the Playbook

```bash
./cexec --playbook hpc-setup.yaml
```

Watch the output:

```
[master]  install-deps        SKIP (cached)
[master]  configure-nfs       SKIP (cached)
[node1]   install-nfs-client  SKIP (cached)
[node1]   mount-nfs           SKIP (cached)
[node2]   install-nfs-client  ✓  done  (10.3s)
[node3]   install-nfs-client  ✓  done  (10.7s)
[node4]   install-nfs-client  ✓  done  (9.9s)
...
[node19]  mount-nfs           ✓  done  (1.1s)

Summary: 36 steps ran (on new nodes), 0 failed, 4 skipped (on existing nodes)
```

The new nodes are configured concurrently — all 18 run their steps in parallel. Old nodes are untouched. The whole expansion takes roughly as long as configuring one node.

### Step 5: Verify the New Nodes

```bash
./cexec --nodes compute -- "df -h | grep data"
```

All compute nodes (node1 through node19) should show the NFS mount. Any node missing it indicates a setup problem on that specific node.

**What happened:** cexec's cache is keyed on `(nodeName, stepName, command)`. Since `node2` through `node19` are new names, they have no cache entries. Existing nodes (`master`, `node1`) have all their steps cached. The playbook ran exactly the right work on exactly the right nodes automatically.

---

## Tutorial 5: Running Ad-hoc Commands

### What you'll learn
- How to use cexec without a playbook for day-to-day server management
- How to filter by node group or specific node names
- How to exclude specific nodes
- Why cexec is useful far beyond HPC

### The -- Separator

When running ad-hoc commands (no playbook), you pass the command after `--`:

```bash
./cexec -- <your command here>
```

Everything after `--` is passed verbatim to the remote shell. cexec SSHes into all nodes concurrently and runs it.

### Checking System Status

**Check disk space on every node:**

```bash
./cexec -- df -h
```

Output shows disk usage from all nodes simultaneously. Useful for spotting which node is running low before it becomes a problem.

**Check uptime and load:**

```bash
./cexec -- uptime
```

You will see one line per node with load average. If `node7` has a load of 48 when everything else shows 2, something is wrong on `node7`.

**Check which processes are running:**

```bash
./cexec -- "ps aux | grep myapp"
```

Quotes are needed when your command contains pipes or redirects — the shell needs to pass the whole string to SSH as a single argument.

### Targeting Specific Groups

**Restart a service only on compute nodes:**

```bash
./cexec --nodes compute --sudo -- systemctl restart myservice
```

`--nodes compute` filters to only the `compute` group (everything that is not `master`). `--sudo` runs the command with sudo.

**Run something only on master:**

```bash
./cexec --nodes control -- "exportfs -v"
```

**Target specific nodes by name:**

```bash
./cexec --nodes master,node2 -- systemctl restart nginx
```

You can mix group names and individual node names in the `--nodes` list, comma-separated.

### Excluding Nodes

**Reboot all workers except node1 (which you need to keep up):**

```bash
./cexec --nodes compute --exclude node1 --sudo -- reboot
```

`--exclude` removes specific nodes from the target set. Combine it with `--nodes` for fine-grained control.

**Run package updates on all except the database node:**

```bash
./cexec --exclude dbnode --sudo -- apt-get upgrade -y
```

### Deploying Configuration Files

**Copy a config to specific nodes:**

The usual pattern is: stage the file somewhere accessible (e.g., `/tmp` via `scp`), then use cexec to place it:

```bash
# First, put the config on those nodes via scp (or from a shared NFS path)
./cexec --nodes node1,node2 --sudo -- cp /tmp/nginx.conf /etc/nginx/nginx.conf
./cexec --nodes node1,node2 --sudo -- nginx -t
./cexec --nodes node1,node2 --sudo -- systemctl reload nginx
```

### Chaining Commands

**Install a package, enable, and start it in one shot:**

```bash
./cexec --sudo -- "apt-get install -y filebeat && systemctl enable filebeat && systemctl start filebeat"
```

The `&&` means if any step fails on a node, that node stops executing further commands. This is standard shell behavior — it applies per-node.

### Running Package Updates

```bash
./cexec --sudo -- "apt-get update && apt-get upgrade -y"
```

This runs concurrently on every node. What would take 30 minutes done one-at-a-time finishes in about the time it takes to update a single server.

**What happened:** cexec is not HPC-specific. It is a thin concurrency layer over SSH. Any command you can run manually via SSH, you can run on 1 or 100 servers in parallel using cexec. The `--nodes`, `--exclude`, and `--sudo` flags give you enough control for most day-to-day operations without writing a playbook.

---

## Tutorial 6: Writing Your Own Playbook

### What you'll learn
- How to create a playbook from scratch for a non-HPC use case
- What every playbook field does
- How `depends_on` prevents race conditions
- Common mistakes and how to avoid them

### Scenario: Setting Up a Web Cluster

You have three servers. `master` will be the load balancer running nginx in proxy mode. `node1` and `node2` will be application servers running a Go web service.

### Step 1: Create the Playbook File

Create `webcluster-setup.yaml`:

```yaml
steps:
  - name: "update-packages"
    command: "apt-get update -qq"
    sudo: true

  - name: "install-nginx"
    command: "apt-get install -y nginx"
    sudo: true
    depends_on: ["update-packages"]

  - name: "deploy-app-binary"
    command: "cp /mnt/shared/myapp /usr/local/bin/myapp && chmod +x /usr/local/bin/myapp"
    sudo: true
    roles: [compute]
    depends_on: ["update-packages"]

  - name: "install-app-service"
    command: "cp /mnt/shared/myapp.service /etc/systemd/system/myapp.service && systemctl daemon-reload"
    sudo: true
    roles: [compute]
    depends_on: ["deploy-app-binary"]

  - name: "start-app-service"
    command: "systemctl enable myapp && systemctl start myapp"
    sudo: true
    roles: [compute]
    depends_on: ["install-app-service"]

  - name: "configure-lb"
    command: "cp /mnt/shared/nginx-lb.conf /etc/nginx/sites-enabled/default && nginx -t"
    sudo: true
    roles: [control]
    depends_on: ["install-nginx", "start-app-service"]

  - name: "reload-lb"
    command: "systemctl reload nginx"
    sudo: true
    roles: [control]
    depends_on: ["configure-lb"]
```

### Step 2: Understanding Each Field

**`name`** — A human-readable identifier. Also used as part of the cache key. Keep names short, descriptive, and unique within the playbook. Names with spaces work fine; use quotes in YAML.

**`command`** — The shell command to run. This is passed to `/bin/sh -c` on the remote node. You can use `&&`, pipes, redirects, variables — anything your remote shell supports.

**`sudo`** — Set to `true` to prefix the command with `sudo`. The sudo password comes from `CLUSTER_USER_PASSWORDS` (matched by the node's username) or `CLUSTER_PASSWORD` as a fallback. Without this, commands run as `CLUSTER_USER`. If your command needs root, you must set this.

**`roles`** — A list of groups. `[control]` means the step only runs on the master node. `[compute]` means it only runs on worker nodes. Omit `roles` entirely to run on all nodes.

**`depends_on`** — A list of step names that must complete successfully before this step starts. cexec enforces this ordering. If a dependency fails, this step is skipped.

### Step 3: Understanding depends_on With a Cross-Node Example

Look at `configure-lb`:

```yaml
depends_on: ["install-nginx", "start-app-service"]
```

This step only runs on `control`. But it depends on `start-app-service`, which only runs on `compute` nodes. This is a cross-node dependency: cexec will not configure the load balancer until the application service is running on the compute nodes.

This prevents the classic mistake of pointing your load balancer at backends that aren't up yet.

### Step 4: Run the Playbook

```bash
./cexec --playbook webcluster-setup.yaml
```

cexec figures out the execution order automatically:

1. `update-packages` runs on all nodes in parallel.
2. `install-nginx` and `deploy-app-binary` run in parallel (both wait only for `update-packages`).
3. `install-app-service` waits for `deploy-app-binary`.
4. `start-app-service` waits for `install-app-service`.
5. `configure-lb` waits for both `install-nginx` AND `start-app-service` to complete.
6. `reload-lb` waits for `configure-lb`.

### Common Pitfalls

**Forgetting sudo:**

```yaml
# This will fail silently or with a permission error
- name: "install-nginx"
  command: "apt-get install -y nginx"
  # Missing: sudo: true
```

`apt-get` requires root. Without `sudo: true`, the command runs as your SSH user and fails. Always check whether your command needs elevated privileges.

**Circular depends_on:**

```yaml
- name: "step-a"
  depends_on: ["step-b"]   # A waits for B

- name: "step-b"
  depends_on: ["step-a"]   # B waits for A  ← DEADLOCK
```

cexec will detect this and report an error rather than hanging forever. If you see a "circular dependency" error, draw your dependency graph on paper and find the cycle.

**Depending on a step that only runs on a different role:**

```yaml
- name: "app-step"
  roles: [compute]
  depends_on: ["control-only-step"]   # This step never ran on compute nodes
```

If `control-only-step` only runs on `control` nodes, then `app-step` on compute nodes is waiting for a step that will never complete on those nodes. Design your dependencies around what runs where.

---

## Tutorial 7: Role-based Steps

### What you'll learn
- How cexec maps nodes to groups (control vs compute)
- How to write steps that target specific roles
- Real examples from HPC setup (NFS server vs NFS client)

### How Groups Are Assigned

Groups come from `inventory.yaml`. When a node is discovered in `/etc/hosts` but not yet in `inventory.yaml`, cexec auto-adds it with a default group:

- `master` → `[control]`
- `node1`, `node2`, `node3`, etc. → `[compute]`

```
/etc/hosts                         inventory.yaml (auto-maintained)
──────────────────────────         ──────────────────────────────────
192.168.1.10   master         →    groups: [control]
192.168.1.11   node1          →    groups: [compute]
192.168.1.12   node2          →    groups: [compute]
```

To override a group (e.g. a dedicated storage node), edit `inventory.yaml` directly after the auto-add and change its `groups` field. cexec will not overwrite manual group changes.

### Steps That Run Everywhere (No roles field)

```yaml
- name: "set-hostname"
  command: "hostnamectl set-hostname $(hostname)"
  sudo: true
```

No `roles` field means this step runs on every node in the cluster — master and all compute nodes.

### Steps That Run Only on Master

```yaml
- name: "install-nfs-server"
  command: "apt-get install -y nfs-kernel-server"
  sudo: true
  roles: [control]
```

The NFS server only needs to run on master. Putting it in `roles: [control]` ensures it never runs on worker nodes — even if you have 50 of them.

```yaml
- name: "configure-nfs-exports"
  command: "echo '/data *(rw,sync,no_subtree_check)' >> /etc/exports && exportfs -ra"
  sudo: true
  roles: [control]
  depends_on: ["install-nfs-server"]
```

### Steps That Run Only on Workers

```yaml
- name: "install-nfs-client"
  command: "apt-get install -y nfs-common"
  sudo: true
  roles: [compute]
```

Workers need the NFS client, not the server. `roles: [compute]` ensures this only runs on `node1`, `node2`, etc.

```yaml
- name: "mount-nfs"
  command: "mkdir -p /mnt/data && mount master:/data /mnt/data"
  sudo: true
  roles: [compute]
  depends_on: ["install-nfs-client", "configure-nfs-exports"]
```

The `depends_on` here includes `configure-nfs-exports` (a control-group step). Workers will not attempt to mount until master has finished configuring exports.

### Steps That Sync Across Node Boundaries

The `{{hosts_sync_cmds}}` template variable expands to a series of idempotent `grep ... || echo >> /etc/hosts` commands — one per cluster entry in master's `/etc/hosts`. Each command checks whether an entry already exists on the target node before appending it:

```yaml
- name: "sync-hosts"
  command: "{{hosts_sync_cmds}}"
  sudo: true
```

This incrementally adds any missing entries to each node's `/etc/hosts`. It is safe to re-run: existing entries are never duplicated.

When you add a new node to master's `/etc/hosts`, the expanded value of `{{hosts_sync_cmds}}` changes — which changes the SHA256 hash of this step — so cexec automatically re-runs the sync on all nodes next time. No manual cache clearing needed.

### Fully Overwriting /etc/hosts with {{hosts_content_b64}}

If a node's `/etc/hosts` is corrupted or was accidentally emptied, use `{{hosts_content_b64}}` to do a full overwrite:

```yaml
- name: "overwrite /etc/hosts from master"
  command: "echo '{{hosts_content_b64}}' | base64 -d > /etc/hosts"
  sudo: true
  roles: [compute]
```

`{{hosts_content_b64}}` expands to the full content of master's `/etc/hosts`, base64-encoded. This completely replaces the node's `/etc/hosts` with master's copy — a hard reset, not an incremental sync.

**Which one to use?**

| Scenario | Use |
|---|---|
| Adding new nodes, keeping existing entries | `{{hosts_sync_cmds}}` |
| Node's `/etc/hosts` is empty or corrupted | `{{hosts_content_b64}}` |
| Want master's exact `/etc/hosts` on every node | `{{hosts_content_b64}}` |

### Using the Master Public Key

For passwordless SSH between cluster nodes, use `{{master_pubkey}}`:

```yaml
- name: "distribute-ssh-key"
  command: "echo '{{master_pubkey}}' >> ~/.ssh/authorized_keys"
  roles: [compute]
```

`{{master_pubkey}}` is expanded to the contents of `~/.ssh/id_ed25519.pub` on the machine running cexec. This lets your master node SSH into worker nodes without a password for MPI or other HPC workloads.

### Full Example: HPC Node Setup

```yaml
steps:
  - name: "install-base"
    command: "apt-get install -y build-essential htop nfs-common"
    sudo: true

  - name: "install-nfs-server"
    command: "apt-get install -y nfs-kernel-server"
    sudo: true
    roles: [control]
    depends_on: ["install-base"]

  - name: "configure-nfs"
    command: "echo '/data *(rw,sync)' >> /etc/exports && exportfs -ra"
    sudo: true
    roles: [control]
    depends_on: ["install-nfs-server"]

  - name: "mount-shared"
    command: "mkdir -p /data && mount master:/data /data"
    sudo: true
    roles: [compute]
    depends_on: ["install-base", "configure-nfs"]

  - name: "add-ssh-key"
    command: "echo '{{master_pubkey}}' >> ~/.ssh/authorized_keys"
    roles: [compute]
    depends_on: ["install-base"]

  - name: "sync-hosts-to-nodes"
    command: "{{hosts_sync_cmds}}"
    sudo: true
    roles: [control]
    depends_on: ["install-base"]
```

**What happened:** Role-based execution means you write one playbook for the whole cluster. cexec reads the role and sends each step only to the nodes where it belongs. You do not need to write separate playbooks for master vs. workers or SSH into individual nodes manually.

---

## Tutorial 8: Debugging Failures

### What you'll learn
- How to read failure output
- How to find and interpret the JSON log file
- How to use concurrency 1 for sequential debugging
- How to use --timeout for hanging commands

### Step 1: What a Failed Step Looks Like

When a step fails, cexec prints something like:

```
[node3]  install-nfs-client  ✗  FAILED
         Error category: non_zero_exit
         Last stderr: E: Could not get lock /var/lib/dpkg/lock-frontend
```

The key fields:

- **Node name** — which node failed.
- **Step name** — which step failed.
- **Error category** — a machine-readable classification (see list below).
- **Last stderr line** — the most relevant error message from the command's stderr.

### Error Categories

| Category | Meaning |
|---|---|
| `ssh_auth_failed` | Wrong password, wrong user, or key not accepted |
| `host_unreachable` | Cannot connect to the host (network, firewall, host down) |
| `sudo_auth_failed` | sudo rejected the password; user may not be in sudoers |
| `non_zero_exit` | Command ran but exited with non-zero status |
| `timeout` | Command exceeded the `--timeout` duration |
| `connection_reset` | SSH connection dropped mid-execution |

### Step 2: Read the JSON Log File

For every failed step, cexec writes a detailed log to `logs/`. The filename pattern is `<node>_<step>.json`:

```bash
cat logs/node3_install-nfs-client.json
```

```json
{
  "node": "node3",
  "step": "install-nfs-client",
  "command": "apt-get install -y nfs-common",
  "exit_code": 100,
  "stdout": "",
  "stderr": "E: Could not get lock /var/lib/dpkg/lock-frontend - is another process using it?\nE: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), is another process using it?",
  "error_category": "non_zero_exit",
  "timestamp": "2026-03-14T10:23:11Z"
}
```

The `stderr` field contains the full error output, not just the last line. This is often enough to diagnose the problem without logging into the node manually.

### Step 3: Use --concurrency 1 for Sequential Debugging

By default, cexec runs steps concurrently. When debugging, interleaved output from multiple nodes makes it hard to follow what is happening. Run with a single connection:

```bash
./cexec --playbook hpc-setup.yaml --concurrency 1
```

Output is now one node at a time, one step at a time, in a clean sequence. You can watch exactly what happens and spot the failure without noise.

### Step 4: Check State Before Re-running

Before re-running after a fix, use `--dry-run` to confirm which steps will run:

```bash
./cexec --playbook hpc-setup.yaml --dry-run
```

This tells you whether cexec correctly identified the failed steps as uncached (they should show `WOULD RUN`).

### Step 5: Use --timeout for Hanging Commands

Some commands hang indefinitely. A common example: mounting a filesystem when the NFS server is down. Without a timeout, cexec waits forever.

```bash
./cexec --playbook hpc-setup.yaml --timeout 2m
```

This sets a 2-minute timeout per command. If any command on any node exceeds 2 minutes, cexec kills it and records the step as failed with `error_category: timeout`. You then know immediately which node's command is hanging rather than waiting indefinitely.

### Step 6: Re-run After Fixing

Once you have diagnosed and fixed the issue (freed the dpkg lock, in the example above):

```bash
./cexec --playbook hpc-setup.yaml
```

cexec skips everything that already succeeded and retries only the failed steps.

### Step 7: Using --retries for Flaky Operations

Some operations are inherently flaky — package downloads, network mounts, anything with external dependencies. Instead of failing immediately:

```bash
./cexec --playbook hpc-setup.yaml --retries 3
```

Failed steps are retried up to 3 times before being marked as failed. Use this in environments with unreliable networks or package mirrors.

**What happened:** The combination of error categories, JSON logs, `--concurrency 1`, `--timeout`, and `--dry-run` gives you a complete debugging toolkit. Start with the JSON log to understand what happened, use `--concurrency 1` to watch it happen in real time, and use `--timeout` to prevent indefinite hangs.

---

## Tutorial 9: Concurrency and Performance

### What you'll learn
- Why concurrency is the core value proposition of cexec
- How to configure the concurrency level
- How depends_on affects parallelism
- The real-world time savings

### The Math of Sequential vs Concurrent Execution

Suppose you have 20 nodes and each step takes 10 minutes. Running sequentially:

```
20 nodes × 10 minutes = 200 minutes (~3.3 hours)
```

Running concurrently (all nodes at the same time):

```
1 batch × 10 minutes = 10 minutes
```

For a 5-step playbook with no dependencies between steps:

```
Sequential: 20 nodes × 5 steps × 2 min/step = 200 minutes
Concurrent: 5 steps × 2 min/step = 10 minutes
```

This is why cexec runs everything in parallel by default.

### Controlling Concurrency

**Default behavior:** cexec opens SSH connections to all nodes simultaneously and runs eligible steps as fast as possible.

**Limit connections with --concurrency:**

```bash
./cexec --playbook hpc-setup.yaml --concurrency 5
```

This caps the number of simultaneous SSH connections to 5. Use this when:

- Your SSH server has a connection limit (`MaxSessions` in `sshd_config`).
- Your network cannot handle 50 simultaneous SSH sessions.
- You are debugging and want more ordered output.
- The remote nodes have limited resources and simultaneous heavy operations would overwhelm them.

**Sequential for debugging:**

```bash
./cexec --playbook hpc-setup.yaml --concurrency 1
```

One connection at a time. Useful for understanding what is happening step by step.

### How depends_on Affects Parallelism

Consider this playbook:

```yaml
steps:
  - name: "update"
    command: "apt-get update"
    sudo: true

  - name: "install-a"
    command: "apt-get install -y packageA"
    sudo: true
    depends_on: ["update"]

  - name: "install-b"
    command: "apt-get install -y packageB"
    sudo: true
    depends_on: ["update"]

  - name: "configure"
    command: "/usr/local/bin/configure-all"
    sudo: true
    depends_on: ["install-a", "install-b"]
```

The execution timeline looks like:

```
T=0:  update      → runs on all 20 nodes simultaneously
T=2m: install-a   → starts on all 20 nodes simultaneously (update done)
      install-b   → starts on all 20 nodes simultaneously (update done)
T=4m: configure   → starts only after BOTH install-a and install-b finish
```

`install-a` and `install-b` run in parallel with each other because neither depends on the other — only both depend on `update`. This is the correct way to model independent work that needs a shared prerequisite.

### Reading Concurrent Output

When 20 nodes run simultaneously, output lines arrive in the order things complete, not the order you defined steps:

```
[node7]   install-deps  ✓  done  (9.8s)
[node12]  install-deps  ✓  done  (10.1s)
[node1]   install-deps  ✓  done  (10.3s)
[master]  install-deps  ✓  done  (10.5s)
...
```

This is normal. The summary at the end tells you the totals:

```
Summary: 40 steps ran, 0 failed, 0 skipped
Total wall time: 12m 34s   (vs ~3h 20m sequential)
```

**What happened:** cexec's concurrency is transparent. You write your playbook with `depends_on` to express ordering constraints, and cexec figures out which steps can run in parallel. Steps without dependencies between them always run simultaneously. Steps with dependencies wait for their prerequisites and then run on all applicable nodes at the same time.

---

## Tutorial 10: Beyond HPC — General Use Cases

### What you'll learn
- Real use cases for cexec outside of HPC clusters
- How the same tool and patterns apply to web fleets, databases, and CI infrastructure

cexec is just SSH plus concurrency plus optional playbook orchestration. The HPC examples demonstrate the tool, but anything you would do over SSH on multiple servers is a valid use case.

### Use Case 1: Web Server Fleet Management

You have 10 nginx web servers. You want to deploy a new configuration:

```bash
# Stage the new config from a central location
./cexec --nodes compute --sudo -- "curl -s http://config-server/nginx.conf -o /tmp/nginx.conf"

# Validate it
./cexec --nodes compute --sudo -- "nginx -t -c /tmp/nginx.conf"

# If validation passed, deploy and reload
./cexec --nodes compute --sudo -- "cp /tmp/nginx.conf /etc/nginx/nginx.conf && systemctl reload nginx"

# Verify reload succeeded
./cexec --nodes compute -- "systemctl is-active nginx"
```

Each command runs on all 10 servers concurrently. If `nginx -t` fails on `node4`, you see it immediately and can investigate before deploying the broken config.

### Use Case 2: Database Cluster Backup

You run PostgreSQL on 5 replica nodes. Trigger a backup on all of them simultaneously:

```bash
./cexec --nodes compute --sudo -- "pg_dump mydb | gzip > /backups/mydb-$(date +%Y%m%d).sql.gz"
```

Or as a playbook for more control:

```yaml
steps:
  - name: "create-backup-dir"
    command: "mkdir -p /backups && chown postgres:postgres /backups"
    sudo: true
    roles: [compute]

  - name: "run-backup"
    command: "pg_dump mydb | gzip > /backups/mydb-$(date +%Y%m%d).sql.gz"
    sudo: true
    roles: [compute]
    depends_on: ["create-backup-dir"]

  - name: "verify-backup"
    command: "ls -lh /backups/mydb-$(date +%Y%m%d).sql.gz"
    roles: [compute]
    depends_on: ["run-backup"]
```

Run this from a cron job on your control server with `--quiet` to suppress per-step output.

### Use Case 3: Security Patching

You have 50 servers and need to apply CVE patches. Manual patching one-by-one would take hours and you'd inevitably miss some:

```bash
# Preview what would be upgraded (no changes made)
./cexec --sudo -- "apt-get upgrade --dry-run 2>/dev/null | grep '^Inst'"

# Apply the patches
./cexec --sudo -- "DEBIAN_FRONTEND=noninteractive apt-get upgrade -y"

# Reboot nodes that need it (check for reboot-required file)
./cexec -- "test -f /var/run/reboot-required && echo NEEDS_REBOOT || echo ok"

# Reboot the ones that need it (exclude one node to keep something running)
./cexec --exclude node1 --sudo -- reboot
```

50 servers patched and rebooted in the time it takes to manually patch 2 or 3.

### Use Case 4: Log Collection Setup (Filebeat)

Roll out Elastic's Filebeat to all nodes for centralized logging:

```yaml
steps:
  - name: "add-elastic-repo"
    command: "curl -fsSL https://artifacts.elastic.co/GPG-KEY-elasticsearch | apt-key add - && echo 'deb https://artifacts.elastic.co/packages/8.x/apt stable main' > /etc/apt/sources.list.d/elastic-8.x.list"
    sudo: true

  - name: "apt-update"
    command: "apt-get update -qq"
    sudo: true
    depends_on: ["add-elastic-repo"]

  - name: "install-filebeat"
    command: "apt-get install -y filebeat"
    sudo: true
    depends_on: ["apt-update"]

  - name: "configure-filebeat"
    command: "cp /mnt/shared/filebeat.yml /etc/filebeat/filebeat.yml"
    sudo: true
    depends_on: ["install-filebeat"]

  - name: "enable-filebeat"
    command: "systemctl enable filebeat && systemctl start filebeat"
    sudo: true
    depends_on: ["configure-filebeat"]
```

```bash
./cexec --playbook filebeat-setup.yaml
```

All nodes get Filebeat installed and configured simultaneously. What would be 20+ manual SSH sessions becomes one command.

### Use Case 5: Development Environment Provisioning

Your team gets new developer boxes regularly. Standardize setup with a playbook:

```yaml
steps:
  - name: "install-tools"
    command: "apt-get install -y git curl vim tmux htop build-essential"
    sudo: true

  - name: "install-docker"
    command: "curl -fsSL https://get.docker.com | sh"
    sudo: true
    depends_on: ["install-tools"]

  - name: "add-user-to-docker"
    command: "usermod -aG docker $USER"
    sudo: true
    depends_on: ["install-docker"]

  - name: "install-go"
    command: "curl -fsSL https://go.dev/dl/go1.22.linux-amd64.tar.gz | tar -C /usr/local -xz"
    sudo: true
    depends_on: ["install-tools"]

  - name: "configure-bashrc"
    command: "echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc"
    depends_on: ["install-go"]
```

New developer joins → add their box IP to the hosts file → run `./cexec --playbook devenv.yaml` → done. Cache means existing boxes are untouched.

---

## Tips and Tricks

### Use --quiet for Cron Jobs

When running cexec from cron, you probably only want output on failure:

```bash
# In crontab
0 2 * * * /home/admin/cexec --playbook nightly-backup.yaml --quiet
```

`--quiet` suppresses per-step output and only prints the final summary (and full output on failure). Your cron logs stay clean.

### Use --retries for Flaky Networks

Package downloads, external API calls, and NFS mounts can be intermittently flaky. Add retries:

```bash
./cexec --playbook hpc-setup.yaml --retries 3
```

Each failed step is retried up to 3 times before being marked as definitively failed. The retry count is not burned through on cached steps.

### Mixed-User Clusters (Different Usernames on Different Nodes)

When nodes have different SSH usernames — common during migrations — use `CLUSTER_USER_PASSWORDS` in `cluster.env`:

```bash
# cluster.env
CLUSTER_USER_PASSWORDS=hpc:hpcpassword,lab1:lab1password
```

cexec matches each node's SSH `user` field against this map and uses the corresponding password for **both** SSH login and `sudo`. You do not need to touch `CLUSTER_PASSWORD` at all if every username in your cluster is listed here.

If some nodes also have inventory entries with different `user` fields (e.g. some nodes say `user: hpc`, others say `user: lab1`), the map automatically routes the right password to each node.

### Per-node Password Override

For a one-off override of a specific node's password, export an environment variable named after the node:

```bash
export node3=special_password
./cexec --playbook hpc-setup.yaml
```

This takes the highest priority — overrides both `CLUSTER_USER_PASSWORDS` and `CLUSTER_PASSWORD` for that node. Useful for testing a single node without changing `cluster.env`.

### Inspect the Cache

The cache is a plain JSON file. You can read and manipulate it directly:

```bash
# See all cached entries
cat .cexec_state.json | jq .

# Count cached entries
cat .cexec_state.json | jq 'keys | length'

# Find entries for a specific node
cat .cexec_state.json | jq 'to_entries | map(select(.key | contains("node3")))'
```

### Reset the Cache

To force a fresh start on everything:

```bash
rm .cexec_state.json
```

cexec creates a new state file on the next run. Use this when you suspect the state file is incorrect or after a major cluster rebuild.

### Target All Nodes Explicitly

```bash
# These are equivalent when you have no filtering
./cexec -- hostname
./cexec --nodes all -- hostname
```

`--nodes all` is useful when you want to be explicit in scripts.

### Chain Multiple Commands

Use `&&` to run a sequence of commands where each depends on the previous:

```bash
./cexec -- "systemctl stop myservice && rm -rf /var/lib/myservice/cache && systemctl start myservice"
```

Use `;` if you want to run all commands regardless of whether earlier ones fail:

```bash
./cexec -- "journalctl -u myservice --since '1 hour ago' ; df -h ; free -m"
```

### Preview a Forced Re-run

Before committing to a `--force` run on 50 nodes, preview it:

```bash
./cexec --playbook hpc-setup.yaml --dry-run --force
```

Shows all steps as `WOULD RUN` without making any connections. Lets you confirm the scope before running for real.

### SSH Key-Based Auth

While cexec supports password authentication, SSH key authentication is faster, more reliable, and avoids storing passwords in config files:

```bash
# Generate a key pair on your control machine (if you don't have one)
ssh-keygen -t ed25519 -C "cexec-control"

# Copy the key to all nodes (do this once manually)
for host in master node1 node2 node3; do
  ssh-copy-id ubuntu@$host
done

# Then remove CLUSTER_PASSWORD from cluster.env
# cexec will use your SSH agent automatically
```

---

## Troubleshooting

### "no SSH auth method available"

**Cause:** cexec cannot find a way to authenticate. Either `CLUSTER_PASSWORD` is not set in `cluster.env` and there is no SSH key loaded in your agent.

**Fix:**
```bash
# Option 1: Set password in cluster.env
CLUSTER_PASSWORD=yourpassword

# Option 2: Load your SSH key into the agent
ssh-add ~/.ssh/id_ed25519

# Option 3: Copy your SSH key to all nodes
ssh-copy-id ubuntu@master
ssh-copy-id ubuntu@node1
```

---

### "permission denied (publickey, password)"

**Cause:** The SSH server on the remote host rejected all authentication methods. Either the password is wrong, the username is wrong, or the key being used is not in `~/.ssh/authorized_keys` on the remote host.

**Fix:**
```bash
# Verify the user and password manually
ssh ubuntu@node1  # try logging in by hand

# Check CLUSTER_USER in cluster.env matches the actual user on the nodes
# Check CLUSTER_PASSWORD is the correct password

# If using key auth, verify the key is deployed
ssh-copy-id ubuntu@node1
```

---

### "no route to host"

**Cause:** The network cannot reach the node. The node may be down, the IP address may be wrong, or a firewall is blocking the connection.

**Fix:**
```bash
# Check if the node is reachable at all
ping 192.168.1.11

# Check if port 22 is open
nc -zv 192.168.1.11 22

# Check the IP in /etc/hosts matches reality
grep node1 /etc/hosts
```

---

### "sudo: a password is required" / "Sorry, try again"

**Cause:** The step uses `sudo: true` but the sudo password is not reaching the node. This happens when:
- Neither `CLUSTER_USER_PASSWORDS` nor `CLUSTER_PASSWORD` is set in `cluster.env`
- The password in `cluster.env` is wrong for that node
- SSH key auth succeeded (so login worked) but the sudo password source was empty

**Fix:**
```bash
# Recommended: set per-user passwords in cluster.env
# These apply to BOTH SSH login and sudo
CLUSTER_USER_PASSWORDS=hpc:yourpassword,lab1:lab1password

# Alternative: set a single global fallback
CLUSTER_PASSWORD=yourpassword

# Verify the password works manually by SSH-ing in and testing sudo:
ssh hpc@node1 "echo yourpassword | sudo -S whoami"
# Expected output: root

# If you prefer passwordless sudo, add NOPASSWD to /etc/sudoers on each node:
# hpc ALL=(ALL) NOPASSWD:ALL
```

**Important:** `CLUSTER_USER_PASSWORDS` has higher priority than `CLUSTER_PASSWORD` and applies to sudo as well as SSH login. If your nodes use SSH key auth for login, cexec still needs the password for `sudo` — make sure it is set in `cluster.env`.

---

### "no cluster nodes found"

**Cause:** cexec read `CLUSTER_HOSTS_FILE` but found no lines matching `master` or `nodeN` patterns.

**Fix:**
```bash
# Check the file exists and has entries
cat /etc/hosts | grep -E 'master|node[0-9]'

# Check CLUSTER_HOSTS_FILE points to the right file
grep CLUSTER_HOSTS_FILE cluster.env

# If using a custom hosts file, verify the format:
# IP<whitespace>hostname
192.168.1.10   master
192.168.1.11   node1
```

---

### Step Hanging Indefinitely

**Cause:** A command on a remote node is waiting for input, a network operation is stuck, or an NFS mount is blocking.

**Fix:**
```bash
# Set a timeout to fail stuck commands after 2 minutes
./cexec --playbook hpc-setup.yaml --timeout 2m

# Check which node is hanging (use concurrency 1 to see sequential output)
./cexec --playbook hpc-setup.yaml --concurrency 1 --timeout 2m
```

After the timeout, cexec reports the step as failed with `error_category: timeout` and logs the full context to `logs/`.

---

### State File Corrupt or Stale

**Cause:** `.cexec_state.json` was partially written (interrupted run), manually edited incorrectly, or contains entries from a previous cluster configuration.

**Fix:**
```bash
# Just delete it. cexec creates a fresh one on the next run.
rm .cexec_state.json

# Then optionally preview what will run (everything, since cache is empty)
./cexec --playbook hpc-setup.yaml --dry-run
```

---

### Steps Skipping That Shouldn't Be

**Cause:** You changed the intent of a step but not its `name` or `command`. Because the cache key is `SHA256(nodeName|stepName|command)`, if the command string is identical to the last successful run, the step is cached.

**Fix:**
```bash
# Force re-run of everything
./cexec --playbook hpc-setup.yaml --force

# Or delete the cache for a clean slate
rm .cexec_state.json && ./cexec --playbook hpc-setup.yaml
```

Alternatively, if only one step needs to re-run, change the `command` field in the playbook (even trivially — adding a comment `# v2`) to produce a new hash.

---

### Concurrency Too High (SSH Refused / Resource Exhaustion)

**Cause:** With 50 nodes and default concurrency, you may open 50 simultaneous SSH connections. Some SSH servers limit sessions per user or per source IP. Remote nodes may also struggle with 50 simultaneous heavy operations (like `apt-get upgrade`).

**Fix:**
```bash
# Limit to 10 simultaneous connections
./cexec --playbook hpc-setup.yaml --concurrency 10

# Or stagger package operations (use a sleep in the command — last resort)
./cexec --sudo --concurrency 5 -- "apt-get upgrade -y"
```

---

*This tutorial covers the core workflows. For the full flag reference, run `./cexec --help`. For playbook schema details, see the example playbooks in the repository.*
