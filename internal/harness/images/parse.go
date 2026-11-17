package images

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// ParseTrivy reads a Trivy JSON report and returns the sorted, distinct
// critical and high vulnerability ids.
func ParseTrivy(b []byte) (critical, high []string, err error) {
	var rep struct {
		Results []struct {
			Vulnerabilities []struct {
				VulnerabilityID string `json:"VulnerabilityID"`
				Severity        string `json:"Severity"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, nil, fmt.Errorf("read the trivy report: %w", err)
	}
	for _, r := range rep.Results {
		for _, v := range r.Vulnerabilities {
			switch strings.ToUpper(v.Severity) {
			case "CRITICAL":
				critical = append(critical, v.VulnerabilityID)
			case "HIGH":
				high = append(high, v.VulnerabilityID)
			}
		}
	}
	return distinct(critical), distinct(high), nil
}

// ParseTrivyDBTime reads the vulnerability DB's update time from
// `trivy version --format json`.
func ParseTrivyDBTime(b []byte) string {
	var v struct {
		VulnerabilityDB struct {
			UpdatedAt string `json:"UpdatedAt"`
		} `json:"VulnerabilityDB"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return v.VulnerabilityDB.UpdatedAt
}

// ParseClamAV reads `clamscan --infected` output: "<path>: <sig> FOUND".
// Hits read "clamav:<sig> <path under dir>".
func ParseClamAV(out []byte, dir string) []string {
	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, " FOUND") {
			continue
		}
		i := strings.LastIndex(line, ": ")
		if i < 0 {
			continue
		}
		sig := strings.TrimSuffix(line[i+2:], " FOUND")
		hits = append(hits, "clamav:"+sig+" "+relTo(dir, line[:i]))
	}
	return distinct(hits)
}

// ParseYARA reads yara output: "<rule> <path>". Hits read
// "yara:<rule> <path under dir>".
func ParseYARA(out []byte, dir string) []string {
	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		rule, path, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || rule == "" {
			continue
		}
		hits = append(hits, "yara:"+rule+" "+relTo(dir, path))
	}
	return distinct(hits)
}

func relTo(dir, path string) string {
	if rel, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return "layer " + rel
	}
	return path
}

// ParseOSVMal reads osv-scanner's JSON and returns the MAL- ids among the
// vulnerabilities and their aliases.
func ParseOSVMal(b []byte) ([]string, error) {
	var rep struct {
		Results []struct {
			Packages []struct {
				Vulnerabilities []struct {
					ID      string   `json:"id"`
					Aliases []string `json:"aliases"`
				} `json:"vulnerabilities"`
			} `json:"packages"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, fmt.Errorf("read the osv-scanner report: %w", err)
	}
	var ids []string
	for _, r := range rep.Results {
		for _, p := range r.Packages {
			for _, v := range p.Vulnerabilities {
				for _, id := range append([]string{v.ID}, v.Aliases...) {
					if strings.HasPrefix(id, "MAL-") {
						ids = append(ids, id)
					}
				}
			}
		}
	}
	return distinct(ids), nil
}

func distinct(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}
