package inventory

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Node represents a single cluster machine.
type Node struct {
	Name     string   `yaml:"name"   json:"name"`
	Host     string   `yaml:"host"   json:"host"`
	User     string   `yaml:"user"   json:"user"`
	Port     int      `yaml:"port"   json:"port"`
	Groups   []string `yaml:"groups" json:"groups"`
	// Password is never stored in YAML — injected at runtime from env.
	Password string   `yaml:"-"      json:"-"`
}

// Inventory is the top-level inventory file structure.
type Inventory struct {
	Nodes []Node `yaml:"nodes"`
}

// Load reads and parses a YAML inventory file.
func Load(path string) (*Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading inventory %s: %w", path, err)
	}
	var inv Inventory
	if err := yaml.Unmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("parsing inventory %s: %w", path, err)
	}
	// Apply defaults.
	for i := range inv.Nodes {
		if inv.Nodes[i].Port == 0 {
			inv.Nodes[i].Port = 22
		}
		if inv.Nodes[i].User == "" {
			inv.Nodes[i].User = "root"
		}
	}
	return &inv, nil
}

// Select returns nodes matching the selector while honouring exclusions.
//
//	selector: "all", a group name, or comma-separated node names.
//	exclude:  comma-separated node names to skip (may be empty).
func Select(inv *Inventory, selector string, exclude string) ([]Node, error) {
	excludeSet := make(map[string]bool)
	if exclude != "" {
		for _, n := range strings.Split(exclude, ",") {
			excludeSet[strings.TrimSpace(n)] = true
		}
	}

	var candidates []Node

	switch {
	case selector == "all":
		candidates = inv.Nodes

	case containsComma(selector):
		// Explicit list.
		wanted := make(map[string]bool)
		for _, n := range strings.Split(selector, ",") {
			wanted[strings.TrimSpace(n)] = true
		}
		for _, n := range inv.Nodes {
			if wanted[n.Name] {
				candidates = append(candidates, n)
			}
		}

	default:
		// Treat as group name.
		for _, n := range inv.Nodes {
			if slices.Contains(n.Groups, selector) {
				candidates = append(candidates, n)
			}
		}
	}

	// Apply exclusions.
	var result []Node
	for _, n := range candidates {
		if !excludeSet[n.Name] {
			result = append(result, n)
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no nodes matched selector %q (exclude: %q)", selector, exclude)
	}
	return result, nil
}

func containsComma(s string) bool {
	return strings.Contains(s, ",")
}
