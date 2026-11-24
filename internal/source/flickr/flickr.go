// Package flickr fetches geotagged photos via flickr.photos.search.
//
// Two API quirks drive the design:
//   - A geo search with no date filter only returns the last 12 hours, so we
//     always search by upload-date window.
//   - A single query returns at most ~4000 distinct results however many
//     pages you request, so when a (box, window) has more than that we split
//     the time window, and once the window is short, the box.
package flickr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"photospots/internal/model"
	"photospots/internal/source"
)

const (
	endpoint   = "https://api.flickr.com/services/rest/"
	perPage    = 250
	maxResults = 4000
	minWindow  = 7 * 24 * time.Hour
)

type Client struct {
	APIKey string
	HTTP   *http.Client
	// Flickr allows 3600 calls/hour per key; 1.1 s keeps us safely under.
	Delay time.Duration
}

func New(apiKey string) *Client {
	return &Client{APIKey: apiKey, HTTP: &http.Client{Timeout: 60 * time.Second}, Delay: 1100 * time.Millisecond}
}

// Fetch returns photos in box uploaded within [from, to).
func (c *Client) Fetch(ctx context.Context, box model.BBox, from, to time.Time, emit func([]model.Photo) error) error {
	first, err := c.search(ctx, box, from, to, 1)
	if err != nil {
		return err
	}
	if first.Total > maxResults {
		if to.Sub(from) > minWindow {
			mid := from.Add(to.Sub(from) / 2)
			if err := c.Fetch(ctx, box, from, mid, emit); err != nil {
				return err
			}
			return c.Fetch(ctx, box, mid, to, emit)
		}
		if box.MaxLat-box.MinLat > 0.001 {
			for _, q := range box.Quadrants() {
				if err := c.Fetch(ctx, q, from, to, emit); err != nil {
					return err
				}
			}
			return nil
		}
		// Tiny box and short window but still >4000: take what we can get.
	}
	if err := emit(first.Photos); err != nil {
		return err
	}
	for page := 2; page <= first.Pages && page*perPage <= maxResults; page++ {
		res, err := c.search(ctx, box, from, to, page)
		if err != nil {
			return err
		}
		if err := emit(res.Photos); err != nil {
			return err
		}
	}
	return nil
}

type result struct {
	Total, Pages int
	Photos       []model.Photo
}

// flex decodes a JSON value that Flickr sends as either a string or a number.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	*f = flex(strings.Trim(string(b), `"`))
	return nil
}

func (f flex) Int() int       { n, _ := strconv.Atoi(string(f)); return n }
func (f flex) Float() float64 { n, _ := strconv.ParseFloat(string(f), 64); return n }

type apiPhoto struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Title       string `json:"title"`
	DateTaken   string `json:"datetaken"`
	Granularity flex   `json:"datetakengranularity"`
	Unknown     flex   `json:"datetakenunknown"`
	OwnerName   string `json:"ownername"`
	Views       flex   `json:"views"`
	Tags        string `json:"tags"`
	Latitude    flex   `json:"latitude"`
	Longitude   flex   `json:"longitude"`
	Accuracy    flex   `json:"accuracy"`
	URLM        string `json:"url_m"`
	License     flex   `json:"license"`
}

func (c *Client) search(ctx context.Context, box model.BBox, from, to time.Time, page int) (*result, error) {
	p := url.Values{}
	p.Set("method", "flickr.photos.search")
	p.Set("api_key", c.APIKey)
	p.Set("format", "json")
	p.Set("nojsoncallback", "1")
	p.Set("bbox", fmt.Sprintf("%f,%f,%f,%f", box.MinLon, box.MinLat, box.MaxLon, box.MaxLat))
	p.Set("has_geo", "1")
	p.Set("accuracy", "15") // street level or better; drops city-level geotags
	p.Set("content_type", "1")
	p.Set("media", "photos")
	p.Set("min_upload_date", strconv.FormatInt(from.Unix(), 10))
	p.Set("max_upload_date", strconv.FormatInt(to.Unix(), 10))
	p.Set("extras", "geo,date_taken,owner_name,views,url_m,license,tags")
	p.Set("sort", "date-posted-asc")
	p.Set("per_page", strconv.Itoa(perPage))
	p.Set("page", strconv.Itoa(page))

	var res struct {
		Stat    string `json:"stat"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Photos  struct {
			Page  int        `json:"page"`
			Pages int        `json:"pages"`
			Total flex       `json:"total"`
			Photo []apiPhoto `json:"photo"`
		} `json:"photos"`
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 3 * time.Second)
		}
		time.Sleep(c.Delay)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+p.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || err != nil {
			lastErr = fmt.Errorf("flickr: HTTP %d: %v", resp.StatusCode, err)
			continue
		}
		if res.Stat != "ok" {
			return nil, fmt.Errorf("flickr: error %d: %s", res.Code, res.Message)
		}
		out := &result{Total: res.Photos.Total.Int(), Pages: res.Photos.Pages}
		for _, ap := range res.Photos.Photo {
			out.Photos = append(out.Photos, toPhoto(ap))
		}
		return out, nil
	}
	return nil, lastErr
}

// https://www.flickr.com/services/api/flickr.photos.licenses.getInfo.html
var licenses = map[int]string{
	0: "All Rights Reserved", 1: "CC BY-NC-SA 2.0", 2: "CC BY-NC 2.0", 3: "CC BY-NC-ND 2.0",
	4: "CC BY 2.0", 5: "CC BY-SA 2.0", 6: "CC BY-ND 2.0", 7: "No known copyright restrictions",
	8: "United States Government Work", 9: "CC0 1.0", 10: "Public Domain Mark 1.0",
	11: "CC BY 4.0", 12: "CC BY-SA 4.0", 13: "CC BY-ND 4.0", 14: "CC BY-NC 4.0",
	15: "CC BY-NC-SA 4.0", 16: "CC BY-NC-ND 4.0",
}

func toPhoto(ap apiPhoto) model.Photo {
	views := ap.Views.Int()
	ph := model.Photo{
		Source:       "flickr",
		SourceID:     ap.ID,
		Photographer: "flickr:" + ap.Owner, // owner NSID is stable; names are not
		Title:        ap.Title,
		Lat:          ap.Latitude.Float(),
		Lon:          ap.Longitude.Float(),
		ThumbURL:     ap.URLM,
		PageURL:      "https://www.flickr.com/photos/" + ap.Owner + "/" + ap.ID,
		License:      licenses[ap.License.Int()],
		Views:        &views,
	}
	if ap.Tags != "" {
		ph.Tags = strings.Fields(ap.Tags)
	}
	// Granularity 0 means an exact timestamp; anything else is month/year/circa.
	// 4 = month known, 6 = year only, 8 = circa: drop those, the month is a guess.
	if ap.Unknown.Int() == 0 && ap.Granularity.Int() <= 4 {
		if t, hasTime, ok := source.ParseTaken(ap.DateTaken); ok {
			ph.TakenLocal = &t
			ph.HasTime = hasTime && ap.Granularity.Int() == 0
		}
	}
	return ph
}
