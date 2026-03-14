package executor

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	cerrors "github.com/shivamrkm/cexec/internal/errors"
	"github.com/shivamrkm/cexec/internal/inventory"
	"github.com/shivamrkm/cexec/internal/logging"
	"github.com/shivamrkm/cexec/internal/ssh"
)

// Options controls execution behaviour.
type Options struct {
	Command        string
	Sudo           bool
	SudoPassEnvFn  func(nodeName string) string // returns password from env
	Timeout        time.Duration
	MaxConcurrency int // 0 = unlimited
	MaxRetries     int // 0 = no retry
	BackoffBase    time.Duration
	BackoffFixed   bool // true = fixed delay, false = exponential
}

// Run executes the command on all nodes in parallel, respecting concurrency
// limits and the parent context for graceful shutdown.
func Run(ctx context.Context, nodes []inventory.Node, opts Options) []logging.NodeLog {
	results := make([]logging.NodeLog, len(nodes))

	// Semaphore for concurrency control.
	var sem chan struct{}
	if opts.MaxConcurrency > 0 {
		sem = make(chan struct{}, opts.MaxConcurrency)
	}

	var wg sync.WaitGroup

	for i, node := range nodes {
		wg.Add(1)
		go func(idx int, n inventory.Node) {
			defer wg.Done()

			// Acquire semaphore slot.
			if sem != nil {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					results[idx] = logging.NodeLog{
						Result: ssh.Result{
							Node:     n.Name,
							Host:     n.Host,
							Start:    time.Now(),
							End:      time.Now(),
							Duration: "0s",
							RawError: "cancelled before execution: " + ctx.Err().Error(),
							ExitCode: -1,
						},
						ErrorCategory: cerrors.CommandTimeout,
					}
					return
				}
			}

			results[idx] = executeWithRetry(ctx, n, opts)
		}(i, node)
	}

	wg.Wait()
	return results
}

// executeWithRetry runs the command once, then retries on failure up to
// opts.MaxRetries times with configurable backoff.
func executeWithRetry(ctx context.Context, node inventory.Node, opts Options) logging.NodeLog {
	var retries int

	for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
		if attempt > 0 {
			retries++
			delay := computeBackoff(attempt, opts.BackoffBase, opts.BackoffFixed)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return logging.NodeLog{
					Result: ssh.Result{
						Node:     node.Name,
						Host:     node.Host,
						Start:    time.Now(),
						End:      time.Now(),
						Duration: "0s",
						RawError: "cancelled during retry backoff: " + ctx.Err().Error(),
						ExitCode: -1,
					},
					ErrorCategory: cerrors.CommandTimeout,
					Retries:       retries,
				}
			}
		}

		// Per-command timeout context.
		execCtx := ctx
		var cancel context.CancelFunc
		if opts.Timeout > 0 {
			execCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		}

		sudoPass := ""
		if opts.Sudo && opts.SudoPassEnvFn != nil {
			sudoPass = opts.SudoPassEnvFn(node.Name)
		}

		result := ssh.RunCommand(execCtx, node, opts.Command, opts.Sudo, sudoPass)
		if cancel != nil {
			cancel()
		}

		nl := logging.NodeLog{
			Result:  result,
			Retries: retries,
		}

		if result.Success {
			return nl
		}

		// Classify error.
		nl.ErrorCategory = cerrors.ClassifyError(result.RawError, result.ExitCode)

		// If this was the last attempt, return the result as-is.
		if attempt == opts.MaxRetries {
			return nl
		}

		// Only retry on transient-looking errors.
		if !isRetryable(nl.ErrorCategory) {
			return nl
		}
	}

	// Unreachable, but satisfy the compiler.
	return logging.NodeLog{}
}

func computeBackoff(attempt int, base time.Duration, fixed bool) time.Duration {
	if base == 0 {
		base = 2 * time.Second
	}
	if fixed {
		return base
	}
	// Exponential: base * 2^(attempt-1), capped at 60s.
	d := time.Duration(float64(base) * math.Pow(2, float64(attempt-1)))
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

func isRetryable(cat cerrors.ErrorCategory) bool {
	switch cat {
	case cerrors.ConnectionTimeout,
		cerrors.CommandTimeout,
		cerrors.SSHConnectionFailed,
		cerrors.HostUnreachable,
		cerrors.DNSResolutionFailed:
		return true
	default:
		return false
	}
}

// RunSingle executes the command on a single node with the configured retry policy.
// Exported for use by the concurrent playbook runner.
func RunSingle(ctx context.Context, node inventory.Node, opts Options) logging.NodeLog {
	return executeWithRetry(ctx, node, opts)
}

// DryRun prints what would happen without executing anything.
func DryRun(nodes []inventory.Node, opts Options) {
	fmt.Println("=== DRY RUN ===")
	fmt.Printf("Command : %s\n", opts.Command)
	fmt.Printf("Sudo    : %v\n", opts.Sudo)
	fmt.Printf("Timeout : %s\n", opts.Timeout)
	fmt.Printf("Retries : %d\n", opts.MaxRetries)
	fmt.Printf("Targets : %d node(s)\n\n", len(nodes))

	for i, n := range nodes {
		groups := "none"
		if len(n.Groups) > 0 {
			groups = fmt.Sprintf("%v", n.Groups)
		}
		fmt.Printf("  [%d] %s  (%s@%s:%d)  groups=%s\n",
			i+1, n.Name, n.User, n.Host, n.Port, groups)
	}
	fmt.Println("\nNo commands will be executed.")
}
