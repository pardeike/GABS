package version

import "testing"

func TestParseAcceptsTheFormsGABSEmits(t *testing.T) {
	cases := map[string]Semver{
		"1.1.0":            {1, 1, 0},
		"v1.1.0":           {1, 1, 0},
		"1.0.8":            {1, 0, 8},
		"2.0.0-rc.1":       {2, 0, 0},
		"1.2.3+build.5":    {1, 2, 3},
		"1.1":              {1, 1, 0},
		"1":                {1, 0, 0},
		"  1.1.0  ":        {1, 1, 0},
		"1.1.0-dirty":      {1, 1, 0},
		"10.20.30":         {10, 20, 30},
		"1.1.0-rc.1+meta2": {1, 1, 0},
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := Parse(in)
			if err != nil {
				t.Fatalf("Parse(%q) errored: %v", in, err)
			}
			if got != want {
				t.Errorf("Parse(%q) = %+v, want %+v", in, got, want)
			}
		})
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "dev", "unknown", "1.x.0", "1.2.3.4", "1..2", "-1.0.0", "abc"} {
		t.Run(in, func(t *testing.T) {
			if got, err := Parse(in); err == nil {
				t.Errorf("Parse(%q) should fail, got %+v", in, got)
			}
		})
	}
}

func TestCompareOrdersByPrecedence(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.1.0", "1.0.8", 1},
		{"1.0.8", "1.1.0", -1},
		{"1.1.0", "1.1.0", 0},
		{"2.0.0", "1.99.99", 1},
		{"1.1.1", "1.1.0", 1},
		{"1.10.0", "1.9.0", 1}, // numeric, not lexicographic
		{"1.1.0-rc.1", "1.1.0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			a, err := Parse(tc.a)
			if err != nil {
				t.Fatal(err)
			}
			b, err := Parse(tc.b)
			if err != nil {
				t.Fatal(err)
			}
			if got := Compare(a, b); got != tc.want {
				t.Errorf("Compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestAtLeastReportsIncomparableForDevBuilds pins the rule that a development
// build must not be treated as violating a requirement: refusing to load a
// config because the binary is stamped "dev" would be worse than the version
// skew the check exists to catch.
func TestAtLeastReportsIncomparableForDevBuilds(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	want, err := Parse("1.1.0")
	if err != nil {
		t.Fatal(err)
	}

	for _, dev := range []string{"dev", "unknown", ""} {
		Version = dev
		if ok, comparable := AtLeast(want); comparable || ok {
			t.Errorf("Version=%q: got (ok=%v, comparable=%v), want (false, false)", dev, ok, comparable)
		}
	}

	Version = "1.1.0"
	if ok, comparable := AtLeast(want); !ok || !comparable {
		t.Errorf("Version=1.1.0 must satisfy 1.1.0, got (ok=%v, comparable=%v)", ok, comparable)
	}

	Version = "1.0.8"
	if ok, comparable := AtLeast(want); ok || !comparable {
		t.Errorf("Version=1.0.8 must not satisfy 1.1.0, got (ok=%v, comparable=%v)", ok, comparable)
	}
}
