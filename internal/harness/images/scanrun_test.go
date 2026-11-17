package images

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeTools answers the scanner tools from canned outputs.
type fakeTools struct {
	trivyReport string
	clamOut     string
	osvOut      string
	calls       []string
}

func (f *fakeTools) run(_ context.Context, name string, args, _ []string) ([]byte, int, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	out := func() string {
		if i := slices.Index(args, "--output"); i >= 0 {
			return args[i+1]
		}
		return ""
	}
	switch name {
	case "trivy":
		switch args[0] {
		case "image":
			return nil, 0, os.WriteFile(out(), []byte(`{"bomFormat":"CycloneDX"}`), 0o600)
		case "sbom":
			return nil, 0, os.WriteFile(out(), []byte(f.trivyReport), 0o600)
		case "version":
			return []byte(`{"VulnerabilityDB":{"UpdatedAt":"2026-11-16T00:00:00Z"}}`), 0, nil
		}
	case "clamscan":
		if f.clamOut != "" {
			return []byte(f.clamOut), 1, nil
		}
		return nil, 0, nil
	case "osv-scanner":
		return []byte(f.osvOut), 1, nil
	}
	return nil, 2, nil
}

// quarantineOf fetches img from a fake registry into a layout.
func quarantineOf(t *testing.T, img testImage) string {
	t.Helper()
	reg := newFakeRegistry(t)
	reg.push("o/app", "1", img)
	r := reg.registry()
	res, err := r.Resolve(context.Background(), reg.host()+"/o/app:1", HostPlatform())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "q")
	if err := Fetch(context.Background(), r, res, dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunScanCVE(t *testing.T) {
	in := quarantineOf(t, newTestImage(t, layerTar(t, true, tarEntry{Name: "a", Body: "a"})))
	scratch := t.TempDir()
	tools := &fakeTools{trivyReport: `{"Results":[{"Vulnerabilities":[
		{"VulnerabilityID":"CVE-2","Severity":"HIGH"},{"VulnerabilityID":"CVE-1","Severity":"CRITICAL"},
		{"VulnerabilityID":"CVE-2","Severity":"HIGH"},{"VulnerabilityID":"CVE-3","Severity":"LOW"}]}]}`}
	res, err := RunScan(context.Background(), ScanPlan{Stage: ScanStageCVE, Tools: []string{ToolTrivy}, In: in, Scratch: scratch, DB: "/db", MaxBytes: 1 << 20}, tools.run)
	if err != nil || res.Error != "" {
		t.Fatalf("RunScan = %+v, %v", res, err)
	}
	if !slices.Equal(res.Critical, []string{"CVE-1"}) || !slices.Equal(res.High, []string{"CVE-2"}) || res.TrivyDB == "" {
		t.Fatalf("cve result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(scratch, "out", "sbom.cdx.json")); err != nil {
		t.Fatal("no SBOM written")
	}
	if _, err := os.Stat(filepath.Join(scratch, "out", "cve.json")); err != nil {
		t.Fatal("no stage result written")
	}
}

func TestRunScanStatic(t *testing.T) {
	img := newTestImage(t,
		layerTar(t, true, tarEntry{Name: "bin/x", Body: "x"}),
		layerTar(t, false, tarEntry{Name: "bin/.wh.x"}))
	in := quarantineOf(t, img)
	scratch := t.TempDir()
	tools := &fakeTools{
		clamOut: filepath.Join(scratch, "layers", "0", "bin", "x") + ": Eicar-Test-Signature FOUND\n",
		osvOut:  `{"results":[{"packages":[{"vulnerabilities":[{"id":"GHSA-1","aliases":["MAL-2025-7"]},{"id":"MAL-2025-7"}]}]}]}`,
	}
	plan := ScanPlan{Stage: ScanStageStatic, Tools: []string{ToolClamAV, ToolOSV}, In: in, Scratch: scratch, DB: "/db", MaxBytes: 1 << 20}
	res, err := RunScan(context.Background(), plan, tools.run)
	if err != nil || res.Error != "" {
		t.Fatalf("RunScan = %+v, %v", res, err)
	}
	if !slices.Equal(res.StaticHits, []string{"clamav:Eicar-Test-Signature layer 0/bin/x"}) {
		t.Fatalf("static hits = %q", res.StaticHits)
	}
	if !slices.Equal(res.MalIDs, []string{"MAL-2025-7"}) {
		t.Fatalf("MAL ids = %q", res.MalIDs)
	}
	if _, err := os.Stat(filepath.Join(scratch, "layers")); !os.IsNotExist(err) {
		t.Fatal("the extracted layers stayed in the scratch")
	}
}

func TestRunScanStaticFailsOnTraversal(t *testing.T) {
	in := quarantineOf(t, newTestImage(t, layerTar(t, false, tarEntry{Name: "../../x", Body: "x"})))
	tools := &fakeTools{}
	res, err := RunScan(context.Background(), ScanPlan{Stage: ScanStageStatic, Tools: []string{ToolClamAV}, In: in, Scratch: t.TempDir(), MaxBytes: 1 << 20}, tools.run)
	if err != nil || !strings.Contains(res.Error, "unsafe layer entry") {
		t.Fatalf("RunScan = %+v, %v", res, err)
	}
	if len(tools.calls) != 0 {
		t.Fatalf("a scanner ran after a refused extraction: %q", tools.calls)
	}
}

func TestParseYARA(t *testing.T) {
	got := ParseYARA([]byte("SUSP_Miner /s/layers/1/usr/bin/xm\n\n"), "/s/layers")
	if !slices.Equal(got, []string{"yara:SUSP_Miner layer 1/usr/bin/xm"}) {
		t.Fatalf("ParseYARA = %q", got)
	}
}
