# photospots

Finds good photography spots from where people actually take photos.
It pulls geotagged photos from Wikimedia Commons and Flickr, groups them into
spots, and serves a map that shows each spot's best time of day, best months
and today's golden and blue hours.

## How it works

```
ingest-commons / ingest-flickr  ->  photos table (PostGIS)
cluster                         ->  spots table (rebuilt each run)
serve                           ->  JSON API + map page; reads only the database
```

- **Spots are ranked by distinct photographers, not photo count.** Each
  photographer counts once per 20 m cell, so one person's 400 balcony shots
  cannot create a spot. Histograms count photographers too.
- **Clustering** is DBSCAN (`internal/cluster`) with one change: clusters
  wider than 200 m are re-clustered with half the radius. This stops busy
  areas like Marina Bay from chaining into one blob.
- **No external API calls per request.** Ingests run on a schedule; sun
  times are computed locally.

## Run it with Docker (easiest)

```bash
docker compose up -d --build                        # database + web server
docker compose run --rm app ingest-commons          # import Singapore photos (slow-ish, polite pace)
docker compose run --rm app cluster                 # build spots
```

Open http://localhost:8080. Re-run `cluster` after each import.
For Flickr, put `FLICKR_API_KEY=...` in a `.env` file next to `docker-compose.yml`,
then `docker compose run --rm app ingest-flickr`.

## Run it without Docker

Needs Go 1.25+ and PostgreSQL with PostGIS.

```bash
createdb photospots && psql photospots -c 'CREATE EXTENSION postgis'
export DATABASE_URL=postgres://localhost/photospots   # default: postgres://spots:spots@localhost:5432/photospots
go build -o bin/photospots ./cmd/photospots

bin/photospots ingest-commons           # Singapore, no key needed
FLICKR_API_KEY=... bin/photospots ingest-flickr
bin/photospots cluster
bin/photospots serve                    # http://localhost:8080
```

`ingest-flickr` resumes where the last run stopped, so running it daily only
fetches new uploads. Get a key at https://www.flickr.com/services/apps/create/
(non-commercial keys are free; charging money needs a commercial key).

## API

| Endpoint | Purpose |
|---|---|
| `GET /api/spots?bbox=minLon,minLat,maxLon,maxLat` | Spots in a map view, best first |
| `GET /api/spots?lat=&lon=&radius_km=5` | Spots near a point, with distance |
| `GET /api/spots/{id}` | One spot, up to 24 photos (one per photographer), today's sun times |
| `GET /api/sun?lat=&lon=&date=YYYY-MM-DD` | Golden and blue hours |

All list endpoints take `min_photographers` (default 3) and `limit`.

## Attribution

Every photo links to its source page with photographer and licence. Images
are loaded from Wikimedia's and Flickr's servers, not re-hosted.
