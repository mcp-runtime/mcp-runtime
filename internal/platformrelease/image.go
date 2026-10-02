package platformrelease

import (
	"fmt"
	"regexp"
	"strings"
)

// ImageRef is a parsed OCI image reference.
type ImageRef struct {
	// Registry is the registry host (with optional port); empty when the
	// reference is relative (for example "mcp-ui:v1").
	Registry string
	// Path is the repository path without the registry host.
	Path   string
	Tag    string
	Digest string
}

var (
	repoPathPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]+)?$`)
	tagPattern      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestPattern   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ValidTag reports whether s is a valid OCI tag.
func ValidTag(s string) bool { return tagPattern.MatchString(s) }

// ValidDigest reports whether s is a sha256 digest.
func ValidDigest(s string) bool { return digestPattern.MatchString(s) }

// ParseImageRef parses repo[:tag][@digest] with an optional registry host.
func ParseImageRef(s string) (ImageRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ImageRef{}, fmt.Errorf("image reference is empty")
	}
	var ref ImageRef
	name := s
	if at := strings.Index(name, "@"); at >= 0 {
		ref.Digest = name[at+1:]
		name = name[:at]
		if !ValidDigest(ref.Digest) {
			return ImageRef{}, fmt.Errorf("image %q has an invalid digest (want sha256:<64 hex>)", s)
		}
	}
	if colon := strings.LastIndex(name, ":"); colon >= 0 && !strings.Contains(name[colon+1:], "/") {
		ref.Tag = name[colon+1:]
		name = name[:colon]
		if !ValidTag(ref.Tag) {
			return ImageRef{}, fmt.Errorf("image %q has an invalid tag", s)
		}
	}
	if slash := strings.Index(name, "/"); slash >= 0 {
		first := name[:slash]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			ref.Registry = first
			name = name[slash+1:]
			if !hostPattern.MatchString(ref.Registry) {
				return ImageRef{}, fmt.Errorf("image %q has an invalid registry host", s)
			}
		}
	}
	ref.Path = name
	if !repoPathPattern.MatchString(ref.Path) {
		return ImageRef{}, fmt.Errorf("image %q has an invalid repository path", s)
	}
	return ref, nil
}

// Name returns registry/path (or just path for relative references).
func (r ImageRef) Name() string {
	if r.Registry == "" {
		return r.Path
	}
	return r.Registry + "/" + r.Path
}

// String renders the full reference.
func (r ImageRef) String() string {
	out := r.Name()
	if r.Tag != "" {
		out += ":" + r.Tag
	}
	if r.Digest != "" {
		out += "@" + r.Digest
	}
	return out
}

// DigestFromImageID extracts the sha256 digest from a container status imageID
// such as "docker-pullable://repo@sha256:..." or "repo@sha256:...".
func DigestFromImageID(imageID string) string {
	idx := strings.LastIndex(imageID, "sha256:")
	if idx < 0 {
		return ""
	}
	d := imageID[idx:]
	if !ValidDigest(d) {
		return ""
	}
	return d
}
