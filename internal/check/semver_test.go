package check

import "testing"

func TestHighestSemverTag(t *testing.T) {
	t.Run("picks the highest, tolerating the v prefix", func(t *testing.T) {
		top, ok := HighestSemverTag([]string{"v1.9.0", "v1.10.0", "1.2.3"}, false)
		if !ok {
			t.Fatal("ok = false, want a candidate")
		}
		if top.Tag != "v1.10.0" || top.Version != "1.10.0" {
			t.Errorf("top = %+v, want v1.10.0 / 1.10.0", top)
		}
	})
	t.Run("ignores non-semver tags", func(t *testing.T) {
		if _, ok := HighestSemverTag([]string{"latest", "edge", "nightly"}, false); ok {
			t.Error("ok = true for tags with no semver, want false")
		}
	})
	t.Run("prerelease beats the previous stable by semver precedence", func(t *testing.T) {
		top, ok := HighestSemverTag([]string{"v1.2.9", "v1.3.0-rc.1"}, false)
		if !ok || top.Tag != "v1.3.0-rc.1" {
			t.Errorf("top = %+v ok=%v, want v1.3.0-rc.1 (semver precedence)", top, ok)
		}
	})
	t.Run("stable mode skips prereleases", func(t *testing.T) {
		top, ok := HighestSemverTag([]string{"v1.2.9", "v1.3.0-rc.1"}, true)
		if !ok || top.Tag != "v1.2.9" {
			t.Errorf("top = %+v ok=%v, want v1.2.9 in stable mode", top, ok)
		}
	})
	t.Run("partial versions count", func(t *testing.T) {
		top, ok := HighestSemverTag([]string{"1.27", "1.26"}, false)
		if !ok || top.Version != "1.27.0" {
			t.Errorf("top = %+v, want 1.27.0 (nginx-style tags)", top)
		}
	})
}

func TestSemverBump(t *testing.T) {
	one, _ := HighestSemverTag([]string{"v1.2.3"}, false)
	two, _ := HighestSemverTag([]string{"v1.2.4"}, false)
	if SemverBump("1.2.3", one) {
		t.Error("same version counts as a bump")
	}
	if !SemverBump("1.2.3", two) {
		t.Error("higher version does not count as a bump")
	}
	if SemverBump("", two) {
		t.Error("empty stored version counts as a bump; seeding must handle it")
	}
	if SemverBump("garbage", two) {
		t.Error("unparseable stored version counts as a bump")
	}
}
