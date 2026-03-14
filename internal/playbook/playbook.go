package playbook

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Step is one command in the playbook.
// Roles filters which nodes run this step by group name (e.g. "control", "compute").
// If Roles is empty the step runs on all targeted nodes.
// DependsOn lists step names that must complete (on their applicable nodes) before
// this step starts. Used to express cross-node ordering (e.g. mount NFS only after
// master has exported it), while unrelated steps on different nodes run in parallel.
type Step struct {
	Name      string   `yaml:"name"`
	Command   string   `yaml:"command"`
	Sudo      bool     `yaml:"sudo"`
	Roles     []string `yaml:"roles"`      // optional: limit to nodes in these groups
	DependsOn []string `yaml:"depends_on"` // optional: step names that must finish first
}

// Playbook holds an ordered list of steps.
type Playbook struct {
	Steps []Step `yaml:"steps"`
}

// Load reads and parses a YAML playbook file.
func Load(path string) (*Playbook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading playbook %s: %w", path, err)
	}
	var pb Playbook
	if err := yaml.Unmarshal(data, &pb); err != nil {
		return nil, fmt.Errorf("parsing playbook %s: %w", path, err)
	}

	nameSet := make(map[string]bool, len(pb.Steps))
	for i, s := range pb.Steps {
		if s.Name == "" {
			return nil, fmt.Errorf("step %d has no name", i+1)
		}
		if s.Command == "" {
			return nil, fmt.Errorf("step %q has no command", s.Name)
		}
		nameSet[s.Name] = true
	}
	for _, s := range pb.Steps {
		for _, dep := range s.DependsOn {
			if !nameSet[dep] {
				return nil, fmt.Errorf("step %q depends_on unknown step %q", s.Name, dep)
			}
		}
	}

	// Detect dependency cycles using depth-first search.
	// A cycle (A depends_on B depends_on A) would cause all goroutines to
	// block forever — catch it here at load time with a clear error message.
	type mark int
	const (
		unvisited mark = iota
		inProgress
		visited
	)
	state := make(map[string]mark, len(pb.Steps))
	deps := make(map[string][]string, len(pb.Steps))
	for _, s := range pb.Steps {
		deps[s.Name] = s.DependsOn
	}
	var dfs func(name string) error
	dfs = func(name string) error {
		switch state[name] {
		case inProgress:
			return fmt.Errorf("dependency cycle detected involving step %q", name)
		case visited:
			return nil
		}
		state[name] = inProgress
		for _, dep := range deps[name] {
			if err := dfs(dep); err != nil {
				return err
			}
		}
		state[name] = visited
		return nil
	}
	for _, s := range pb.Steps {
		if err := dfs(s.Name); err != nil {
			return nil, err
		}
	}

	if len(pb.Steps) == 0 {
		return nil, fmt.Errorf("playbook %s has no steps", path)
	}
	return &pb, nil
}
