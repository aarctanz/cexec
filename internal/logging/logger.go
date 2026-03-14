package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cerrors "github.com/shivamrkm/cexec/internal/errors"
	"github.com/shivamrkm/cexec/internal/ssh"
)

// NodeLog is one entry in the per-run log, enriched with error classification.
type NodeLog struct {
	ssh.Result
	ErrorCategory cerrors.ErrorCategory `json:"error_category,omitempty"`
	Retries       int                   `json:"retries"`
}

// RunLog captures the entire execution run.
type RunLog struct {
	RunID     string    `json:"run_id"`
	Command   string    `json:"command"`
	Sudo      bool      `json:"sudo"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Duration  string    `json:"duration"`
	Summary   Summary   `json:"summary"`
	Results   []NodeLog `json:"results"`
}

// Summary gives a quick overview of the run outcome.
type Summary struct {
	Total       int `json:"total"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Unreachable int `json:"unreachable"`
	TimedOut    int `json:"timed_out"`
	Retried     int `json:"retried"`
}

// BuildSummary computes summary counters from a slice of NodeLogs.
func BuildSummary(logs []NodeLog) Summary {
	s := Summary{Total: len(logs)}
	for _, l := range logs {
		switch {
		case l.Success:
			s.Succeeded++
		case l.ErrorCategory == cerrors.HostUnreachable ||
			l.ErrorCategory == cerrors.SSHConnectionFailed ||
			l.ErrorCategory == cerrors.DNSResolutionFailed:
			s.Unreachable++
		case l.ErrorCategory == cerrors.ConnectionTimeout ||
			l.ErrorCategory == cerrors.CommandTimeout:
			s.TimedOut++
		default:
			s.Failed++
		}
		if l.Retries > 0 {
			s.Retried++
		}
	}
	return s
}

// WriteLog writes the run log as a JSON file under logDir.
func WriteLog(logDir string, rl RunLog) (string, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", fmt.Errorf("creating log dir: %w", err)
	}
	filename := fmt.Sprintf("run_%s.json", rl.RunID)
	path := filepath.Join(logDir, filename)

	data, err := json.MarshalIndent(rl, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshalling log: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing log: %w", err)
	}
	return path, nil
}
