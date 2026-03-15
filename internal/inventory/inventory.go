package inventory

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Save writes the inventory to a YAML file.
// Existing comments in the file are not preserved — this is a full rewrite.
func Save(path string, inv *Inventory) error {
	const header = "# inventory.yaml — managed by cexec\n" +
		"# Nodes are auto-synced from /etc/hosts when CLUSTER_HOSTS_FILE is set.\n" +
		"# Edit groups, port, or user here to override per-node defaults.\n" +
		"# master is always group [control]; all other nodes default to [compute].\n\n"
	data, err := yaml.Marshal(inv)
	if err != nil {
		return fmt.Errorf("marshalling inventory: %w", err)
	}
	return os.WriteFile(path, append([]byte(header), data...), 0644)
}

// MergeGroups overlays group/port/user metadata from overlay (inventory.yaml)
// onto base (auto-discovered from /etc/hosts). It returns:
//
//   - merged: combined node list ready for execution. Overlay-only nodes
//     (in inventory.yaml but not /etc/hosts) are included using their stored IPs.
//   - addedToInv: nodes that were in base but not overlay — caller should
//     append these to inventory.yaml so the two sources stay in sync.
//   - missingFromHosts: nodes that were in overlay but not base — they are
//     included in merged via their inventory.yaml IP, but caller should warn
//     the user to add them to /etc/hosts.
func MergeGroups(base, overlay *Inventory) (merged *Inventory, addedToInv, missingFromHosts []Node) {
	overlayMap := make(map[string]Node, len(overlay.Nodes))
	for _, n := range overlay.Nodes {
		overlayMap[n.Name] = n
	}

	baseSet := make(map[string]bool, len(base.Nodes))
	merged = &Inventory{Nodes: make([]Node, 0, len(base.Nodes)+len(overlay.Nodes))}

	for _, n := range base.Nodes {
		baseSet[n.Name] = true
		if ov, ok := overlayMap[n.Name]; ok {
			// Overlay wins for group/port/user — /etc/hosts IP is authoritative.
			n.Groups = ov.Groups
			if ov.Port != 0 {
				n.Port = ov.Port
			}
			if ov.User != "" {
				n.User = ov.User
			}
		} else {
			// New node found in /etc/hosts that inventory.yaml doesn't know about yet.
			addedToInv = append(addedToInv, n)
		}
		merged.Nodes = append(merged.Nodes, n)
	}

	// Nodes only in inventory.yaml: include them (IP is stored there) but flag them.
	for _, n := range overlay.Nodes {
		if !baseSet[n.Name] {
			missingFromHosts = append(missingFromHosts, n)
			merged.Nodes = append(merged.Nodes, n)
		}
	}

	return merged, addedToInv, missingFromHosts
}

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
