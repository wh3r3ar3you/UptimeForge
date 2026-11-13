package update

import (
	"regexp"
	"runtime"
	"strings"
)

// releaseRe matches MAJOR.MINOR.PATCH[-pre] as GoReleaser stamps it (no "v").
// Versions name directories under bin/, so nothing else is accepted: no
// slashes, no empty pre-release parts, no build metadata.
var releaseRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// IsRelease reports whether v is a release version. "dev" and local builds
// (git describe: "v0.1.0-3-gabc") are not.
func IsRelease(v string) bool { return releaseRe.MatchString(v) }

// Compare orders versions: -1 if a is older than b, 0 if equal, 1 if newer.
// A pre-release is older than its release (0.2.0-rc1 < 0.2.0); its
// dot-separated parts compare as numbers when both are numeric, numbers
// before words, and a shorter list of equal parts first. Anything that is
// not a release sorts before every release.
func Compare(a, b string) int {
	ra, rb := IsRelease(a), IsRelease(b)
	switch {
	case !ra && !rb:
		return strings.Compare(a, b)
	case !ra:
		return -1
	case !rb:
		return 1
	}
	coreA, preA, _ := strings.Cut(a, "-")
	coreB, preB, _ := strings.Cut(b, "-")
	na, nb := strings.Split(coreA, "."), strings.Split(coreB, ".")
	for i := range 3 {
		if c := compareNum(na[i], nb[i]); c != 0 {
			return c
		}
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	pa, pb := strings.Split(preA, "."), strings.Split(preB, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		da, db := isDigits(pa[i]), isDigits(pb[i])
		var c int
		switch {
		case da && db:
			c = compareNum(pa[i], pb[i])
		case da:
			c = -1
		case db:
			c = 1
		default:
			c = strings.Compare(pa[i], pb[i])
		}
		if c != 0 {
			return c
		}
	}
	switch {
	case len(pa) < len(pb):
		return -1
	case len(pa) > len(pb):
		return 1
	}
	return 0
}

// compareNum compares decimal strings of any length.
func compareNum(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// ReleaseArch is this binary's architecture as release file names spell it.
func ReleaseArch() string { return releaseArch(runtime.GOARCH) }

// releaseArch maps a GOARCH to the release name: arm is shipped only as
// armv7 (GOARM=7); the rest keep their Go names.
func releaseArch(goarch string) string {
	if goarch == "arm" {
		return "armv7"
	}
	return goarch
}

// AssetName is the release file of the agent for a platform, as served under
// /download/ and listed in checksums.txt.
func AssetName(goos, arch string) string {
	name := "uptimeforge-agent_" + goos + "_" + arch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}
