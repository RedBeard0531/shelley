package db

import (
	"context"
	"database/sql"
	"errors"

	"shelley.exe.dev/db/generated"
)

// builtinProfiles is the single source of shipped profile settings.
var builtinProfiles = map[string]string{"Default": "{}"}

func (db *DB) updateBuiltinProfiles(ctx context.Context) error {
	return db.syncBuiltinProfiles(ctx, builtinProfiles)
}

// syncBuiltinProfiles installs each built-in once and updates only those the
// user has not edited. Registration survives deletion; updates never recreate it.
func (db *DB) syncBuiltinProfiles(ctx context.Context, profiles map[string]string) error {
	return db.QueriesTx(ctx, func(q *generated.Queries) error {
		for name, settings := range profiles {
			_, err := q.GetBuiltinProfile(ctx, name)
			if errors.Is(err, sql.ErrNoRows) {
				if err := q.CreateBuiltinProfile(ctx, generated.CreateBuiltinProfileParams{Name: name, Settings: settings}); err != nil {
					return err
				}
				if err := q.RegisterBuiltinProfile(ctx, name); err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if err := q.UpdateBuiltinProfile(ctx, generated.UpdateBuiltinProfileParams{Name: name, Settings: settings}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RestoreBuiltinProfile resets a built-in to its current shipped settings and
// re-enables automatic updates without changing default selection or chats.
func (db *DB) RestoreBuiltinProfile(ctx context.Context, name string) (*generated.Profile, error) {
	settings, ok := builtinProfiles[name]
	if !ok {
		return nil, sql.ErrNoRows
	}
	var profile generated.Profile
	err := db.QueriesTx(ctx, func(q *generated.Queries) (err error) {
		profile, err = q.RestoreBuiltinProfile(ctx, generated.RestoreBuiltinProfileParams{Name: name, Settings: settings})
		return err
	})
	return &profile, err
}
