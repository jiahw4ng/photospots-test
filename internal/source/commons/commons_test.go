package commons

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"photospots/internal/model"
)

// fakeCommons answers geosearch and pageids queries. The first geosearch
// call fails with cirrussearch-too-busy-error; boxes wider than 0.15° are
// "toobig"; every other box holds exactly one file.
func fakeCommons(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var geoCalls atomic.Int32
	var busySent atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		if q.Get("list") == "geosearch" {
			geoCalls.Add(1)
			if !busySent.Swap(true) {
				fmt.Fprint(w, `{"error":{"code":"cirrussearch-too-busy-error","info":"Search is currently too busy."}}`)
				return
			}
			var top, left, bottom, right float64
			fmt.Sscanf(strings.ReplaceAll(q.Get("gsbbox"), "|", " "), "%f %f %f %f", &top, &left, &bottom, &right)
			if right-left > 0.15 {
				fmt.Fprint(w, `{"error":{"code":"toobig","info":"Bounding box is too big"}}`)
				return
			}
			id := int(left*10000) + int(bottom*10000)*7
			fmt.Fprintf(w, `{"query":{"geosearch":[{"pageid":%d,"lat":%f,"lon":%f}]}}`, id, bottom, left)
			return
		}
		if strings.Contains(q.Get("prop"), "coordinates") {
			t.Errorf("details request asked for coordinates; they come from geosearch")
		}
		var pages []map[string]any
		for _, s := range strings.Split(q.Get("pageids"), "|") {
			id, _ := strconv.Atoi(s)
			pages = append(pages, map[string]any{
				"pageid": id,
				"title":  fmt.Sprintf("File:Photo %d.jpg", id),
				"imageinfo": []map[string]any{{
					"user": "Panoramio upload bot", "thumburl": "t", "descriptionurl": "d", "mime": "image/jpeg",
					"extmetadata": map[string]any{
						"Artist":           map[string]string{"value": `<a href="x">Jane</a>`},
						"DateTimeOriginal": map[string]string{"value": "2015:06:01 18:30:00"},
					},
				}},
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"pages": pages}})
	}))
	t.Cleanup(srv.Close)
	return srv, &geoCalls
}

func testClient(url string) *Client {
	c := New()
	c.Endpoint, c.Delay, c.RetryBase = url, 0, time.Millisecond
	return c
}

func TestFetchRetriesBusySplitsAndResumes(t *testing.T) {
	srv, geoCalls := fakeCommons(t)
	box := model.BBox{MinLon: 103.6, MinLat: 1.2, MaxLon: 103.8, MaxLat: 1.4} // 0.2° wide: splits once

	done := map[model.BBox]bool{}
	c := testClient(srv.URL)
	c.IsDone = func(b model.BBox) bool { return done[b] }
	c.MarkDone = func(b model.BBox) error { done[b] = true; return nil }

	var photos []model.Photo
	err := c.Fetch(context.Background(), box, func(p []model.Photo) error { photos = append(photos, p...); return nil })
	if err != nil {
		t.Fatalf("busy error should be retried, got %v", err)
	}
	if len(photos) != 4 {
		t.Fatalf("got %d photos, want 4 (one per quadrant)", len(photos))
	}
	if photos[0].Lat < 1.2 || photos[0].Lon < 103.6 {
		t.Fatalf("coordinates not taken from geosearch: %+v", photos[0])
	}
	if photos[0].Photographer != "jane" || !photos[0].HasTime || photos[0].TakenLocal.Hour() != 18 {
		t.Fatalf("bad photo parse: %+v", photos[0])
	}
	if !done[box] {
		t.Fatal("parent box should be marked done")
	}

	// A second run with the same checkpoints makes no geosearch calls at all.
	before := geoCalls.Load()
	if err := c.Fetch(context.Background(), box, func([]model.Photo) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if geoCalls.Load() != before {
		t.Fatalf("resumed run made %d new geosearch calls, want 0", geoCalls.Load()-before)
	}
}
