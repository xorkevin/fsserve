package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"

	"xorkevin.dev/forge/model/sqldb"
	"xorkevin.dev/fsserve/db"
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

// ErrNotFound is returned when a file is not found
var ErrNotFound errNotFound

type (
	errNotFound struct{}
)

func (e errNotFound) Error() string {
	return "File not found"
}

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
		if errors.Is(err, fs.ErrNotExist) {
			return nil, kerrors.WithKind(err, ErrNotFound, "Content config not found")
		}
		return nil, kerrors.WithMsg(err, "Failed to get content config")
	}
	cfg := &ContentConfig{}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, kerrors.WithMsg(err, "Failed to parse content config")
	}
	return cfg, nil
}

type (
	SQLiteTreeDB struct {
		repo treedbmodel.Repo
	}
)

func NewSQLiteTreeDB(d sqldb.Executor, contentTable, encTable string) *SQLiteTreeDB {
	return &SQLiteTreeDB{
		repo: treedbmodel.New(d, contentTable, encTable),
	}
}

func (t *SQLiteTreeDB) GetContent(ctx context.Context, fpath string) (*ContentConfig, error) {
	m, enc, err := t.repo.Get(ctx, fpath)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, kerrors.WithKind(err, ErrNotFound, "Content config not found")
		}
		return nil, kerrors.WithMsg(err, "Failed to get content config")
	}
	res := make([]EncodedContent, 0, len(enc))
	for _, i := range enc {
		res = append(res, EncodedContent{
			Code: i.Code,
			Hash: i.Hash,
		})
	}
	return &ContentConfig{
		Hash:        m.Hash,
		ContentType: m.ContentType,
		Encoded:     res,
	}, nil
}
