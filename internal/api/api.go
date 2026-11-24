// Package api serves spots as JSON plus the map page.
package api

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sixdouglas/suncalc"

	"photospots/internal/store"
)

//go:embed web
var webFS embed.FS

type Server struct {
	Store    *store.Store
	Location *time.Location // time zone for sun times
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/spots", s.listSpots)
	mux.HandleFunc("GET /api/spots/{id}", s.getSpot)
	mux.HandleFunc("GET /api/sun", s.sun)
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	return mux
}

type Spot struct {
	ID                int      `json:"id"`
	Lat               float64  `json:"lat"`
	Lon               float64  `json:"lon"`
	RadiusM           float32  `json:"radius_m"`
	PhotoCount        int      `json:"photo_count"`
	PhotographerCount int      `json:"photographer_count"`
	Score             float32  `json:"score"`
	Label             string   `json:"label"`
	CoverThumb        string   `json:"cover_thumb"`
	HourHistogram     []int32  `json:"hour_histogram"`
	MonthHistogram    []int32  `json:"month_histogram"`
	DistanceM         *float64 `json:"distance_m,omitempty"`
}

const spotCols = `s.id, ST_Y(s.geom), ST_X(s.geom), s.radius_m, s.photo_count, s.photographer_count,
	s.score, s.label, s.cover_thumb, s.hour_histogram, s.month_histogram`

func scanSpot(r pgx.Row, withDist bool) (Spot, error) {
	var sp Spot
	dest := []any{&sp.ID, &sp.Lat, &sp.Lon, &sp.RadiusM, &sp.PhotoCount, &sp.PhotographerCount,
		&sp.Score, &sp.Label, &sp.CoverThumb, &sp.HourHistogram, &sp.MonthHistogram}
	if withDist {
		dest = append(dest, &sp.DistanceM)
	}
	err := r.Scan(dest...)
	return sp, err
}

// listSpots answers either ?bbox=minLon,minLat,maxLon,maxLat (map view) or
// ?lat=&lon=&radius_km= (near me, ranked by score).
func (s *Server) listSpots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := clampInt(q.Get("limit"), 200, 1, 2000)
	minPhotographers := clampInt(q.Get("min_photographers"), 3, 1, 1000)
	var rows pgx.Rows
	var err error
	withDist := false

	switch {
	case q.Get("bbox") != "":
		b, ok := parseFloats(q.Get("bbox"), 4)
		if !ok {
			httpError(w, http.StatusBadRequest, "bbox must be minLon,minLat,maxLon,maxLat")
			return
		}
		rows, err = s.Store.DB.Query(r.Context(), `SELECT `+spotCols+` FROM spots s
			WHERE s.geom && ST_MakeEnvelope($1,$2,$3,$4,4326) AND s.photographer_count >= $5
			ORDER BY s.score DESC LIMIT $6`, b[0], b[1], b[2], b[3], minPhotographers, limit)
	case q.Get("lat") != "" && q.Get("lon") != "":
		lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
		lon, err2 := strconv.ParseFloat(q.Get("lon"), 64)
		if err1 != nil || err2 != nil {
			httpError(w, http.StatusBadRequest, "lat and lon must be numbers")
			return
		}
		radiusM := float64(clampInt(q.Get("radius_km"), 5, 1, 100)) * 1000
		withDist = true
		rows, err = s.Store.DB.Query(r.Context(), `SELECT `+spotCols+`,
				ST_Distance(s.geom::geography, ST_MakePoint($2,$1)::geography) AS d
			FROM spots s
			WHERE ST_DWithin(s.geom::geography, ST_MakePoint($2,$1)::geography, $3)
			  AND s.photographer_count >= $4
			ORDER BY s.score DESC LIMIT $5`, lat, lon, radiusM, minPhotographers, limit)
	default:
		httpError(w, http.StatusBadRequest, "pass bbox, or lat and lon")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	spots, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Spot, error) { return scanSpot(r, withDist) })
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"spots": spots})
}

type Photo struct {
	Title        string     `json:"title"`
	Photographer string     `json:"photographer"`
	ThumbURL     string     `json:"thumb_url"`
	PageURL      string     `json:"page_url"`
	License      string     `json:"license"`
	Source       string     `json:"source"`
	TakenLocal   *time.Time `json:"taken_local,omitempty"`
}

func (s *Server) getSpot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad id")
		return
	}
	sp, err := scanSpot(s.Store.DB.QueryRow(r.Context(), `SELECT `+spotCols+` FROM spots s WHERE s.id = $1`, id), false)
	if errors.Is(err, pgx.ErrNoRows) {
		httpError(w, http.StatusNotFound, "no such spot")
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	// One photo per photographer so a single prolific uploader can't fill the gallery.
	rows, err := s.Store.DB.Query(r.Context(), `
		SELECT title, photographer, thumb_url, page_url, license, source, taken_local FROM (
			SELECT DISTINCT ON (photographer) * FROM photos
			WHERE spot_id = $1 AND thumb_url <> ''
			ORDER BY photographer, views DESC NULLS LAST, md5(source_id)
		) one_each ORDER BY views DESC NULLS LAST, md5(source_id) LIMIT 24`, id)
	if err != nil {
		serverError(w, err)
		return
	}
	photos, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Photo, error) {
		var p Photo
		err := r.Scan(&p.Title, &p.Photographer, &p.ThumbURL, &p.PageURL, &p.License, &p.Source, &p.TakenLocal)
		return p, err
	})
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"spot":   sp,
		"photos": photos,
		"sun":    sunTimes(time.Now().In(s.Location), sp.Lat, sp.Lon),
	})
}

func (s *Server) sun(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lon, err2 := strconv.ParseFloat(q.Get("lon"), 64)
	if err1 != nil || err2 != nil {
		httpError(w, http.StatusBadRequest, "lat and lon must be numbers")
		return
	}
	day := time.Now().In(s.Location)
	if d := q.Get("date"); d != "" {
		t, err := time.ParseInLocation("2006-01-02", d, s.Location)
		if err != nil {
			httpError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
			return
		}
		day = t.Add(12 * time.Hour)
	}
	writeJSON(w, sunTimes(day, lat, lon))
}

type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type SunDay struct {
	Date          string    `json:"date"`
	Sunrise       time.Time `json:"sunrise"`
	Sunset        time.Time `json:"sunset"`
	MorningBlue   Window    `json:"morning_blue_hour"`
	MorningGolden Window    `json:"morning_golden_hour"`
	EveningGolden Window    `json:"evening_golden_hour"`
	EveningBlue   Window    `json:"evening_blue_hour"`
}

// sunTimes uses suncalc's angles: golden hour while the sun is below 6°,
// blue hour approximated as civil twilight (sun between 0° and -6°).
func sunTimes(day time.Time, lat, lon float64) SunDay {
	// suncalc keys the computation off the instant passed in; use local noon.
	noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, day.Location())
	t := suncalc.GetTimes(noon, lat, lon)
	at := func(n suncalc.DayTimeName) time.Time { return t[n].Value.In(day.Location()) }
	return SunDay{
		Date:          noon.Format("2006-01-02"),
		Sunrise:       at(suncalc.Sunrise),
		Sunset:        at(suncalc.Sunset),
		MorningBlue:   Window{at(suncalc.Dawn), at(suncalc.Sunrise)},
		MorningGolden: Window{at(suncalc.Sunrise), at(suncalc.GoldenHourEnd)},
		EveningGolden: Window{at(suncalc.GoldenHour), at(suncalc.Sunset)},
		EveningBlue:   Window{at(suncalc.Sunset), at(suncalc.Dusk)},
	}
}

func parseFloats(s string, n int) ([]float64, bool) {
	parts := strings.Split(s, ",")
	if len(parts) != n {
		return nil, false
	}
	out := make([]float64, n)
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, false
		}
		out[i] = f
	}
	return out, true
}

func clampInt(s string, def, lo, hi int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return max(lo, min(hi, n))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("api: %v", err)
	httpError(w, http.StatusInternalServerError, "internal error")
}
