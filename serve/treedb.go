package serve

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"

	_ "modernc.org/sqlite"
	"xorkevin.dev/kerrors"
)

type (
	TreeDB interface {
		GetContent(ctx context.Context, fpath string) (*ContentConfig, error)
	}

	ContentConfig struct {
		Hash    string           `json:"hash"`
		Type    string           `json:"type"`
		Encoded []EncodedContent `json:"encoded"`
	}

	EncodedContent struct {
		Code string `json:"code"`
		Hash string `json:"hash"`
	}
)

type (
	FSTreeDB struct {
		fsys fs.FS
	}
)

func NewFSTreeDB(fsys fs.FS) *FSTreeDB {
	return &FSTreeDB{
		fsys: fsys,
	}
}

func (t *FSTreeDB) GetContent(ctx context.Context, fpath string) (*ContentConfig, error) {
	b, err := fs.ReadFile(t.fsys, fpath)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Failed to get file config")
	}
	cfg := &ContentConfig{}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, kerrors.WithMsg(err, "Failed to parse file config")
	}
	return cfg, nil
}

type (
	SQLiteTreeDB struct {
		db        *sql.DB
		tableName string
	}
)

func NewSQLiteTreeDB(dsn string, tableName string) (*SQLiteTreeDB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Failed creating sqlite db client")
	}
	return &SQLiteTreeDB{
		db:        db,
		tableName: tableName,
	}, nil
}

func (t *SQLiteTreeDB) GetContent(ctx context.Context, fpath string) (*ContentConfig, error) {
	// TODO sql query
	t.db.QueryContext(ctx, `SELECT hash, type FROM `+t.tableName+` WHERE fpath = $1`)
	return nil, nil
}
