package platformrelease

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a parsed semantic version (vMAJOR.MINOR.PATCH[-PRERELEASE]).
type Version struct {
	Major, Minor, Patch int
	Pre                 string
	raw                 string
}

var semverPattern = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

// ParseVersion parses a semantic version with an optional leading "v".
func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	m := semverPattern.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a semantic version (want vMAJOR.MINOR.PATCH)", s)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return Version{Major: major, Minor: minor, Patch: patch, Pre: m[4], raw: s}, nil
}

// String returns the original version text.
func (v Version) String() string { return v.raw }

// SameMinor reports whether both versions share major and minor numbers.
func (v Version) SameMinor(o Version) bool { return v.Major == o.Major && v.Minor == o.Minor }

// Compare returns -1, 0, or 1 following semver precedence rules.
func (v Version) Compare(o Version) int {
	for _, pair := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	return comparePrerelease(v.Pre, o.Pre)
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		an, aErr := strconv.Atoi(ap[i])
		bn, bErr := strconv.Atoi(bp[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(ap[i], bp[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(ap) < len(bp):
		return -1
	case len(ap) > len(bp):
		return 1
	}
	return 0
}

// CompareVersionStrings compares two version strings. ok is false when either
// side is not a semantic version, in which case the result is meaningless.
func CompareVersionStrings(a, b string) (cmp int, ok bool) {
	av, err := ParseVersion(a)
	if err != nil {
		return 0, false
	}
	bv, err := ParseVersion(b)
	if err != nil {
		return 0, false
	}
	return av.Compare(bv), true
}

// ReleaseVersionFromImageTag removes the full Git revision suffix from tags
// emitted for built release images. Other prerelease tags retain their semantic
// version ordering.
func ReleaseVersionFromImageTag(tag string) string {
	if _, ok := ReleaseCommitFromImageTag(tag); !ok {
		return tag
	}
	return tag[:strings.LastIndexByte(tag, '-')]
}

// ReleaseCommitFromImageTag recognizes the commit suffix used by release
// manifests. A short or arbitrary prerelease suffix is not a source revision.
func ReleaseCommitFromImageTag(tag string) (string, bool) {
	lastDash := strings.LastIndexByte(tag, '-')
	if lastDash < 0 {
		return "", false
	}
	version, suffix := tag[:lastDash], tag[lastDash+1:]
	if !isReleaseCommitSuffix(suffix) {
		return "", false
	}
	if _, err := ParseVersion(version); err != nil {
		return "", false
	}
	return suffix[1:], true
}

func isReleaseCommitSuffix(s string) bool {
	if !strings.HasPrefix(s, "g") || len(s) != 41 {
		return false
	}
	for _, ch := range s[1:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
