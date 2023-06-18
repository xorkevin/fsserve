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
	"xorkevin.dev/kfs"
)

type (
	TreeDB interface {
		Get(ctx context.Context, name string) (*ContentConfig, error)
		Add(ctx context.Context, dst string, cfg ContentConfig) error
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

func (t *FSTreeDB) Get(ctx context.Context, name string) (*ContentConfig, error) {
	b, err := fs.ReadFile(t.fsys, name)
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

func (t *FSTreeDB) Add(ctx context.Context, dst string, cfg ContentConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return kerrors.WithMsg(err, "Failed marshalling content config to json")
	}
	if err := kfs.WriteFile(t.fsys, dst, b, 0o644); err != nil {
		return kerrors.WithMsg(err, "Failed writing content config")
	}
	return nil
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

func (t *SQLiteTreeDB) Get(ctx context.Context, name string) (*ContentConfig, error) {
	m, enc, err := t.repo.Get(ctx, name)
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

func (t *SQLiteTreeDB) Add(ctx context.Context, dst string, cfg ContentConfig) error {
	m := treedbmodel.Model{
		Name:        dst,
		Hash:        cfg.Hash,
		ContentType: cfg.ContentType,
	}
	enc := make([]*treedbmodel.Encoded, 0, len(cfg.Encoded))
	for n, i := range cfg.Encoded {
		enc = append(enc, &treedbmodel.Encoded{
			FHash: m.Hash,
			Code:  i.Code,
			Order: n + 1,
			Hash:  i.Hash,
		})
	}

	if _, err := t.repo.Exists(ctx, dst); err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			return kerrors.WithMsg(err, "Failed checking dst file")
		}
		if err := t.repo.Insert(ctx, &m, enc); err != nil {
			return kerrors.WithMsg(err, "Failed to insert content config")
		}
	} else {
		if err := t.repo.Update(ctx, &m, enc); err != nil {
			return kerrors.WithMsg(err, "Failed to update content config")
		}
	}

	return nil
}
