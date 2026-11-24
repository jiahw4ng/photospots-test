// Package spots rebuilds the spots table from the photos table.
package spots

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"photospots/internal/cluster"
	"photospots/internal/model"
	"photospots/internal/store"
)

// One photographer counts once per cell of this size when clustering.
const dedupCell = 20.0 // metres

type Summary struct {
	Photos, PhotographerPoints, Spots, PhotosInSpots int
}

// Build re-clusters every photo inside region and replaces that region's spots.
func Build(ctx context.Context, st *store.Store, region model.Region, params cluster.Params) (Summary, error) {
	var sum Summary
	photos, err := st.PhotosIn(ctx, region.BBox)
	if err != nil {
		return sum, err
	}
	sum.Photos = len(photos)
	proj := cluster.NewProjector((region.BBox.MinLat + region.BBox.MaxLat) / 2)

	// Collapse each photographer's photos into one point per 20 m cell.
	type cellKey struct {
		who  string
		x, y int64
	}
	pointOf := map[cellKey]int{}
	var points []cluster.Point
	var members [][]int // point index -> photo indexes
	for i, p := range photos {
		x, y := proj.Forward(p.Lon, p.Lat)
		k := cellKey{p.Photographer, int64(math.Floor(x / dedupCell)), int64(math.Floor(y / dedupCell))}
		pi, ok := pointOf[k]
		if !ok {
			pi = len(points)
			pointOf[k] = pi
			points = append(points, cluster.Point{X: x, Y: y, Photographer: p.Photographer})
			members = append(members, nil)
		}
		members[pi] = append(members[pi], i)
	}
	sum.PhotographerPoints = len(points)

	clusters := cluster.Run(points, params)
	sum.Spots = len(clusters)

	tx, err := st.DB.Begin(ctx)
	if err != nil {
		return sum, err
	}
	defer tx.Rollback(ctx)

	env := []any{region.BBox.MinLon, region.BBox.MinLat, region.BBox.MaxLon, region.BBox.MaxLat}
	if _, err := tx.Exec(ctx, `DELETE FROM spots WHERE geom && ST_MakeEnvelope($1,$2,$3,$4,4326)`, env...); err != nil {
		return sum, err
	}
	if _, err := tx.Exec(ctx, `UPDATE photos SET spot_id = NULL WHERE geom && ST_MakeEnvelope($1,$2,$3,$4,4326)`, env...); err != nil {
		return sum, err
	}
	var baseID int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM spots`).Scan(&baseID); err != nil {
		return sum, err
	}

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE assign (source text, source_id text, spot_id int) ON COMMIT DROP;
		CREATE TEMP TABLE spot_geo (spot_id int, lon float8, lat float8, radius real) ON COMMIT DROP`); err != nil {
		return sum, err
	}
	var assignRows, geoRows [][]any
	for ci, c := range clusters {
		id := baseID + ci + 1
		cx, cy := cluster.Centroid(points, c)
		lon, lat := proj.Inverse(cx, cy)
		geoRows = append(geoRows, []any{id, lon, lat, float32(cluster.Radius(points, c))})
		for _, pi := range c {
			for _, phi := range members[pi] {
				assignRows = append(assignRows, []any{photos[phi].Source, photos[phi].SourceID, id})
			}
		}
	}
	sum.PhotosInSpots = len(assignRows)
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"assign"}, []string{"source", "source_id", "spot_id"}, pgx.CopyFromRows(assignRows)); err != nil {
		return sum, fmt.Errorf("copy assignments: %w", err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"spot_geo"}, []string{"spot_id", "lon", "lat", "radius"}, pgx.CopyFromRows(geoRows)); err != nil {
		return sum, fmt.Errorf("copy spot geometry: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE photos p SET spot_id = a.spot_id FROM assign a
		WHERE p.source = a.source AND p.source_id = a.source_id`); err != nil {
		return sum, err
	}
	if _, err := tx.Exec(ctx, statsSQL); err != nil {
		return sum, fmt.Errorf("compute spot stats: %w", err)
	}
	return sum, tx.Commit(ctx)
}

// Histograms count distinct photographers per bucket, not photos, for the
// same reason clustering does. Score v1: log of distinct photographers,
// nudged up by median Flickr views when we have them.
const statsSQL = `
INSERT INTO spots (id, geom, radius_m, photo_count, photographer_count, score,
                   hour_histogram, month_histogram, label, cover_thumb)
SELECT g.spot_id,
       ST_SetSRID(ST_MakePoint(g.lon, g.lat), 4326),
       g.radius,
       s.photo_count,
       s.photographer_count,
       ln(1 + s.photographer_count) * (1 + 0.1 * ln(1 + coalesce(s.median_views, 0))),
       (SELECT array_agg(coalesce(x.n, 0) ORDER BY h)
          FROM generate_series(0, 23) h
          LEFT JOIN (SELECT taken_hour, count(DISTINCT photographer) n FROM photos
                     WHERE spot_id = g.spot_id GROUP BY 1) x ON x.taken_hour = h),
       (SELECT array_agg(coalesce(x.n, 0) ORDER BY m)
          FROM generate_series(1, 12) m
          LEFT JOIN (SELECT taken_month, count(DISTINCT photographer) n FROM photos
                     WHERE spot_id = g.spot_id GROUP BY 1) x ON x.taken_month = m),
       coalesce(c.label, ''),
       coalesce(c.cover, '')
FROM spot_geo g
JOIN LATERAL (
    SELECT count(*) AS photo_count,
           count(DISTINCT photographer) AS photographer_count,
           percentile_cont(0.5) WITHIN GROUP (ORDER BY views) AS median_views
    FROM photos WHERE spot_id = g.spot_id
) s ON true
LEFT JOIN LATERAL (
    SELECT p.thumb_url AS cover,
           trim(regexp_replace(p.title, '\s*(\(\d+\)|- panoramio|\.\w{3,4})\s*$', '', 'i')) AS label
    FROM photos p WHERE p.spot_id = g.spot_id AND p.thumb_url <> ''
    ORDER BY p.views DESC NULLS LAST, md5(p.source_id)
    LIMIT 1
) c ON true`
