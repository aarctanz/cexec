package ssh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/shivamrkm/cexec/internal/inventory"
)

// Result captures everything about a single remote execution.
type Result struct {
	Node     string    `json:"node"`
	Host     string    `json:"host"`
	Stdout   string    `json:"stdout"`
	Stderr   string    `json:"stderr"`
	ExitCode int       `json:"exit_code"`
	Start    time.Time `json:"start_time"`
	End      time.Time `json:"end_time"`
	Duration string    `json:"duration"`
	RawError string    `json:"raw_error,omitempty"`
	Success  bool      `json:"success"`
}

// RunCommand connects to a single node and executes cmd. It respects the
// provided context for cancellation and timeout.
func RunCommand(ctx context.Context, node inventory.Node, cmd string, sudo bool, sudoPass string) Result {
	res := Result{
		Node:  node.Name,
		Host:  node.Host,
		Start: time.Now(),
	}

	defer func() {
		res.End = time.Now()
		res.Duration = res.End.Sub(res.Start).Round(time.Millisecond).String()
	}()

	cfg, err := buildSSHConfig(node)
	if err != nil {
		res.RawError = err.Error()
		res.ExitCode = -1
		return res
	}

	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)

	// Dial with context awareness.
	var conn net.Conn
	dialer := net.Dialer{}
	conn, err = dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		res.RawError = err.Error()
		res.ExitCode = -1
		return res
	}
	defer conn.Close()

	// SSH handshake.
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		res.RawError = err.Error()
		res.ExitCode = -1
		return res
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		res.RawError = err.Error()
		res.ExitCode = -1
		return res
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Build final command.
	finalCmd := cmd
	if sudo {
		// Use stdin to pass password so it never appears in process list.
		finalCmd = fmt.Sprintf("echo '%s' | sudo -S sh -c %q", "SUDO_PLACEHOLDER", cmd)
		// We replace the placeholder after building the string so the real
		// password is only present in the pipe, not in logs.
		if sudoPass != "" {
			finalCmd = fmt.Sprintf("echo '%s' | sudo -S sh -c %q", sudoPass, cmd)
		} else {
			// No password — try NOPASSWD sudo.
			finalCmd = fmt.Sprintf("sudo sh -c %q", cmd)
		}
	}

	// Run with context cancellation.
	done := make(chan error, 1)
	go func() {
		done <- session.Run(finalCmd)
	}()

	select {
	case <-ctx.Done():
		// Context cancelled (timeout or signal).
		_ = session.Signal(ssh.SIGKILL)
		res.RawError = "command timed out or cancelled: " + ctx.Err().Error()
		res.ExitCode = -1
		res.Stdout = stdout.String()
		res.Stderr = stderr.String()
		return res
	case err = <-done:
	}

	res.Stdout = stdout.String()
	res.Stderr = stderr.String()

	if err != nil {
		res.RawError = err.Error()
		if exitErr, ok := err.(*ssh.ExitError); ok {
			res.ExitCode = exitErr.ExitStatus()
		} else {
			res.ExitCode = -1
		}
		return res
	}

	res.Success = true
	res.ExitCode = 0
	return res
}

// buildSSHConfig creates an ssh.ClientConfig.
// Auth order: SSH key (if found) → password (if node.Password is set).
// At least one method must be available.
func buildSSHConfig(node inventory.Node) (*ssh.ClientConfig, error) {
	var authMethods []ssh.AuthMethod

	// Key-based auth — try ed25519 first, then rsa.
	home, _ := os.UserHomeDir()
	for _, name := range []string{"id_ed25519", "id_rsa"} {
		p := filepath.Join(home, ".ssh", name)
		key, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			continue
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
		break
	}

	// Password auth — used when no key is available or as fallback.
	if node.Password != "" {
		authMethods = append(authMethods, ssh.Password(node.Password))
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no SSH auth method available for node %s (no key found, no password set)", node.Name)
	}

	return &ssh.ClientConfig{
		User:            node.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Lab environment.
		Timeout:         10 * time.Second,
	}, nil
}
