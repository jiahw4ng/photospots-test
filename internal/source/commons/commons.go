// Package commons fetches geotagged photos from Wikimedia Commons.
//
// Commons geosearch returns at most 500 files per bounding box and refuses
// boxes that are too big, so we split boxes recursively until each one
// returns fewer than 500 results.
package commons

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
	endpoint  = "https://commons.wikimedia.org/w/api.php"
	pageLimit = 500
	// Boxes smaller than this (~30 m) are accepted even when full: they are
	// single points with hundreds of uploads (e.g. one bus stop).
	minBoxDeg = 0.0003
)

type Client struct {
	HTTP      *http.Client
	UserAgent string
	Delay     time.Duration // pause between requests, to be polite
	RetryBase time.Duration // unit for retry backoff
	Endpoint  string
	Log       func(format string, args ...any)

	// Optional checkpointing so an interrupted import resumes: boxes for
	// which IsDone returns true are skipped, and MarkDone is called once a
	// box (and everything inside it) has been fully imported.
	IsDone   func(model.BBox) bool
	MarkDone func(model.BBox) error
}

func (c *Client) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

func New() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		UserAgent: "photospots/0.1 (hobby photo-spot finder)",
		Delay:     time.Second,
		RetryBase: time.Second,
		Endpoint:  endpoint,
	}
}

// Fetch walks the box and calls emit for each batch of photos found.
func (c *Client) Fetch(ctx context.Context, box model.BBox, emit func([]model.Photo) error) error {
	if c.IsDone != nil && c.IsDone(box) {
		return nil
	}
	if err := c.fetch(ctx, box, emit); err != nil {
		return err
	}
	if c.MarkDone != nil {
		return c.MarkDone(box)
	}
	return nil
}

func (c *Client) fetch(ctx context.Context, box model.BBox, emit func([]model.Photo) error) error {
	ids, err := c.pageIDs(ctx, box)
	c.logf("box %.4f,%.4f..%.4f,%.4f: %d files, err=%v", box.MinLon, box.MinLat, box.MaxLon, box.MaxLat, len(ids), err)
	if err != nil {
		if apiErrCode(err) == "toobig" {
			return c.splitFetch(ctx, box, emit)
		}
		return err
	}
	if len(ids) >= pageLimit && box.MaxLat-box.MinLat > minBoxDeg {
		return c.splitFetch(ctx, box, emit)
	}
	if len(ids) == 0 {
		return nil
	}
	// imageinfo accepts at most 50 titles per request.
	for start := 0; start < len(ids); start += 50 {
		end := min(start+50, len(ids))
		photos, err := c.details(ctx, ids[start:end])
		if err != nil {
			return err
		}
		if err := emit(photos); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) splitFetch(ctx context.Context, box model.BBox, emit func([]model.Photo) error) error {
	for _, q := range box.Quadrants() {
		if err := c.Fetch(ctx, q, emit); err != nil {
			return err
		}
	}
	return nil
}

type apiError struct{ Code, Info string }

func (e *apiError) Error() string { return "commons: " + e.Code + ": " + e.Info }

func apiErrCode(err error) string {
	if e, ok := err.(*apiError); ok {
		return e.Code
	}
	return ""
}

// Temporary server-side conditions worth waiting out.
var retryable = map[string]bool{
	"maxlag":                          true,
	"cirrussearch-too-busy-error":     true,
	"ratelimited":                     true,
	"readonly":                        true,
	"internal_api_error_DBQueryError": true,
}

func (c *Client) get(ctx context.Context, params url.Values, out any) error {
	params.Set("format", "json")
	params.Set("formatversion", "2")
	params.Set("maxlag", "5")
	var lastErr error
	wait := time.Duration(0)
	for attempt := 0; attempt < 10; attempt++ {
		if err := sleep(ctx, c.Delay+wait); err != nil {
			return err
		}
		wait = time.Duration(attempt+1) * 2 * c.RetryBase
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"?"+params.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			c.logf("request failed (attempt %d): %v", attempt+1, err)
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			c.logf("HTTP %d (attempt %d), retry-after=%q", resp.StatusCode, attempt+1, resp.Header.Get("Retry-After"))
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			// Honour Retry-After, and slow down for the rest of the run.
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s)*time.Second + time.Second
			}
			c.Delay = min(c.Delay*2, 5*time.Second)
			lastErr = fmt.Errorf("commons: HTTP %d (rate limited)", resp.StatusCode)
			continue
		}
		var env struct {
			Error *apiError `json:"error"`
		}
		var raw json.RawMessage
		err = json.NewDecoder(resp.Body).Decode(&raw)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			lastErr = fmt.Errorf("commons: HTTP %d: %v", resp.StatusCode, err)
			continue
		}
		if err := json.Unmarshal(raw, &env); err == nil && env.Error != nil {
			if retryable[env.Error.Code] {
				c.logf("API busy (%s), attempt %d; retrying", env.Error.Code, attempt+1)
				lastErr = env.Error
				wait = time.Duration(attempt+1) * 5 * c.RetryBase
				continue
			}
			return env.Error
		}
		return json.Unmarshal(raw, out)
	}
	return lastErr
}

func (c *Client) pageIDs(ctx context.Context, box model.BBox) ([]int, error) {
	p := url.Values{}
	p.Set("action", "query")
	p.Set("list", "geosearch")
	p.Set("gsnamespace", "6")
	p.Set("gslimit", strconv.Itoa(pageLimit))
	p.Set("gsprop", "")
	p.Set("gsbbox", fmt.Sprintf("%f|%f|%f|%f", box.MaxLat, box.MinLon, box.MinLat, box.MaxLon))
	var res struct {
		Query struct {
			Geosearch []struct {
				PageID int `json:"pageid"`
			} `json:"geosearch"`
		} `json:"query"`
	}
	if err := c.get(ctx, p, &res); err != nil {
		return nil, err
	}
	ids := make([]int, len(res.Query.Geosearch))
	for i, g := range res.Query.Geosearch {
		ids[i] = g.PageID
	}
	return ids, nil
}

type metaValue struct {
	Value string `json:"value"`
}

type page struct {
	PageID      int    `json:"pageid"`
	Title       string `json:"title"`
	Coordinates []struct {
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
		Primary bool    `json:"primary"`
	} `json:"coordinates"`
	ImageInfo []struct {
		User           string               `json:"user"`
		ThumbURL       string               `json:"thumburl"`
		DescriptionURL string               `json:"descriptionurl"`
		Mime           string               `json:"mime"`
		ExtMetadata    map[string]metaValue `json:"extmetadata"`
	} `json:"imageinfo"`
}

func (c *Client) details(ctx context.Context, ids []int) ([]model.Photo, error) {
	strIDs := make([]string, len(ids))
	for i, id := range ids {
		strIDs[i] = strconv.Itoa(id)
	}
	base := url.Values{}
	base.Set("action", "query")
	base.Set("pageids", strings.Join(strIDs, "|"))
	base.Set("prop", "coordinates|imageinfo")
	base.Set("coprimary", "primary")
	base.Set("iiprop", "url|user|mime|extmetadata")
	base.Set("iiurlwidth", "400")
	base.Set("iiextmetadatafilter", "DateTimeOriginal|LicenseShortName|Artist|ObjectName")

	pages := map[int]*page{}
	cont := map[string]string{}
	for {
		p := url.Values{}
		for k, v := range base {
			p[k] = v
		}
		for k, v := range cont {
			p.Set(k, v)
		}
		var res struct {
			Continue map[string]any `json:"continue"`
			Query    struct {
				Pages []page `json:"pages"`
			} `json:"query"`
		}
		if err := c.get(ctx, p, &res); err != nil {
			return nil, err
		}
		for _, pg := range res.Query.Pages {
			pg := pg
			if have, ok := pages[pg.PageID]; ok {
				// Continuations return the same pages with the props not yet sent.
				if len(have.Coordinates) == 0 {
					have.Coordinates = pg.Coordinates
				}
				if len(have.ImageInfo) == 0 {
					have.ImageInfo = pg.ImageInfo
				}
				continue
			}
			pages[pg.PageID] = &pg
		}
		if len(res.Continue) == 0 {
			break
		}
		cont = map[string]string{}
		for k, v := range res.Continue {
			cont[k] = fmt.Sprint(v)
		}
	}

	out := make([]model.Photo, 0, len(pages))
	for _, pg := range pages {
		if ph, ok := toPhoto(pg); ok {
			out = append(out, ph)
		}
	}
	return out, nil
}

func toPhoto(pg *page) (model.Photo, bool) {
	if len(pg.Coordinates) == 0 || len(pg.ImageInfo) == 0 {
		return model.Photo{}, false
	}
	ii := pg.ImageInfo[0]
	if !strings.HasPrefix(ii.Mime, "image/") || ii.Mime == "image/svg+xml" {
		return model.Photo{}, false // maps, diagrams, PDFs, videos
	}
	meta := func(k string) string { return source.StripHTML(ii.ExtMetadata[k].Value) }

	photographer := meta("Artist")
	if photographer == "" || len(photographer) > 120 {
		photographer = ii.User
	}
	title := meta("ObjectName")
	if title == "" {
		title = strings.TrimSuffix(strings.TrimPrefix(pg.Title, "File:"), extOf(pg.Title))
	}
	ph := model.Photo{
		Source:       "commons",
		SourceID:     strconv.Itoa(pg.PageID),
		Photographer: strings.ToLower(strings.TrimSpace(photographer)),
		Title:        title,
		Lat:          pg.Coordinates[0].Lat,
		Lon:          pg.Coordinates[0].Lon,
		ThumbURL:     ii.ThumbURL,
		PageURL:      ii.DescriptionURL,
		License:      meta("LicenseShortName"),
	}
	if t, hasTime, ok := source.ParseTaken(ii.ExtMetadata["DateTimeOriginal"].Value); ok {
		ph.TakenLocal, ph.HasTime = &t, hasTime
	}
	return ph, true
}

func extOf(title string) string {
	if i := strings.LastIndex(title, "."); i >= 0 {
		return title[i:]
	}
	return ""
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
