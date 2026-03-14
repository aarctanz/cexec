package hosts

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/shivamrkm/cexec/internal/inventory"
)

var clusterHostRe = regexp.MustCompile(`^(master|node\d+)$`)

// IsClusterHost reports whether the given hostname matches the cluster naming
// convention: "master" or "nodeN" (where N is one or more digits).
func IsClusterHost(name string) bool {
	return clusterHostRe.MatchString(name)
}

// LoadInventory parses an /etc/hosts-style file and returns Node entries for
// any line whose hostname matches "master" or "nodeN". The given user and port
// are applied to every discovered node.
func LoadInventory(hostsFile, user string, port int) (*inventory.Inventory, error) {
	if port == 0 {
		port = 22
	}
	if user == "" {
		user = "hpc"
	}

	f, err := os.Open(hostsFile)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", hostsFile, err)
	}
	defer f.Close()

	var nodes []inventory.Node
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ip := fields[0]
		// A single line may have multiple hostnames; check all.
		for _, name := range fields[1:] {
			name = strings.ToLower(name)
			if clusterHostRe.MatchString(name) && !seen[name] {
				seen[name] = true
				group := "compute"
				if name == "master" {
					group = "control"
				}
				nodes = append(nodes, inventory.Node{
					Name:   name,
					Host:   ip,
					User:   user,
					Port:   port,
					Groups: []string{group},
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", hostsFile, err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no cluster nodes (master/nodeN) found in %s", hostsFile)
	}
	return &inventory.Inventory{Nodes: nodes}, nil
}
