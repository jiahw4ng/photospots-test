// Package model holds the types shared by sources, storage, clustering and the API.
package model

import "time"

// Photo is one geotagged photo from any source.
type Photo struct {
	Source       string     `json:"source"`
	SourceID     string     `json:"source_id"`
	Photographer string     `json:"photographer"`
	Title        string     `json:"title"`
	TakenLocal   *time.Time `json:"taken_local,omitempty"`
	HasTime      bool       `json:"-"` // false when the source gave only a date
	Lat          float64    `json:"lat"`
	Lon          float64    `json:"lon"`
	ThumbURL     string     `json:"thumb_url"`
	PageURL      string     `json:"page_url"`
	License      string     `json:"license"`
	Views        *int       `json:"views,omitempty"`
	Tags         []string   `json:"tags,omitempty"`
}

// BBox is a lon/lat bounding box.
type BBox struct {
	MinLon, MinLat, MaxLon, MaxLat float64
}

// Quadrants splits the box into four equal parts.
func (b BBox) Quadrants() [4]BBox {
	midLon, midLat := (b.MinLon+b.MaxLon)/2, (b.MinLat+b.MaxLat)/2
	return [4]BBox{
		{b.MinLon, b.MinLat, midLon, midLat},
		{midLon, b.MinLat, b.MaxLon, midLat},
		{b.MinLon, midLat, midLon, b.MaxLat},
		{midLon, midLat, b.MaxLon, b.MaxLat},
	}
}

// Region is an area we ingest and cluster as a unit.
type Region struct {
	Name     string
	BBox     BBox
	TimeZone string
}

// Regions known to the CLI. Add more here as coverage grows.
var Regions = map[string]Region{
	"singapore": {
		Name:     "singapore",
		BBox:     BBox{MinLon: 103.59, MinLat: 1.20, MaxLon: 104.05, MaxLat: 1.48},
		TimeZone: "Asia/Singapore",
	},
}
