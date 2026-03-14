package errors

import "strings"

// ErrorCategory represents a normalized error classification.
type ErrorCategory string

const (
	SSHConnectionFailed  ErrorCategory = "ssh_connection_failed"
	SSHAuthFailed        ErrorCategory = "ssh_auth_failed"
	HostUnreachable      ErrorCategory = "host_unreachable"
	DNSResolutionFailed  ErrorCategory = "dns_resolution_failed"
	ConnectionTimeout    ErrorCategory = "connection_timeout"
	CommandTimeout       ErrorCategory = "command_timeout"
	SudoAuthFailed       ErrorCategory = "sudo_auth_failed"
	SudoPermissionDenied ErrorCategory = "sudo_permission_denied"
	RemoteCommandFailed  ErrorCategory = "remote_command_failed"
	ShellNotFound        ErrorCategory = "shell_not_found"
	BinaryNotFound       ErrorCategory = "binary_not_found"
	NonZeroExit          ErrorCategory = "non_zero_exit"
	UnknownError         ErrorCategory = "unknown_error"
)

// ClassifyError inspects the raw error string and exit code to return a
// normalized category. When the heuristic is uncertain it falls back to
// unknown_error so that the raw message is always the authoritative detail.
func ClassifyError(rawErr string, exitCode int) ErrorCategory {
	low := strings.ToLower(rawErr)

	switch {
	// DNS
	case strings.Contains(low, "no such host"),
		strings.Contains(low, "name resolution"),
		strings.Contains(low, "dns"):
		return DNSResolutionFailed

	// Timeout (check before generic connection errors)
	case strings.Contains(low, "i/o timeout"),
		strings.Contains(low, "deadline exceeded"),
		strings.Contains(low, "context deadline"):
		return ConnectionTimeout

	// SSH auth
	case strings.Contains(low, "unable to authenticate"),
		strings.Contains(low, "no supported methods"),
		strings.Contains(low, "permission denied (publickey"):
		return SSHAuthFailed

	// SSH connection
	case strings.Contains(low, "connection refused"),
		strings.Contains(low, "ssh:"):
		return SSHConnectionFailed

	// Host unreachable
	case strings.Contains(low, "no route to host"),
		strings.Contains(low, "host is unreachable"),
		strings.Contains(low, "network is unreachable"):
		return HostUnreachable

	// Sudo
	case strings.Contains(low, "incorrect password"),
		strings.Contains(low, "sudo: a password is required"):
		return SudoAuthFailed
	case strings.Contains(low, "not in the sudoers"),
		strings.Contains(low, "not allowed to execute"):
		return SudoPermissionDenied

	// Shell / binary
	case strings.Contains(low, "sh: not found"),
		strings.Contains(low, "bash: not found"),
		strings.Contains(low, "/bin/sh: no such file"):
		return ShellNotFound
	case strings.Contains(low, "command not found"),
		strings.Contains(low, "no such file or directory") && exitCode == 127:
		return BinaryNotFound

	// Command timeout (set by our own context cancel)
	case strings.Contains(low, "signal: killed"),
		strings.Contains(low, "command timed out"):
		return CommandTimeout

	// Generic non-zero exit
	case exitCode != 0:
		return NonZeroExit

	default:
		return UnknownError
	}
}
