package serve

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"xorkevin.dev/forge/model/sqldb"
	"xorkevin.dev/fsserve/db"
	"xorkevin.dev/fsserve/serve/treedbmodel"
	"xorkevin.dev/kerrors"
)

type (
	TreeDB interface {
		Get(ctx context.Context, name string) (*ContentConfig, error)
		Iterate(ctx context.Context, f TreeIterator) error
		Add(ctx context.Context, dst string, cfg ContentConfig) error
		Rm(ctx context.Context, dst string) error
		Setup(ctx context.Context) error
	}

	TreeIterator = func(ctx context.Context, name string, cfg ContentConfig) error

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

const (
	sqliteTreeConfigBatchSize = 32
)

func (t *SQLiteTreeDB) Iterate(ctx context.Context, f TreeIterator) error {
	cursor := ""
	for {
		m, err := t.repo.List(ctx, sqliteTreeConfigBatchSize, cursor)
		if err != nil {
			return kerrors.WithMsg(err, "Failed to list db content configs")
		}
		if len(m) == 0 {
			return nil
		}
		fhashes := make([]string, 0, len(m))
		for _, i := range m {
			fhashes = append(fhashes, i.Hash)
		}
		enc, err := t.repo.ListEncoded(ctx, fhashes)
		if err != nil {
			return kerrors.WithMsg(err, "Failed to list db encoded content configs")
		}
		sort.Slice(enc, func(i, j int) bool {
			if enc[i].FHash < enc[j].FHash {
				return true
			}
			if enc[i].FHash > enc[j].FHash {
				return false
			}
			return enc[i].Order < enc[j].Order
		})
		encMap := map[string][]EncodedContent{}
		for _, i := range enc {
			encMap[i.FHash] = append(encMap[i.FHash], EncodedContent{
				Code: i.Code,
				Hash: i.Hash,
			})
		}
		for _, i := range m {
			if err := f(ctx, i.Name, ContentConfig{
				Hash:        i.Hash,
				ContentType: i.ContentType,
				Encoded:     encMap[i.Hash],
			}); err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed executing iterator for %s", i.Name))
			}
		}
		if len(m) < sqliteTreeConfigBatchSize {
			return nil
		}
		cursor = m[len(m)-1].Name
	}
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

func (t *SQLiteTreeDB) Rm(ctx context.Context, dst string) error {
	if _, err := t.repo.Exists(ctx, dst); err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			return kerrors.WithMsg(err, "Failed checking dst file")
		}
		return nil
	}
	if err := t.repo.Delete(ctx, dst); err != nil {
		return kerrors.WithMsg(err, "Failed to delete content config")
	}
	return nil
}

func (t *SQLiteTreeDB) Setup(ctx context.Context) error {
	if err := t.repo.Setup(ctx); err != nil {
		return kerrors.WithMsg(err, "Failed to setup sqlite db")
	}
	return nil
}
