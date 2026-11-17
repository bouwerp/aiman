package images

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractLayerWritesOnlyInside(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		want    error
	}{
		{"dotdot", []tarEntry{{Name: "../escape", Body: "x"}}, ErrUnsafeEntry},
		{"nested dotdot", []tarEntry{{Name: "a/../../escape", Body: "x"}}, ErrUnsafeEntry},
		{"absolute", []tarEntry{{Name: "/etc/passwd", Body: "x"}}, ErrUnsafeEntry},
		{"hard link out", []tarEntry{{Name: "l", Type: tar.TypeLink, Linkname: "../../etc/passwd"}}, ErrUnsafeEntry},
		{"hard link to a file the layer lacks", []tarEntry{{Name: "l", Type: tar.TypeLink, Linkname: "etc/shadow"}}, ErrUnsafeEntry},
		{"symlink then write through it", []tarEntry{
			{Name: "link", Type: tar.TypeSymlink, Linkname: "/"},
			{Name: "link/escape", Body: "x"},
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, "layer")
			err := ExtractLayer(bytes.NewReader(layerTar(t, false, c.entries...)), dest, &Budget{Bytes: 1 << 20, Entries: 100})
			if (c.want == nil && err != nil) || (c.want != nil && !errors.Is(err, c.want)) {
				t.Fatalf("ExtractLayer = %v, want %v", err, c.want)
			}
			if _, err := os.Lstat(filepath.Join(base, "escape")); err == nil {
				t.Fatal("an entry landed outside the layer directory")
			}
			if fi, err := os.Lstat(filepath.Join(dest, "link")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				t.Fatal("a symlink was created")
			}
		})
	}
}

func TestExtractLayerKeepsWhiteoutedFiles(t *testing.T) {
	dest := t.TempDir()
	layer := layerTar(t, true,
		tarEntry{Name: "etc/", Type: tar.TypeDir},
		tarEntry{Name: "etc/a", Body: "a"},
		tarEntry{Name: "etc/.wh.b"},
		tarEntry{Name: "etc/hard", Type: tar.TypeLink, Linkname: "etc/a"},
		tarEntry{Name: "dev/null", Type: tar.TypeChar},
	)
	if err := ExtractLayer(bytes.NewReader(layer), dest, &Budget{Bytes: 1 << 20, Entries: 100}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "etc", "hard")); err != nil || string(b) != "a" {
		t.Fatalf("hard link = %q, %v", b, err)
	}
	for _, skipped := range []string{"etc/.wh.b", "dev/null"} {
		if _, err := os.Lstat(filepath.Join(dest, skipped)); err == nil {
			t.Fatalf("%s was written", skipped)
		}
	}
	fi, err := os.Stat(filepath.Join(dest, "etc", "a"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", fi, err)
	}
}

func TestExtractLayerBudget(t *testing.T) {
	layer := layerTar(t, false, tarEntry{Name: "a", Body: "0123456789"}, tarEntry{Name: "b", Body: "x"})
	if err := ExtractLayer(bytes.NewReader(layer), t.TempDir(), &Budget{Bytes: 5, Entries: 10}); !errors.Is(err, ErrScratchFull) {
		t.Fatalf("over bytes = %v", err)
	}
	if err := ExtractLayer(bytes.NewReader(layer), t.TempDir(), &Budget{Bytes: 100, Entries: 1}); !errors.Is(err, ErrScratchFull) {
		t.Fatalf("over entries = %v", err)
	}
}
