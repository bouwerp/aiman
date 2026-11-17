package console

import (
	"github.com/bouwerp/aiman/internal/harness"
)

// sourcemapsChecking is the browser section's state before serve has
// checked the run's scripts.
const sourcemapsChecking = "checking"

// browserView is the Debug tab's Browser section: the forwarded app URL the
// tester opens in their own browser, whether its scripts carry source maps,
// and, when they do not, what to change. The page renders it as text.
type browserView struct {
	URL        string   `json:"url,omitempty"`
	Port       string   `json:"port,omitempty"`
	Sourcemaps string   `json:"sourcemaps"`
	Method     string   `json:"method,omitempty"`
	Files      []string `json:"files,omitempty"`
	// Warning is set when the scripts have no maps; Fixes are the likely
	// causes, most likely first.
	Warning string   `json:"warning,omitempty"`
	Fixes   []string `json:"fixes,omitempty"`
	// Note explains an unknown or pending answer.
	Note string `json:"note,omitempty"`
}

var sourcemapFixes = []string{
	"Vite: set build.sourcemap: true in vite.config.ts, or make app.run the Vite dev server, which always serves maps.",
	"A plugin that uploads maps and then deletes them (Sentry's filesToDeleteAfterUpload, for one) must not run in app.build; load it only when its CI token is set.",
	"Other bundlers: webpack devtool: 'source-map', esbuild --sourcemap, or sourceMap: true in tsconfig.json for plain tsc output.",
}

const hiddenFix = "The build writes .map files but no comment that points the browser at them (Vite build.sourcemap: 'hidden'). Set build.sourcemap: true."

// browserFor builds the Browser section for a ready run with an app port;
// nil otherwise.
func browserFor(run *harness.Run, fwds []forwardView) *browserView {
	if run == nil || run.State != harness.StateReady {
		return nil
	}
	maps := run.BrowserSourcemaps
	port := webPort(run, maps)
	if port == "" {
		return nil
	}
	v := &browserView{Port: port}
	for _, f := range fwds {
		if f.Name == port {
			v.URL = f.URL
		}
	}
	if maps == nil {
		v.Sourcemaps = sourcemapsChecking
		v.Note = "Checking the app's scripts for source maps."
		return v
	}
	v.Sourcemaps, v.Method, v.Files = maps.State, maps.Method, maps.Files
	switch maps.State {
	case harness.SourcemapsAbsent:
		v.Warning = "DevTools will show only the bundled JavaScript, not your .ts and .tsx files"
		if maps.Detail != "" {
			v.Warning += ": " + maps.Detail
		}
		v.Warning += "."
		if maps.Hidden {
			v.Fixes = append(v.Fixes, hiddenFix)
		}
		v.Fixes = append(v.Fixes, sourcemapFixes...)
	case harness.SourcemapsUnknown:
		v.Note = "Serve could not tell whether the app serves source maps"
		if maps.Detail != "" {
			v.Note += " (" + maps.Detail + ")"
		}
		v.Note += ". If Sources in DevTools lists only bundled files, the build has no maps: in Vite, set build.sourcemap: true."
	}
	return v
}

// webPort is the app port the browser section points at: the one serve read
// the page from, else one named web, else the first app port.
func webPort(run *harness.Run, maps *harness.BrowserSourcemaps) string {
	if maps != nil && maps.Port != "" {
		return maps.Port
	}
	first := ""
	for _, p := range run.Ports {
		if p.Service != harness.ServiceApp || p.Name == harness.RelayDAP {
			continue
		}
		if p.Name == "web" {
			return p.Name
		}
		if first == "" {
			first = p.Name
		}
	}
	return first
}
