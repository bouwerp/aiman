package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
)

type harnessDebugReply struct {
	Debug harness.DebugState `json:"debug"`
}

// harnessDebug is `aiman harness debug [status|take|release]`: who holds a
// debug run's debugger, and the operator's take over and release.
func harnessDebug(h harnessIO, a harnessArgs) error {
	op := harness.DebugStatus
	if len(a.pos) == 1 {
		op = a.pos[0]
	}
	if op != harness.DebugStatus && op != harness.DebugTake && op != harness.DebugRelease {
		_, _ = fmt.Fprintf(h.errOut, "unknown debug op %q: status, take or release\n", op)
		return harnessUsage(h.errOut)
	}
	params := a.target()
	params["op"] = op
	raw, err := harnessCallOut(h, a, "harness.debug", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var r harnessDebugReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	writeHarnessDebug(h, r.Debug)
	return nil
}

func writeHarnessDebug(h harnessIO, st harness.DebugState) {
	w := h.out
	if st.Dropped != nil {
		_, _ = fmt.Fprintf(w, "closed %s (%d connections)\n", holderName(st.Dropped.ClientName, st.Dropped.ClientID), st.Dropped.Connections)
	}
	if st.Launch != nil {
		_, _ = fmt.Fprintf(w, "run %s: %s (%s) on host 127.0.0.1:%d\n", st.Run, st.Launch.Adapter, st.Launch.Lang, st.HostPort)
	}
	if st.Holder == nil {
		_, _ = fmt.Fprintln(w, "debugger: free")
	} else {
		_, _ = fmt.Fprintf(w, "debugger: held by %s since %s, %d connections\n",
			holderName(st.Holder.ClientName, st.Holder.ClientID), st.Holder.Since.Local().Format(time.Kitchen), st.Holder.Connections)
	}
	_, _ = fmt.Fprintf(w, "refused %d\n", st.Refused)
	_, _ = fmt.Fprintln(w, "From the laptop, aiman harness console forwards the dap port and shows a launch.json entry.")
}

func holderName(name, id string) string {
	switch {
	case name != "":
		return name
	case id != "":
		return id
	}
	return "a client"
}

// harnessDebugCheck is `aiman harness check <name> --debug [--take]`: the
// check runs as the run's debug target, and the command polls harness.status
// until it ends, then prints its result as a plain check does. Interrupting
// the command leaves the check running.
func harnessDebugCheck(h harnessIO, a harnessArgs) error {
	if len(a.pos) != 1 {
		_, _ = fmt.Fprintln(h.errOut, "name the check to debug")
		return harnessUsage(h.errOut)
	}
	params := a.target()
	params["name"], params["debug"] = a.pos[0], true
	if a.take {
		params["take"] = true
	}
	raw, err := harnessCallOut(h, a, "harness.check", params)
	if err != nil {
		return err
	}
	var started struct {
		Run        string              `json:"run"`
		DebugCheck *harness.DebugCheck `json:"debug_check"`
	}
	if err := json.Unmarshal(raw, &started); err != nil {
		return err
	}
	if !a.json {
		msg := fmt.Sprintf("check %s waits for a debugger on run %s: open the Debug tab of aiman harness console", a.pos[0], started.Run)
		if d := started.DebugCheck; d != nil {
			msg += fmt.Sprintf(" before %s", d.AttachBy.Local().Format(time.Kitchen))
		}
		_, _ = fmt.Fprintln(h.out, msg+". Ctrl+C here leaves it running.")
	}
	var run harness.Run
	for {
		h.sleep(harnessPollEvery)
		if raw, err = harnessCallOut(h, a, "harness.status", map[string]any{"run": started.Run}); err != nil {
			return err
		}
		if run, err = decodeRun(raw); err != nil {
			return err
		}
		if run.DebugCheck == nil || run.DebugCheck.Name != a.pos[0] || !run.Live() {
			break
		}
	}
	var c harness.CheckRun
	for _, x := range run.Checks {
		if x.Name == a.pos[0] {
			c = x
		}
	}
	if a.json {
		b, err := json.Marshal(map[string]any{"type": "harness_check", "run": run.ID, "debug": true, "checks": []harness.CheckRun{c}})
		if err != nil {
			return err
		}
		return printHarnessJSON(h.out, b)
	}
	writeHarnessChecks(h.out, []harness.CheckRun{c})
	if c.Error != "" {
		_, _ = fmt.Fprintf(h.out, "    %s\n", c.Error)
	}
	if c.Exit == nil || *c.Exit != 0 {
		return errHarnessCheckFailed
	}
	return nil
}
