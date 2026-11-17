package images

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Extraction refusals. Each ends the vetting as failed, never clean.
var (
	// ErrUnsafeEntry is an absolute path, a .. element, or a hard link to a
	// file outside the layer.
	ErrUnsafeEntry = errors.New("unsafe layer entry")
	// ErrScratchFull is a layer set over the scratch byte or entry limit.
	ErrScratchFull = errors.New("scratch limit reached")
)

// DefaultMaxEntries caps the entries of all layers of one image.
const DefaultMaxEntries = 1_000_000

// Budget is what extraction may still write.
type Budget struct {
	Bytes   int64
	Entries int
}

func (b *Budget) take(bytes int64) error {
	b.Entries--
	b.Bytes -= bytes
	if b.Entries < 0 || b.Bytes < 0 {
		return ErrScratchFull
	}
	return nil
}

// ExtractLayer writes one layer (tar, gzip or zstd) into dest, a directory
// of its own, without applying whiteouts: a file a later layer deletes stays
// in this layer's directory and is still scanned. Only directories, regular
// files and hard links inside the layer are written, as 0700 and 0600 owned
// by the caller. Symlinks are never created or followed, device files and
// fifos are skipped, and every write goes through an os.Root on dest, so no
// entry can land outside it. An absolute name, a .. element or a hard link to
// a file the layer did not write is ErrUnsafeEntry.
func ExtractLayer(r io.Reader, dest string, budget *Budget) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	plain, closeFn, err := decompress(r)
	if err != nil {
		return err
	}
	defer closeFn()
	tr := tar.NewReader(plain)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read layer: %w", err)
		}
		if err := extractEntry(root, tr, hdr, budget); err != nil {
			return err
		}
	}
}

func decompress(r io.Reader) (io.Reader, func(), error) {
	br := bufio.NewReader(r)
	magic, _ := br.Peek(4)
	switch {
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, err
		}
		return zr, func() { _ = zr.Close() }, nil
	case bytes.Equal(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, err := zstd.NewReader(br, zstd.WithDecoderMaxMemory(1<<30))
		if err != nil {
			return nil, nil, err
		}
		return zr, zr.Close, nil
	default:
		return br, func() {}, nil
	}
}

// entryName cleans a tar name and refuses one that could leave the layer.
func entryName(raw string) (string, error) {
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) {
		return "", fmt.Errorf("%w: absolute name %q", ErrUnsafeEntry, raw)
	}
	for _, el := range strings.Split(strings.ReplaceAll(raw, `\`, "/"), "/") {
		if el == ".." {
			return "", fmt.Errorf("%w: %q has a .. element", ErrUnsafeEntry, raw)
		}
	}
	name := path.Clean(strings.TrimPrefix(raw, "./"))
	if name == "." {
		return "", nil
	}
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeEntry, raw)
	}
	return name, nil
}

func extractEntry(root *os.Root, tr *tar.Reader, hdr *tar.Header, budget *Budget) error {
	name, err := entryName(hdr.Name)
	if err != nil || name == "" {
		return err
	}
	if strings.HasPrefix(path.Base(name), ".wh.") {
		return budget.take(0) // a whiteout marker: the deleted file stays in its own layer
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := budget.take(0); err != nil {
			return err
		}
		return root.MkdirAll(name, 0o700)
	case tar.TypeReg:
		if err := budget.take(hdr.Size); err != nil {
			return err
		}
		return writeEntry(root, name, tr, hdr.Size)
	case tar.TypeLink:
		target, err := entryName(hdr.Linkname)
		if err != nil {
			return err
		}
		if fi, err := root.Lstat(target); err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%w: hard link %q points outside the layer (%q)", ErrUnsafeEntry, hdr.Name, hdr.Linkname)
		}
		if err := budget.take(0); err != nil {
			return err
		}
		if err := mkParent(root, name); err != nil {
			return err
		}
		_ = root.Remove(name)
		return root.Link(target, name)
	default:
		// Symlinks, devices, fifos: never created.
		return budget.take(0)
	}
}

func mkParent(root *os.Root, name string) error {
	if dir := path.Dir(name); dir != "." {
		return root.MkdirAll(dir, 0o700)
	}
	return nil
}

func writeEntry(root *os.Root, name string, r io.Reader, size int64) error {
	if err := mkParent(root, name); err != nil {
		return err
	}
	_ = root.Remove(name)
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.CopyN(f, r, size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
