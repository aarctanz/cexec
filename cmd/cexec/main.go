package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	cerrors "github.com/shivamrkm/cexec/internal/errors"
	"github.com/shivamrkm/cexec/internal/executor"
	"github.com/shivamrkm/cexec/internal/hosts"
	"github.com/shivamrkm/cexec/internal/inventory"
	"github.com/shivamrkm/cexec/internal/logging"
	"github.com/shivamrkm/cexec/internal/playbook"
	"github.com/shivamrkm/cexec/internal/state"
)

func main() {
	// --- Load .env file first so its values become env var defaults -----------
	// Explicit CLI flags and real env vars always take precedence over .env.
	// Find the env file: --env-file flag, or CEXEC_ENV env var, or default "cluster.env".
	envFileFlag := "cluster.env"
	for i, arg := range os.Args[1:] {
		if arg == "--env-file" || arg == "-env-file" {
			if i+2 < len(os.Args) {
				envFileFlag = os.Args[i+2]
			}
		} else if strings.HasPrefix(arg, "--env-file=") {
			envFileFlag = strings.TrimPrefix(arg, "--env-file=")
		} else if strings.HasPrefix(arg, "-env-file=") {
			envFileFlag = strings.TrimPrefix(arg, "-env-file=")
		}
	}
	if v := os.Getenv("CEXEC_ENV"); v != "" {
		envFileFlag = v
	}
	if err := loadEnvFile(envFileFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load env file %s: %v\n", envFileFlag, err)
	}

	// --- Flags (env file values are now in os.Getenv, used as defaults) ------
	inventoryPath := flag.String("inventory", "inventory.yaml", "Path to inventory YAML file (unused when --auto-hosts or CLUSTER_HOSTS_FILE is set)")
	autoHosts     := flag.String("auto-hosts", os.Getenv("CLUSTER_HOSTS_FILE"), "Auto-build inventory from this hosts file — defaults to CLUSTER_HOSTS_FILE in cluster.env")
	hostsUser     := flag.String("hosts-user", envDefault("CLUSTER_USER", "hpc"), "SSH user when using --auto-hosts")
	envFile       := flag.String("env-file", envFileFlag, "Path to cluster.env file (KEY=VALUE, # comments)")
	selector      := flag.String("nodes", "all", "Target selector: 'all', group name, or comma-separated node names")
	exclude       := flag.String("exclude", "", "Comma-separated node names to exclude")
	sudo          := flag.Bool("sudo", false, "Execute command with sudo")
	timeout       := flag.Duration("timeout", 5*time.Minute, "Per-command timeout")
	concurrency   := flag.Int("concurrency", 0, "Max parallel connections (0 = unlimited)")
	retries       := flag.Int("retries", 0, "Max retry attempts for failed nodes (0 = no retry)")
	backoffBase   := flag.Duration("backoff", 2*time.Second, "Backoff base duration")
	backoffFixed  := flag.Bool("backoff-fixed", false, "Use fixed backoff instead of exponential")
	logDir        := flag.String("log-dir", envDefault("CLUSTER_LOG_DIR", "logs"), "Directory for JSON run logs")
	dryRun        := flag.Bool("dry-run", false, "Show targeted nodes without executing")
	quiet         := flag.Bool("quiet", false, "Suppress per-node output (only show summary)")
	playbookPath  := flag.String("playbook", "", "Path to YAML playbook file (runs multi-step setup)")
	stateFile     := flag.String("state-file", envDefault("CLUSTER_STATE_FILE", ".cexec_state.json"), "Path to state file for skip-if-done tracking")
	forceRun      := flag.Bool("force", false, "Ignore cached state and re-run all steps")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: cexec [flags] -- <command>\n")
		fmt.Fprintf(os.Stderr, "       cexec [flags] --playbook <file.yaml>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\ncluster.env keys (loaded automatically if present):\n")
		fmt.Fprintf(os.Stderr, "  CLUSTER_PASSWORD    SSH + sudo password for all nodes\n")
		fmt.Fprintf(os.Stderr, "  CLUSTER_HOSTS_FILE  Hosts file for auto-inventory (e.g. /etc/hosts)\n")
		fmt.Fprintf(os.Stderr, "  CLUSTER_USER        SSH user (default: hpc)\n")
		fmt.Fprintf(os.Stderr, "  CLUSTER_PLAYBOOK    Default playbook path\n")
		fmt.Fprintf(os.Stderr, "  CLUSTER_LOG_DIR     Log directory (default: logs)\n")
		fmt.Fprintf(os.Stderr, "  CLUSTER_STATE_FILE  State file path (default: .cexec_state.json)\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  cexec --playbook hpc-setup.yaml          # uses cluster.env for everything\n")
		fmt.Fprintf(os.Stderr, "  cexec --auto-hosts /etc/hosts -- hostname\n")
		fmt.Fprintf(os.Stderr, "  cexec --nodes compute --exclude node03 -- apt update\n")
	}
	flag.Parse()

	// CLUSTER_PLAYBOOK env default only kicks in when no explicit --playbook flag
	// AND no -- command was given. A bare command always wins over the env default.
	if *playbookPath == "" && len(flag.Args()) == 0 {
		if v := os.Getenv("CLUSTER_PLAYBOOK"); v != "" {
			*playbookPath = v
		}
	}

	// Re-load env file if --env-file was explicitly changed by the user.
	if *envFile != envFileFlag {
		if err := loadEnvFile(*envFile); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not load env file %s: %v\n", *envFile, err)
		}
	}

	// Global SSH/sudo password — env file already loaded, just read it.
	clusterPassword := os.Getenv("CLUSTER_PASSWORD")

	// Graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "\nReceived %s — shutting down gracefully...\n", sig)
		cancel()
		sig = <-sigCh
		fmt.Fprintf(os.Stderr, "Received %s again — forcing exit\n", sig)
		os.Exit(130)
	}()

	// --- Load inventory -------------------------------------------------------
	// Priority: --auto-hosts flag > CLUSTER_HOSTS_FILE in .env > inventory.yaml
	var inv *inventory.Inventory
	var err error
	if *autoHosts != "" {
		inv, err = hosts.LoadInventory(*autoHosts, *hostsUser, 22)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error (auto-hosts): %v\n", err)
			os.Exit(1)
		}
	} else {
		inv, err = inventory.Load(*inventoryPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// Inject passwords at runtime — never stored in inventory files.
	for i := range inv.Nodes {
		n := &inv.Nodes[i]
		if p := os.Getenv(n.Name); p != "" {
			n.Password = p
		} else if clusterPassword != "" {
			n.Password = clusterPassword
		}
	}

	nodes, err := inventory.Select(inv, *selector, *exclude)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// --- Playbook mode ---
	if *playbookPath != "" {
		if *dryRun {
			dryRunPlaybook(nodes, *playbookPath, *stateFile, *forceRun)
			os.Exit(0)
		}
		runPlaybook(ctx, nodes, *playbookPath, *stateFile, *forceRun, *logDir,
			*timeout, *concurrency, *retries, *backoffBase, *backoffFixed, *quiet,
			clusterPassword, *autoHosts)
		return
	}

	// --- Single-command mode ---
	cmd := strings.Join(flag.Args(), " ")
	if cmd == "" {
		fmt.Fprintln(os.Stderr, "Error: no command specified. Use -- <command> or --playbook <file>")
		flag.Usage()
		os.Exit(1)
	}

	opts := executor.Options{
		Command:        cmd,
		Sudo:           *sudo,
		Timeout:        *timeout,
		MaxConcurrency: *concurrency,
		MaxRetries:     *retries,
		BackoffBase:    *backoffBase,
		BackoffFixed:   *backoffFixed,
		SudoPassEnvFn: func(nodeName string) string {
			if p := os.Getenv(nodeName); p != "" {
				return p
			}
			if clusterPassword != "" {
				return clusterPassword
			}
			return os.Getenv("CLUSTER_SUDO_PASS")
		},
	}

	if *dryRun {
		executor.DryRun(nodes, opts)
		os.Exit(0)
	}

	runID := generateRunID()
	runStart := time.Now()
	fmt.Printf("Run ID  : %s\n", runID)
	fmt.Printf("Command : %s\n", cmd)
	fmt.Printf("Targets : %d node(s)\n", len(nodes))
	fmt.Printf("Sudo    : %v\n", *sudo)
	fmt.Println(strings.Repeat("─", 60))

	nodeLogs := executor.Run(ctx, nodes, opts)
	runEnd := time.Now()

	if !*quiet {
		for _, nl := range nodeLogs {
			printNodeResult(nl)
		}
	}

	summary := logging.BuildSummary(nodeLogs)
	printSummary(summary)

	runLog := logging.RunLog{
		RunID:     runID,
		Command:   cmd,
		Sudo:      *sudo,
		StartTime: runStart,
		EndTime:   runEnd,
		Duration:  runEnd.Sub(runStart).Round(time.Millisecond).String(),
		Summary:   summary,
		Results:   sanitizeLogs(nodeLogs),
	}
	logPath, err := logging.WriteLog(*logDir, runLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write log: %v\n", err)
	} else {
		fmt.Printf("\nLog written: %s\n", logPath)
	}

	if summary.Failed > 0 || summary.Unreachable > 0 || summary.TimedOut > 0 {
		os.Exit(1)
	}
}

// dryRunPlaybook prints what each step would do without executing anything.
func dryRunPlaybook(nodes []inventory.Node, pbPath, stateFilePath string, force bool) {
	pb, err := playbook.Load(pbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading playbook: %v\n", err)
		os.Exit(1)
	}
	st, err := state.Load(stateFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading state: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("=== DRY RUN — Playbook: %s (%d steps, %d nodes) ===\n\n", pbPath, len(pb.Steps), len(nodes))
	for i, step := range pb.Steps {
		fmt.Printf("[%d/%d] %s  (sudo=%v)\n", i+1, len(pb.Steps), step.Name, step.Sudo)
		if len(step.Roles) > 0 {
			fmt.Printf("  roles: %v\n", step.Roles)
		}
		fmt.Printf("  cmd  : %s\n", step.Command)
		for _, n := range nodes {
			if len(step.Roles) > 0 && !nodeMatchesRoles(n, step.Roles) {
				fmt.Printf("  %-10s → SKIP (role mismatch)\n", n.Name)
				continue
			}
			h := state.Hash(n.Name, step.Name, step.Command)
			if !force && st.Done(n.Name, h) {
				fmt.Printf("  %-10s → SKIP (already done)\n", n.Name)
			} else {
				fmt.Printf("  %-10s → WOULD RUN\n", n.Name)
			}
		}
		fmt.Println()
	}
}

// nodeResult is one step-completion event streamed from a node goroutine.
type nodeResult struct {
	node       string
	stepName   string
	stepIdx    int
	nl         logging.NodeLog
	skipped    bool
	skipReason string // "already done" | "role mismatch"
}

// runPlaybook executes a multi-step YAML playbook with full node-level concurrency.
//
// Each node runs in its own goroutine and processes steps in order. Steps with
// depends_on block only until their named dependencies have completed on all
// applicable nodes — all other work continues in parallel across nodes.
//
// Example with depends_on:
//   master goroutine:  [apt] → [install nfs-server] → [export nfs]  (signals done)
//   node1  goroutine:  [apt] → [install nfs-common] → wait(export nfs) → [mount]
//   node2  goroutine:  [apt] → [install nfs-common] → wait(export nfs) → [mount]
// node1/node2 run apt and nfs-common concurrently with master's nfs-server setup.
func runPlaybook(
	ctx context.Context,
	nodes []inventory.Node,
	pbPath, stateFilePath string,
	force bool,
	logDir string,
	timeout time.Duration,
	concurrency, retries int,
	backoffBase time.Duration,
	backoffFixed bool,
	quiet bool,
	clusterPassword string,
	autoHostsVal string,
) {
	pb, err := playbook.Load(pbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading playbook: %v\n", err)
		os.Exit(1)
	}

	st, err := state.Load(stateFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading state: %v\n", err)
		os.Exit(1)
	}

	sudoPassFn := func(nodeName string) string {
		if p := os.Getenv(nodeName); p != "" {
			return p
		}
		if clusterPassword != "" {
			return clusterPassword
		}
		return os.Getenv("CLUSTER_SUDO_PASS")
	}

	// Build template variables available to all step commands.
	// Hashes are computed on expanded commands, so any variable change
	// (key rotation, new node added to hosts) triggers automatic re-run.
	templateVars := map[string]string{}

	// {{master_pubkey}} — master's public key for passwordless SSH setup.
	if pubkey, err := readMasterPubKey(); err == nil {
		templateVars["master_pubkey"] = pubkey
	} else {
		fmt.Fprintf(os.Stderr, "Warning: could not read master public key: %v\n", err)
	}

	// {{hosts_sync_cmds}} — shell commands to push all cluster entries from
	// master's hosts file to a node. When nodes are added to /etc/hosts,
	// this var changes → hash changes → step re-runs on all nodes automatically.
	hostsSource := clusterHostsFile(pbPath, autoHostsVal)
	if hostsSource != "" {
		templateVars["hosts_sync_cmds"] = buildHostsSyncCmds(hostsSource)
	}

	fmt.Printf("Playbook : %s  (%d steps, %d nodes)\n", pbPath, len(pb.Steps), len(nodes))
	if !force {
		fmt.Printf("State    : %s\n", stateFilePath)
	} else {
		fmt.Println("State    : FORCE — ignoring cached state")
	}
	fmt.Println(strings.Repeat("═", 60))

	// ── Per-step completion signals ────────────────────────────────────────────
	// Each channel is closed when ALL nodes that are supposed to run that step
	// have finished (success, failure, or skip). This unblocks depends_on waiters.
	stepIdx := make(map[string]int, len(pb.Steps))
	for i, s := range pb.Steps {
		stepIdx[s.Name] = i
	}

	signals := make([]chan struct{}, len(pb.Steps))
	pending := make([]int32, len(pb.Steps)) // counts nodes still to process this step
	for i, step := range pb.Steps {
		signals[i] = make(chan struct{})
		var count int32
		for _, n := range nodes {
			if len(step.Roles) == 0 || nodeMatchesRoles(n, step.Roles) {
				count++
			}
		}
		pending[i] = count
		if count == 0 {
			close(signals[i]) // no applicable nodes → immediately done
		}
	}

	// signalDone atomically decrements the pending counter for step i and closes
	// the channel when it reaches zero, unblocking depends_on waiters.
	signalDone := func(i int) {
		if atomic.AddInt32(&pending[i], -1) == 0 {
			close(signals[i])
		}
	}

	// ── Results channel ────────────────────────────────────────────────────────
	results := make(chan nodeResult, len(nodes)*len(pb.Steps))

	// ── Per-node goroutines ────────────────────────────────────────────────────
	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)
		go func(n inventory.Node) {
			defer wg.Done()
			for i, step := range pb.Steps {
				// ── Wait for declared dependencies ─────────────────────────
				for _, depName := range step.DependsOn {
					di := stepIdx[depName]
					select {
					case <-signals[di]:
					case <-ctx.Done():
						return
					}
				}

				// ── Role filter ────────────────────────────────────────────
				if len(step.Roles) > 0 && !nodeMatchesRoles(n, step.Roles) {
					// Not applicable — don't touch pending (wasn't counted).
					continue
				}

				// ── State cache ────────────────────────────────────────────
				// Expand template vars; hash of expanded cmd changes on key rotation.
				expandedCmd := expandVars(step.Command, templateVars)
				h := state.Hash(n.Name, step.Name, expandedCmd)
				if !force && st.Done(n.Name, h) {
					results <- nodeResult{
						node: n.Name, stepName: step.Name, stepIdx: i,
						skipped: true, skipReason: "already done",
					}
					signalDone(i)
					continue
				}

				// ── Execute ────────────────────────────────────────────────
				opts := executor.Options{
					Command:        expandedCmd,
					Sudo:           step.Sudo,
					Timeout:        timeout,
					MaxConcurrency: concurrency,
					MaxRetries:     retries,
					BackoffBase:    backoffBase,
					BackoffFixed:   backoffFixed,
					SudoPassEnvFn:  sudoPassFn,
				}
				nl := executor.RunSingle(ctx, n, opts)
				if nl.Success {
					st.Mark(n.Name, step.Name, h)
				}
				results <- nodeResult{node: n.Name, stepName: step.Name, stepIdx: i, nl: nl}
				signalDone(i)
			}
		}(node)
	}

	// Close results once all node goroutines finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	// ── Collect and print results as they stream in ────────────────────────────
	totalSucceeded := 0
	totalFailed := 0
	var failedNodes []string
	var mu sync.Mutex // guards totals and failedNodes (printer is single-goroutine here)

	// Map step index → logs for final per-step JSON write.
	stepLogs := make(map[int][]logging.NodeLog)
	stepStart := make(map[int]time.Time)

	for r := range results {
		if r.skipped {
			if !quiet {
				fmt.Printf("  %-10s  [%s]  SKIP (%s)\n", r.node, r.stepName, r.skipReason)
			}
			continue
		}

		// Record step start time on first result for that step.
		if _, seen := stepStart[r.stepIdx]; !seen {
			stepStart[r.stepIdx] = r.nl.Start
		}
		stepLogs[r.stepIdx] = append(stepLogs[r.stepIdx], r.nl)

		mu.Lock()
		if r.nl.Success {
			totalSucceeded++
		} else {
			totalFailed++
			if !containsString(failedNodes, r.node) {
				failedNodes = append(failedNodes, r.node)
			}
		}
		mu.Unlock()

		// Save state after every result so partial progress survives a crash.
		if saveErr := st.Save(); saveErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: state save failed: %v\n", saveErr)
		}

		if !quiet {
			icon := "✓"
			if !r.nl.Success {
				icon = "✗"
			}
			fmt.Printf("  %-10s  [%s]  %s  exit=%d  %s\n",
				r.node, r.stepName, icon, r.nl.ExitCode, r.nl.Duration)
			if !r.nl.Success {
				if r.nl.Stderr != "" {
					// Print just the last non-empty stderr line to keep output terse.
					lines := strings.Split(strings.TrimSpace(r.nl.Stderr), "\n")
					last := lines[len(lines)-1]
					fmt.Printf("             stderr: %s\n", last)
				}
				if r.nl.RawError != "" {
					fmt.Printf("             error : %s\n", r.nl.RawError)
				}
			}
		}
	}

	// ── Write per-step JSON logs for failed steps ──────────────────────────────
	var logPaths []string
	for i, logs := range stepLogs {
		hasFail := false
		for _, l := range logs {
			if !l.Success {
				hasFail = true
				break
			}
		}
		if !hasFail {
			continue
		}
		summary := logging.BuildSummary(logs)
		rl := logging.RunLog{
			RunID:     fmt.Sprintf("%s_step%02d", generateRunID(), i+1),
			Command:   pb.Steps[i].Command,
			Sudo:      pb.Steps[i].Sudo,
			StartTime: stepStart[i],
			EndTime:   time.Now(),
			Duration:  time.Since(stepStart[i]).Round(time.Millisecond).String(),
			Summary:   summary,
			Results:   sanitizeLogs(logs),
		}
		if p, err := logging.WriteLog(logDir, rl); err == nil {
			logPaths = append(logPaths, p)
		}
	}

	// ── Final summary ──────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println(strings.Repeat("═", 60))
	if totalFailed == 0 {
		fmt.Printf("PLAYBOOK DONE: %d step-executions succeeded\n", totalSucceeded)
	} else {
		fmt.Printf("PLAYBOOK DONE: %d succeeded  |  %d failed on: %s\n",
			totalSucceeded, totalFailed, strings.Join(failedNodes, ", "))
		fmt.Printf("  Failure logs:\n")
		for _, p := range logPaths {
			fmt.Printf("    %s\n", p)
		}
	}
	fmt.Println(strings.Repeat("═", 60))

	if totalFailed > 0 {
		os.Exit(1)
	}
}

func generateRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	ts := time.Now().Format("20060102T150405")
	return fmt.Sprintf("%s_%s", ts, hex.EncodeToString(b))
}

func printNodeResult(nl logging.NodeLog) {
	status := "✓ OK"
	if !nl.Success {
		status = fmt.Sprintf("✗ FAIL [%s]", nl.ErrorCategory)
	}
	fmt.Printf("\n── %s (%s) %s ──\n", nl.Node, nl.Host, status)
	if nl.Stdout != "" {
		fmt.Printf("  stdout: %s", nl.Stdout)
		if !strings.HasSuffix(nl.Stdout, "\n") {
			fmt.Println()
		}
	}
	if nl.Stderr != "" {
		fmt.Printf("  stderr: %s", nl.Stderr)
		if !strings.HasSuffix(nl.Stderr, "\n") {
			fmt.Println()
		}
	}
	if nl.RawError != "" {
		fmt.Printf("  error : %s\n", nl.RawError)
	}
	if nl.Retries > 0 {
		fmt.Printf("  retries: %d\n", nl.Retries)
	}
	fmt.Printf("  exit=%d  duration=%s\n", nl.ExitCode, nl.Duration)
}

func printSummary(s logging.Summary) {
	fmt.Println()
	fmt.Println(strings.Repeat("═", 60))
	fmt.Printf("SUMMARY: %d total | %d succeeded | %d failed | %d unreachable | %d timed out | %d retried\n",
		s.Total, s.Succeeded, s.Failed, s.Unreachable, s.TimedOut, s.Retried)
	fmt.Println(strings.Repeat("═", 60))
}

func sanitizeLogs(logs []logging.NodeLog) []logging.NodeLog {
	clean := make([]logging.NodeLog, len(logs))
	copy(clean, logs)
	for i := range clean {
		if !clean[i].Success && clean[i].ErrorCategory == "" {
			clean[i].ErrorCategory = cerrors.ClassifyError(clean[i].RawError, clean[i].ExitCode)
		}
	}
	return clean
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// nodeMatchesRoles returns true if the node belongs to any of the given roles (groups).
func nodeMatchesRoles(n inventory.Node, roles []string) bool {
	for _, role := range roles {
		for _, g := range n.Groups {
			if g == role {
				return true
			}
		}
	}
	return false
}

// expandVars replaces {{key}} placeholders in cmd with values from vars.
func expandVars(cmd string, vars map[string]string) string {
	for k, v := range vars {
		cmd = strings.ReplaceAll(cmd, "{{"+k+"}}", v)
	}
	return cmd
}

// readMasterPubKey reads the local SSH public key (ed25519 preferred, rsa fallback).
func readMasterPubKey() (string, error) {
	home, _ := os.UserHomeDir()
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub"} {
		data, err := os.ReadFile(filepath.Join(home, ".ssh", name))
		if err == nil {
			return strings.TrimSpace(string(data)), nil
		}
	}
	return "", fmt.Errorf("no SSH public key found in ~/.ssh/")
}

// loadEnvFile reads a KEY=VALUE file and sets missing environment variables.
// Lines starting with # are comments. Real env vars always take precedence.
func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // missing .env is fine — not an error
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// Strip optional surrounding quotes.
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		// Only set if not already defined by the real environment.
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	return nil
}

// envDefault returns the env var value or fallback if unset.
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// clusterHostsFile returns the best available hosts file path.
// Prefers autoHostsArg (--auto-hosts), then CLUSTER_HOSTS_FILE env var.
func clusterHostsFile(_, autoHostsArg string) string {
	if autoHostsArg != "" {
		return autoHostsArg
	}
	return os.Getenv("CLUSTER_HOSTS_FILE")
}

// buildHostsSyncCmds reads cluster entries (master/nodeN lines) from hostsFile
// and returns a shell command string that ensures each entry is present on
// the target node's /etc/hosts. Adding a new node to master's /etc/hosts
// changes this string, changing the step hash, triggering re-run on all nodes.
func buildHostsSyncCmds(hostsFile string) string {
	data, err := os.ReadFile(hostsFile)
	if err != nil {
		return ""
	}
	var cmds []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, name := range fields[1:] {
			if hosts.IsClusterHost(strings.ToLower(name)) {
				// Normalize to single space so the grep never misses due to
				// tab/multi-space differences in the original /etc/hosts file.
				// Use -E regex to match any whitespace between IP and hostname,
				// preventing duplicates regardless of the source file's formatting.
				normalized := fields[0] + " " + strings.ToLower(name)
				cmds = append(cmds, fmt.Sprintf(
					`grep -qE "^%s[[:space:]]+%s([[:space:]]|$)" /etc/hosts 2>/dev/null || echo %q >> /etc/hosts`,
					fields[0], strings.ToLower(name), normalized,
				))
				break
			}
		}
	}
	if len(cmds) == 0 {
		return "true # no cluster hosts found"
	}
	return strings.Join(cmds, " ; ")
}
