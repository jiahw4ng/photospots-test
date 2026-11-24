package spots

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"photospots/internal/cluster"
	"photospots/internal/model"
	"photospots/internal/store"
)

// Integration test: needs a PostGIS database in TEST_DATABASE_URL.
// Uses a patch of open ocean so it never touches real data.
func TestBuildAgainstDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	region := model.Region{Name: "test-ocean", BBox: model.BBox{MinLon: -30.01, MinLat: -0.01, MaxLon: -29.99, MaxLat: 0.01}, TimeZone: "UTC"}
	cleanup := func() {
		st.DB.Exec(ctx, `DELETE FROM photos WHERE source = 'test'`)
		st.DB.Exec(ctx, `DELETE FROM spots WHERE geom && ST_MakeEnvelope(-30.01,-0.01,-29.99,0.01,4326)`)
	}
	cleanup()
	defer cleanup()

	var photos []model.Photo
	add := func(prefix string, lon, lat float64, photographers, each, hour int) {
		for p := 0; p < photographers; p++ {
			for k := 0; k < each; k++ {
				ts := time.Date(2020, time.Month(1+p%12), 1, hour, 0, 0, 0, time.UTC)
				photos = append(photos, model.Photo{
					Source: "test", SourceID: fmt.Sprintf("%s-%d-%d", prefix, p, k),
					Photographer: fmt.Sprintf("%s-person-%d", prefix, p),
					Lon:          lon + float64(k)*0.00001, Lat: lat,
					TakenLocal: &ts, HasTime: true, ThumbURL: "x",
				})
			}
		}
	}
	add("sunset", -30.0, 0.0, 6, 2, 18)       // 6 photographers -> a spot, peak at 18h
	add("balcony", -29.995, 0.005, 1, 300, 9) // 1 photographer, 300 photos -> not a spot
	if err := st.UpsertPhotos(ctx, photos); err != nil {
		t.Fatal(err)
	}

	sum, err := Build(ctx, st, region, cluster.DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Spots != 1 {
		t.Fatalf("got %d spots, want 1 (summary %+v)", sum.Spots, sum)
	}
	var photographers, photoCount int
	var hours []int32
	err = st.DB.QueryRow(ctx, `SELECT photographer_count, photo_count, hour_histogram FROM spots
		WHERE geom && ST_MakeEnvelope(-30.01,-0.01,-29.99,0.01,4326)`).Scan(&photographers, &photoCount, &hours)
	if err != nil {
		t.Fatal(err)
	}
	if photographers != 6 || photoCount != 12 || hours[18] != 6 {
		t.Fatalf("got photographers=%d photos=%d hours[18]=%d, want 6, 12, 6", photographers, photoCount, hours[18])
	}
}
