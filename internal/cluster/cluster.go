// Package cluster turns a cloud of photo locations into photo spots.
//
// Two ideas matter more than the algorithm itself:
//
//  1. We cluster photographer-locations, not photos. Each photographer counts
//     once per ~20 m cell, so one person uploading 400 shots of their balcony
//     cannot create a "spot" on their own.
//  2. Plain DBSCAN chains dense areas together (all of Marina Bay becomes one
//     blob). Any cluster wider than MaxRadius is re-clustered with a smaller
//     eps until it either splits or eps hits MinEps.
package cluster

import (
	"math"
	"sort"
)

// Point is a location in metres on a local flat projection.
type Point struct {
	X, Y         float64
	Photographer string
}

type Params struct {
	Eps              float64 // neighbourhood radius in metres for the first pass
	MinEps           float64 // never split below this radius
	MinPhotographers int     // distinct photographers needed to form a spot
	MaxRadius        float64 // clusters wider than this get split
}

var DefaultParams = Params{Eps: 60, MinEps: 15, MinPhotographers: 3, MaxRadius: 200}

// Run returns one slice of point indexes per spot. Noise points are dropped.
func Run(points []Point, p Params) [][]int {
	all := make([]int, len(points))
	for i := range all {
		all[i] = i
	}
	return split(points, all, p.Eps, p)
}

func split(points []Point, idx []int, eps float64, p Params) [][]int {
	var out [][]int
	for _, c := range dbscan(points, idx, eps, p.MinPhotographers) {
		if eps/2 >= p.MinEps && Radius(points, c) > p.MaxRadius {
			sub := split(points, c, eps/2, p)
			if len(sub) > 0 {
				out = append(out, sub...)
				continue
			}
			// Splitting dissolved it into noise: keep the wide cluster instead.
		}
		out = append(out, c)
	}
	return out
}

// dbscan clusters the subset idx. A point is "core" when its eps-neighbourhood
// holds at least minPhotographers distinct photographers.
func dbscan(points []Point, idx []int, eps float64, minPhotographers int) [][]int {
	g := newGrid(points, idx, eps)
	const unvisited, noise = -2, -1
	label := make(map[int]int, len(idx))
	for _, i := range idx {
		label[i] = unvisited
	}
	isCore := func(nb []int) bool {
		seen := make(map[string]struct{}, minPhotographers)
		for _, j := range nb {
			seen[points[j].Photographer] = struct{}{}
			if len(seen) >= minPhotographers {
				return true
			}
		}
		return false
	}

	var clusters [][]int
	for _, i := range idx {
		if label[i] != unvisited {
			continue
		}
		nb := g.neighbours(i)
		if !isCore(nb) {
			label[i] = noise
			continue
		}
		id := len(clusters)
		members := []int{i}
		label[i] = id
		queue := nb
		for len(queue) > 0 {
			j := queue[0]
			queue = queue[1:]
			if label[j] == noise {
				label[j] = id // border point
				members = append(members, j)
			}
			if label[j] != unvisited {
				continue
			}
			label[j] = id
			members = append(members, j)
			if jnb := g.neighbours(j); isCore(jnb) {
				queue = append(queue, jnb...)
			}
		}
		sort.Ints(members)
		clusters = append(clusters, members)
	}
	return clusters
}

// Centroid returns the mean position of the cluster.
func Centroid(points []Point, c []int) (x, y float64) {
	for _, i := range c {
		x += points[i].X
		y += points[i].Y
	}
	n := float64(len(c))
	return x / n, y / n
}

// Radius is the 90th-percentile distance from the centroid, so one stray
// border point does not make a tight spot look huge.
func Radius(points []Point, c []int) float64 {
	cx, cy := Centroid(points, c)
	d := make([]float64, len(c))
	for k, i := range c {
		d[k] = math.Hypot(points[i].X-cx, points[i].Y-cy)
	}
	sort.Float64s(d)
	return d[int(float64(len(d)-1)*0.9)]
}

// grid is a uniform spatial hash with cell size eps.
type grid struct {
	points []Point
	eps    float64
	cells  map[[2]int][]int
}

func newGrid(points []Point, idx []int, eps float64) *grid {
	g := &grid{points: points, eps: eps, cells: make(map[[2]int][]int)}
	for _, i := range idx {
		k := g.key(points[i])
		g.cells[k] = append(g.cells[k], i)
	}
	return g
}

func (g *grid) key(p Point) [2]int {
	return [2]int{int(math.Floor(p.X / g.eps)), int(math.Floor(p.Y / g.eps))}
}

func (g *grid) neighbours(i int) []int {
	p := g.points[i]
	k := g.key(p)
	var out []int
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			for _, j := range g.cells[[2]int{k[0] + dx, k[1] + dy}] {
				q := g.points[j]
				if math.Hypot(p.X-q.X, p.Y-q.Y) <= g.eps {
					out = append(out, j)
				}
			}
		}
	}
	return out
}

// Projector maps lon/lat to metres around a reference latitude
// (equirectangular). Accurate to well under 1% across a city-sized region.
type Projector struct{ lat0, k float64 }

const earthRadius = 6371008.8

func NewProjector(refLat float64) Projector {
	return Projector{lat0: refLat, k: math.Cos(refLat * math.Pi / 180)}
}

func (p Projector) Forward(lon, lat float64) (x, y float64) {
	return lon * math.Pi / 180 * earthRadius * p.k, lat * math.Pi / 180 * earthRadius
}

func (p Projector) Inverse(x, y float64) (lon, lat float64) {
	return x / (earthRadius * p.k) * 180 / math.Pi, y / earthRadius * 180 / math.Pi
}
