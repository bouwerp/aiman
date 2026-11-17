package images

import (
	"fmt"
	"regexp"
	"strings"
)

// Reference is a parsed image reference.
type Reference struct {
	// Registry is the host serve talks to (registry-1.docker.io for Docker Hub).
	Registry string
	// Repo is the repository path (library/postgres for postgres).
	Repo string
	// Tag is the tag; empty when the ref names only a digest.
	Tag string
	// Digest is the digest the ref pins, if any.
	Digest string
}

var (
	digestRe   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	tagRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	repoPartRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
)

// dockerHub is the registry host of Docker Hub's index names.
const dockerHub = "registry-1.docker.io"

// ParseReference reads name[:tag][@sha256:hex] the way Docker does: a first
// path element with a dot or a colon, or localhost, is the registry; anything
// else is Docker Hub, where a single-element name is under library/.
func ParseReference(ref string) (Reference, error) {
	ref = strings.TrimSpace(ref)
	var r Reference
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest {
		if !digestRe.MatchString(digest) {
			return r, fmt.Errorf("image %q: digest must be sha256:<64 hex>", ref)
		}
		r.Digest = digest
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, r.Tag = name[:i], name[i+1:]
		if !tagRe.MatchString(r.Tag) {
			return r, fmt.Errorf("image %q: bad tag %q", ref, r.Tag)
		}
	} else if !hasDigest {
		r.Tag = "latest"
	}
	parts := strings.Split(name, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		r.Registry, parts = parts[0], parts[1:]
	}
	switch r.Registry {
	case "", "docker.io", "index.docker.io":
		r.Registry = dockerHub
		if len(parts) == 1 {
			parts = append([]string{"library"}, parts...)
		}
	}
	for _, p := range parts {
		if !repoPartRe.MatchString(p) {
			return r, fmt.Errorf("image %q: bad repository element %q", ref, p)
		}
	}
	r.Repo = strings.Join(parts, "/")
	return r, nil
}

// Reference is the target serve asks the registry for: the digest when the
// ref pins one, else the tag.
func (r Reference) reference() string {
	if r.Digest != "" {
		return r.Digest
	}
	return r.Tag
}

// String is the normalised ref.
func (r Reference) String() string {
	s := r.Registry + "/" + r.Repo
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}
