package ssh

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// ScanKnownHosts parses ~/.ssh/known_hosts and /etc/ssh/known_hosts for hostnames.
func ScanKnownHosts() []string {
	hostsMap := make(map[string]struct{})

	paths := []string{
		filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts"),
		"/etc/ssh/known_hosts",
	}

	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			// known_hosts format: <hostname>,<ip> <keytype> <key>
			// Some entries might be hashed (starts with |1|), skip those for now.
			if strings.HasPrefix(line, "|1|") {
				continue
			}

			parts := strings.Fields(line)
			if len(parts) > 0 {
				hostPart := parts[0]
				// Handle multiple hostnames like "host1,host2"
				for _, h := range strings.Split(hostPart, ",") {
					// Basic validation: skip empty or clearly invalid
					if h != "" && !strings.Contains(h, "[") { // Skip [host]:port for now
						hostsMap[h] = struct{}{}
					}
				}
			}
		}
	}

	hosts := make([]string, 0, len(hostsMap))
	for h := range hostsMap {
		hosts = append(hosts, h)
	}
	return hosts
}
