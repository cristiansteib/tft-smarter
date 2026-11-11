package version

import "testing"

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.16.0", "0.15.1", true}, {"v0.16.0", "0.16.0", false}, {"0.15.9", "v0.16.0", false},
		{"1.0.0", "dev", true}, {"dev", "1.0.0", false}, {"0.16.0", "0.16.0-SNAPSHOT-abc", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
	for v, want := range map[string]bool{"0.16.0": true, "v1.2.3": true, "dev": false, "0.16.0-SNAPSHOT-abc": false, "v0.0.0-20261111-abcdef": false} {
		if Released(v) != want {
			t.Errorf("Released(%q) != %v", v, want)
		}
	}
}

func TestFits(t *testing.T) {
	if ok, _ := Fits(1, 1, 2); !ok {
		t.Error("in range must fit")
	}
	if ok, why := Fits(0, 1, 1); ok || why == "" {
		t.Error("old firmware must not fit, with a hint")
	}
	if ok, why := Fits(3, 1, 2); ok || why == "" {
		t.Error("newer firmware must not fit, with a hint")
	}
}
