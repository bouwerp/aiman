package main

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestArtifactPublishParams(t *testing.T) {
	wd, _ := os.Getwd()
	env := func(k string) string {
		if k == "AIMAN_ID" {
			return "s1"
		}
		return ""
	}
	p, err := artifactPublishParams([]string{"r/index.html", "--file", "img/a.svg=r/a.svg", "--file", "r/b.css",
		"--title", "T", "--theme=plex", "--expect-version", "2", "--id", "abc"}, env)
	if err != nil {
		t.Fatal(err)
	}
	files := p["files"].([]map[string]string)
	if p["page"] != filepath.Join(wd, "r/index.html") || p["session"] != "s1" || p["expect_version"] != 2 ||
		p["title"] != "T" || p["theme"] != "plex" || p["id"] != "abc" || len(files) != 2 ||
		files[0]["path"] != "img/a.svg" || files[0]["from"] != filepath.Join(wd, "r/a.svg") || files[1]["path"] != "" {
		t.Fatalf("params %+v", p)
	}
	for _, bad := range [][]string{{}, {"a.html", "b.html"}, {"a.html", "--expect-version", "x"}, {"a.html", "--nope", "1"}, {"a.html", "--title"}} {
		if _, err := artifactPublishParams(bad, env); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestArtifactKitReadsEveryFile(t *testing.T) {
	files, err := artifactKit(fstest.MapFS{"theme.css": {Data: []byte("c")}, "fonts/a.woff2": {Data: []byte("f")}})
	if err != nil || len(files) != 2 || files[0].Path != "fonts/a.woff2" || files[1].Path != "theme.css" {
		t.Fatalf("kit %+v %v", files, err)
	}
}
