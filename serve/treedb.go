package serve

import (
	"context"
	"encoding/json"
	"io/fs"

	"xorkevin.dev/forge/model/sqldb"
	"xorkevin.dev/fsserve/serve/treedbmodel"
	"xorkevin.dev/kerrors"
)

type (
	TreeDB interface {
		GetContent(ctx context.Context, fpath string) (*ContentConfig, error)
	}

	ContentConfig struct {
		Hash        string           `json:"hash"`
		ContentType string           `json:"contenttype"`
		Encoded     []EncodedContent `json:"encoded"`
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
		repo treedbmodel.Repo
	}
)

func NewSQLiteTreeDB(d sqldb.Executor, tableName string) *SQLiteTreeDB {
	return &SQLiteTreeDB{
		repo: treedbmodel.New(d, tableName),
	}
}

func (t *SQLiteTreeDB) GetContent(ctx context.Context, fpath string) (*ContentConfig, error) {
	m, err := t.repo.Get(ctx, fpath)
	if err != nil {
		// TODO
	}
	return nil, nil
}
