package update

import "testing"

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"0.2.0":           true,
		"10.20.30":        true,
		"0.2.0-rc1":       true,
		"1.0.0-alpha.1":   true,
		"1.0.0-x-y.2":     true,
		"dev":             false,
		"":                false,
		"v0.2.0":          false, // a git describe of a local build
		"0.2":             false,
		"0.2.0-":          false,
		"0.2.0-rc..1":     false,
		"0.2.0+build":     false,
		"0.2.0/../../etc": false, // versions become paths under bin/
		"0.2.0-../x":      false,
		" 0.2.0":          false,
		"0.2.0\n":         false,
	} {
		if got := IsRelease(v); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestCompare(t *testing.T) {
	// Each is strictly older than the next.
	order := []string{
		"0.1.9",
		"0.2.0-alpha",
		"0.2.0-alpha.1",
		"0.2.0-alpha.2",
		"0.2.0-alpha.10", // numeric parts compare as numbers
		"0.2.0-alpha.beta",
		"0.2.0-rc1",
		"0.2.0",
		"0.2.1",
		"0.10.0",
		"1.0.0",
		"10.0.0",
	}
	for i := range order {
		for j := range order {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := Compare(order[i], order[j]); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	// Anything that is not a release sorts before every release.
	if Compare("dev", "0.0.1") != -1 || Compare("0.0.1", "dev") != 1 || Compare("dev", "dev") != 0 {
		t.Fatal("non-releases must sort before releases")
	}
	if Compare("99999999999999999999.0.0", "99999999999999999998.0.0") != 1 {
		t.Fatal("huge numbers must compare without overflow")
	}
}

func TestReleaseArch(t *testing.T) {
	if releaseArch("arm") != "armv7" || releaseArch("amd64") != "amd64" || releaseArch("mipsle") != "mipsle" {
		t.Fatal("arm maps to armv7, the rest as is")
	}
	if ReleaseArch() == "" {
		t.Fatal("empty")
	}
}
