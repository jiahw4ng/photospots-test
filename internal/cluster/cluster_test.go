package cluster

import (
	"fmt"
	"math/rand"
	"testing"
)

func blob(r *rand.Rand, cx, cy, spread float64, n int, photographers []string) []Point {
	pts := make([]Point, n)
	for i := range pts {
		pts[i] = Point{
			X:            cx + r.NormFloat64()*spread,
			Y:            cy + r.NormFloat64()*spread,
			Photographer: photographers[i%len(photographers)],
		}
	}
	return pts
}

func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

func TestSinglePhotographerIsNotASpot(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	pts := blob(r, 0, 0, 5, 400, []string{"balcony-guy"})
	if got := Run(pts, DefaultParams); len(got) != 0 {
		t.Fatalf("one photographer produced %d spots, want 0", len(got))
	}
}

func TestSeparateSpots(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	var pts []Point
	pts = append(pts, blob(r, 0, 0, 10, 50, names("a", 10))...)
	pts = append(pts, blob(r, 1000, 0, 10, 50, names("b", 10))...)
	pts = append(pts, Point{X: 500, Y: 500, Photographer: "lonely"})
	got := Run(pts, DefaultParams)
	if len(got) != 2 {
		t.Fatalf("got %d spots, want 2", len(got))
	}
}

func TestDenseChainIsSplit(t *testing.T) {
	// Two busy spots 150 m apart joined by a trickle of photos: first-pass
	// DBSCAN chains them; the split pass must separate them again.
	r := rand.New(rand.NewSource(3))
	var pts []Point
	pts = append(pts, blob(r, 0, 0, 8, 200, names("a", 40))...)
	pts = append(pts, blob(r, 150, 0, 8, 200, names("b", 40))...)
	for x := 10.0; x < 150; x += 20 {
		pts = append(pts, blob(r, x, 0, 5, 3, names(fmt.Sprintf("c%v-", x), 3))...)
	}
	params := DefaultParams
	params.MaxRadius = 60
	got := Run(pts, params)
	if len(got) < 2 {
		t.Fatalf("got %d spots, want the chain split into at least 2", len(got))
	}
}

func TestProjectorRoundTrip(t *testing.T) {
	p := NewProjector(1.3)
	x, y := p.Forward(103.85, 1.29)
	lon, lat := p.Inverse(x, y)
	if d := (lon-103.85)*(lon-103.85) + (lat-1.29)*(lat-1.29); d > 1e-18 {
		t.Fatalf("round trip drifted: %v %v", lon, lat)
	}
}
