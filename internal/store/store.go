// Package store is the PostGIS-backed persistence layer.
package store

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"photospots/internal/model"
)

//go:embed schema.sql
var schema string

type Store struct{ DB *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{DB: pool}, nil
}

func (s *Store) Close() { s.DB.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, schema)
	return err
}

// UpsertPhotos inserts or refreshes photos. spot_id is left untouched.
func (s *Store) UpsertPhotos(ctx context.Context, photos []model.Photo) error {
	b := &pgx.Batch{}
	for _, p := range photos {
		var hour, month *int
		if p.TakenLocal != nil {
			m := int(p.TakenLocal.Month())
			month = &m
			if p.HasTime {
				h := p.TakenLocal.Hour()
				hour = &h
			}
		}
		tags := p.Tags
		if tags == nil {
			tags = []string{}
		}
		b.Queue(`
			INSERT INTO photos (source, source_id, photographer, title, taken_local, taken_hour, taken_month,
			                    geom, thumb_url, page_url, license, views, tags, fetched_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7, ST_SetSRID(ST_MakePoint($8,$9),4326), $10,$11,$12,$13,$14, now())
			ON CONFLICT (source, source_id) DO UPDATE SET
				photographer = EXCLUDED.photographer, title = EXCLUDED.title,
				taken_local = EXCLUDED.taken_local, taken_hour = EXCLUDED.taken_hour,
				taken_month = EXCLUDED.taken_month, geom = EXCLUDED.geom,
				thumb_url = EXCLUDED.thumb_url, page_url = EXCLUDED.page_url,
				license = EXCLUDED.license, views = EXCLUDED.views, tags = EXCLUDED.tags,
				fetched_at = now()`,
			p.Source, p.SourceID, p.Photographer, p.Title, p.TakenLocal, hour, month,
			p.Lon, p.Lat, p.ThumbURL, p.PageURL, p.License, p.Views, tags)
	}
	return s.DB.SendBatch(ctx, b).Close()
}

func (s *Store) GetState(ctx context.Context, key string) (string, error) {
	var v string
	err := s.DB.QueryRow(ctx, `SELECT value FROM ingest_state WHERE key = $1`, key).Scan(&v)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO ingest_state (key, value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

// LocatedPhoto is the minimum the clusterer needs.
type LocatedPhoto struct {
	Source, SourceID, Photographer string
	Lon, Lat                       float64
}

func (s *Store) PhotosIn(ctx context.Context, box model.BBox) ([]LocatedPhoto, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT source, source_id, photographer, ST_X(geom), ST_Y(geom) FROM photos
		WHERE geom && ST_MakeEnvelope($1,$2,$3,$4,4326)`,
		box.MinLon, box.MinLat, box.MaxLon, box.MaxLat)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (LocatedPhoto, error) {
		var p LocatedPhoto
		err := r.Scan(&p.Source, &p.SourceID, &p.Photographer, &p.Lon, &p.Lat)
		return p, err
	})
}
