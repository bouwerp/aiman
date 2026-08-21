package main

import (
	"fmt"
	"strings"
)

type globalFlags struct {
	Debug     bool
	DebugPath string
	Rest      []string
}

func parseGlobalFlags(args []string) (globalFlags, error) {
	out := globalFlags{Rest: make([]string, 0, len(args))}
	for _, a := range args {
		switch {
		case a == "--debug":
			out.Debug = true
		case strings.HasPrefix(a, "--debug="):
			p := strings.TrimSpace(strings.TrimPrefix(a, "--debug="))
			if p == "" {
				return globalFlags{}, fmt.Errorf("--debug= requires a file path")
			}
			out.Debug = true
			out.DebugPath = p
		default:
			out.Rest = append(out.Rest, a)
		}
	}
	return out, nil
}
