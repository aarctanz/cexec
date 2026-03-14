package state

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// Entry records a successful step execution on a node.
type Entry struct {
	Node      string    `json:"node"`
	StepName  string    `json:"step_name"`
	Hash      string    `json:"hash"`
	Timestamp time.Time `json:"timestamp"`
}

// State holds per-node, per-step execution state.
type State struct {
	mu      sync.Mutex
	path    string
	entries map[string]Entry // key: "node:hash"
}

// Load reads state from path, returning empty state if the file doesn't exist.
func Load(path string) (*State, error) {
	s := &State{
		path:    path,
		entries: make(map[string]Entry),
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state file %s: %w", path, err)
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		// Corrupt state — start fresh rather than hard-fail.
		fmt.Fprintf(os.Stderr, "Warning: state file %s is corrupt, starting fresh\n", path)
		return s, nil
	}
	for _, e := range entries {
		s.entries[e.Node+":"+e.Hash] = e
	}
	return s, nil
}

// Hash computes a short, deterministic identifier for a node+step combination.
// Changing the command text changes the hash, forcing a re-run.
func Hash(nodeName, stepName, command string) string {
	h := sha256.Sum256([]byte(nodeName + "|" + stepName + "|" + command))
	return fmt.Sprintf("%x", h[:8])
}

// Done reports whether this exact step (by hash) already succeeded on nodeName.
func (s *State) Done(nodeName, hash string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[nodeName+":"+hash]
	return ok
}

// Mark records that a step succeeded on a node.
func (s *State) Mark(nodeName, stepName, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[nodeName+":"+hash] = Entry{
		Node:      nodeName,
		StepName:  stepName,
		Hash:      hash,
		Timestamp: time.Now(),
	}
}

// Save persists state to disk atomically (write-then-rename).
func (s *State) Save() error {
	s.mu.Lock()
	entries := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Node != entries[j].Node {
			return entries[i].Node < entries[j].Node
		}
		return entries[i].StepName < entries[j].StepName
	})

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling state: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}
	return os.Rename(tmp, s.path)
}
