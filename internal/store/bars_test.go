package store

import (
	"testing"
	"time"
)

// Bars split a window into equal buckets; an empty bucket is a zero bar, a
// bucket where one of two probes failed is one up of two.
func TestCheckBarsBuckets(t *testing.T) {
	s, ctx := open(t), t.Context()
	to := time.Now().Truncate(time.Hour)
	from := to.Add(-4 * time.Hour)
	var samples []Sample
	for _, h := range []int{0, 1, 3} {
		at := from.Add(time.Duration(h)*time.Hour + 10*time.Minute)
		samples = append(samples,
			Sample{Check: "a", Probe: "ru", At: at, Up: h != 1},
			Sample{Check: "a", Probe: "de", At: at, Up: true},
			Sample{Check: "sub/x", Probe: "de", At: at, Up: true})
	}
	if err := s.WriteSamples(ctx, samples); err != nil {
		t.Fatal(err)
	}
	bars, err := s.CheckBars(ctx, from, to, 4)
	if err != nil {
		t.Fatal(err)
	}
	a := bars["a"]
	if len(a) != 4 {
		t.Fatalf("buckets: %+v", a)
	}
	if a[0] != (Bar{N: 2, Up: 2}) || a[1] != (Bar{N: 2, Up: 1}) || a[2] != (Bar{}) || a[3] != (Bar{N: 2, Up: 2}) {
		t.Fatalf("bars: %+v", a)
	}
	// A subscription config has its own bars, and the subscription sums its configs.
	if x := bars["sub/x"]; len(x) != 4 || x[0] != (Bar{N: 1, Up: 1}) {
		t.Fatalf("config: %+v", x)
	}
	if sub := bars["sub"]; len(sub) != 4 || sub[3] != (Bar{N: 1, Up: 1}) || sub[2] != (Bar{}) {
		t.Fatalf("subscription: %+v", sub)
	}
}

// Past raw retention the bars come from the 5-minute rollups.
func TestCheckBarsUsesRollupsPastRawRetention(t *testing.T) {
	s, ctx := open(t), t.Context()
	to := time.Now().Truncate(time.Hour)
	from := to.Add(-72 * time.Hour)
	at := from.Add(10 * time.Minute)
	if err := s.WriteSamples(ctx, []Sample{{Check: "a", Probe: "ru", At: at, Up: false}, {Check: "a", Probe: "de", At: at, Up: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM samples`); err != nil { // as if raw retention had passed
		t.Fatal(err)
	}
	bars, err := s.CheckBars(ctx, from, to, 72)
	if err != nil {
		t.Fatal(err)
	}
	if a := bars["a"]; len(a) != 72 || a[0] != (Bar{N: 2, Up: 1}) {
		t.Fatalf("bars from rollups: %+v", a[:2])
	}
}
