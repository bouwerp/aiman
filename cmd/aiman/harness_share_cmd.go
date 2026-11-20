package main

import (
	"encoding/json"
	"fmt"
)

// harnessShare prints a link that lets a tester reach the session's ready
// run through the gateway.
func harnessShare(h harnessIO, a harnessArgs) error {
	raw, err := harnessCallOut(h, a, "harness.share", a.target())
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var res struct {
		URL string `json:"url"`
		Run string `json:"run"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("decoding share: %w", err)
	}
	_, _ = fmt.Fprintf(h.out, "%s\nrun %s; earlier links of this session no longer work\n", res.URL, res.Run)
	return nil
}

// harnessUnshare ends the session's share links.
func harnessUnshare(h harnessIO, a harnessArgs) error {
	raw, err := harnessCallOut(h, a, "harness.unshare", a.target())
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var res struct {
		Revoked int `json:"revoked"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("decoding unshare: %w", err)
	}
	_, _ = fmt.Fprintf(h.out, "revoked %d share link(s)\n", res.Revoked)
	return nil
}
