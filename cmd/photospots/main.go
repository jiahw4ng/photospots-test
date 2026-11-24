// Command photospots ingests geotagged photos, clusters them into spots and
// serves them.
//
//	photospots migrate
//	photospots ingest-commons [-region singapore]
//	photospots ingest-flickr  [-region singapore] [-since 2004-01-01]   (needs FLICKR_API_KEY)
//	photospots cluster        [-region singapore]
//	photospots serve          [-addr :8080]
//
// DATABASE_URL defaults to postgres://spots:spots@localhost:5432/photospots.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"time"

	"photospots/internal/api"
	"photospots/internal/cluster"
	"photospots/internal/model"
	"photospots/internal/source/commons"
	"photospots/internal/source/flickr"
	"photospots/internal/spots"
	"photospots/internal/store"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, args := os.Args[1], os.Args[2:]
	fsFlags := flag.NewFlagSet(cmd, flag.ExitOnError)
	regionName := fsFlags.String("region", "singapore", "region to work on")
	addr := fsFlags.String("addr", ":8080", "listen address (serve)")
	since := fsFlags.String("since", "", "Flickr: only photos uploaded after this date (default: resume from last run, else 2004-01-01)")
	fsFlags.Parse(args)

	region, ok := model.Regions[*regionName]
	if !ok {
		log.Fatalf("unknown region %q (known: %v)", *regionName, regionNames())
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://spots:spots@localhost:5432/photospots"
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	switch cmd {
	case "migrate":
		check(st.Migrate(ctx))
		log.Print("schema up to date")
	case "ingest-commons":
		check(st.Migrate(ctx))
		total := 0
		start := time.Now()
		cc := commons.New()
		if os.Getenv("VERBOSE") != "" {
			cc.Log = log.Printf
		}
		err := cc.Fetch(ctx, region.BBox, func(ph []model.Photo) error {
			total += len(ph)
			if total%1000 < len(ph) {
				log.Printf("commons: %d photos so far (%s)", total, time.Since(start).Round(time.Second))
			}
			return st.UpsertPhotos(ctx, ph)
		})
		check(err)
		log.Printf("commons: done, %d photos in %s", total, time.Since(start).Round(time.Second))
	case "ingest-flickr":
		key := os.Getenv("FLICKR_API_KEY")
		if key == "" {
			log.Fatal("set FLICKR_API_KEY (get one at https://www.flickr.com/services/apps/create/)")
		}
		check(st.Migrate(ctx))
		stateKey := "flickr:" + region.Name + ":uploaded_until"
		from := time.Date(2004, 1, 1, 0, 0, 0, 0, time.UTC)
		if *since != "" {
			from, err = time.Parse("2006-01-02", *since)
			check(err)
		} else if v, _ := st.GetState(ctx, stateKey); v != "" {
			from, err = time.Parse(time.RFC3339, v)
			check(err)
		}
		to := time.Now().UTC()
		log.Printf("flickr: fetching uploads %s → %s", from.Format("2006-01-02"), to.Format("2006-01-02"))
		total := 0
		// Walk year by year so progress is saved and an interrupted run resumes.
		for cur := from; cur.Before(to); {
			next := cur.AddDate(1, 0, 0)
			if next.After(to) {
				next = to
			}
			n := 0
			err := flickr.New(key).Fetch(ctx, region.BBox, cur, next, func(ph []model.Photo) error {
				n += len(ph)
				return st.UpsertPhotos(ctx, ph)
			})
			check(err)
			total += n
			log.Printf("flickr: %s: %d photos", cur.Format("2006"), n)
			check(st.SetState(ctx, stateKey, next.Format(time.RFC3339)))
			cur = next
		}
		log.Printf("flickr: done, %d photos", total)
	case "cluster":
		start := time.Now()
		sum, err := spots.Build(ctx, st, region, cluster.DefaultParams)
		check(err)
		log.Printf("cluster: %d photos → %d photographer-locations → %d spots (%d photos in spots) in %s",
			sum.Photos, sum.PhotographerPoints, sum.Spots, sum.PhotosInSpots, time.Since(start).Round(time.Millisecond))
	case "serve":
		loc, err := time.LoadLocation(region.TimeZone)
		check(err)
		srv := &http.Server{Addr: *addr, Handler: (&api.Server{Store: st, Location: loc}).Handler()}
		go func() {
			<-ctx.Done()
			srv.Shutdown(context.Background())
		}()
		log.Printf("serving on http://localhost%s", *addr)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	default:
		usage()
	}
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func regionNames() []string {
	var out []string
	for k := range model.Regions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: photospots migrate | ingest-commons | ingest-flickr | cluster | serve  [-region singapore]")
	os.Exit(2)
}
