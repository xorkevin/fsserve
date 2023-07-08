package treedbmodel

import (
	"context"

	"xorkevin.dev/forge/model/sqldb"
	"xorkevin.dev/kerrors"
)

//go:generate forge model

type (
	// Repo is a content tree repository
	Repo interface {
		New(name, hash, contenttype string) *Model
		Exists(ctx context.Context, name string) (*Model, error)
		List(ctx context.Context, limit int, after string) ([]Model, error)
		ListEncoded(ctx context.Context, names []string) ([]Encoded, error)
		Get(ctx context.Context, name string) (*Model, []Encoded, error)
		Insert(ctx context.Context, m *Model, enc []*Encoded) error
		Update(ctx context.Context, m *Model, enc []*Encoded) error
		Delete(ctx context.Context, name string) error
		Setup(ctx context.Context) error
	}

	repo struct {
		db       sqldb.Executor
		ctTable  *ctModelTable
		encTable *encModelTable
	}

	// Model is a content tree model
	//forge:model ct
	//forge:model:query ct
	Model struct {
		Name        string `model:"name,VARCHAR(4095) PRIMARY KEY" query:"name;getoneeq,name;deleq,name;getgroup;getgroupeq,name|gt"`
		Hash        string `model:"hash,VARCHAR(2047) NOT NULL;index" query:"hash"`
		ContentType string `model:"contenttype,VARCHAR(255) NOT NULL" query:"contenttype"`
	}

	//forge:model:query ct
	ctProps struct {
		Hash        string `query:"hash;updeq,name"`
		ContentType string `query:"contenttype"`
	}

	// Encoded is encoded content
	//forge:model enc
	//forge:model:query enc
	Encoded struct {
		Name  string `model:"name,VARCHAR(4095)" query:"name;deleq,name;getgroupeq,name|in"`
		Code  string `model:"code,VARCHAR(255)" query:"code"`
		Order int    `model:"ord,INT NOT NULL" query:"ord;getgroupeq,name"`
		Hash  string `model:"hash,VARCHAR(2047) NOT NULL, PRIMARY KEY (name, code), UNIQUE (name, ord)" query:"hash"`
	}
)

func New(database sqldb.Executor, contentTable, encTable string) Repo {
	return &repo{
		db: database,
		ctTable: &ctModelTable{
			TableName: contentTable,
		},
		encTable: &encModelTable{
			TableName: encTable,
		},
	}
}

func (r *repo) New(name, hash, contenttype string) *Model {
	return &Model{
		Name:        name,
		Hash:        hash,
		ContentType: contenttype,
	}
}

func (r *repo) Exists(ctx context.Context, name string) (*Model, error) {
	m, err := r.ctTable.GetModelEqName(ctx, r.db, name)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Failed to get content config")
	}
	return m, nil
}

func (r *repo) List(ctx context.Context, limit int, after string) ([]Model, error) {
	if after == "" {
		m, err := r.ctTable.GetModelOrdName(ctx, r.db, true, limit, 0)
		if err != nil {
			return nil, kerrors.WithMsg(err, "Failed to get content configs")
		}
		return m, nil
	}
	m, err := r.ctTable.GetModelGtNameOrdName(ctx, r.db, after, true, limit, 0)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Failed to get content configs")
	}
	return m, nil
}

func (r *repo) ListEncoded(ctx context.Context, names []string) ([]Encoded, error) {
	if len(names) == 0 {
		return nil, nil
	}

	m, err := r.encTable.GetEncodedHasNameOrdName(ctx, r.db, names, true, 128*len(names), 0)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Failed to get encoded content configs")
	}
	return m, nil
}

func (r *repo) Get(ctx context.Context, name string) (*Model, []Encoded, error) {
	m, err := r.ctTable.GetModelEqName(ctx, r.db, name)
	if err != nil {
		return nil, nil, kerrors.WithMsg(err, "Failed to get content config")
	}
	enc, err := r.encTable.GetEncodedEqNameOrdOrder(ctx, r.db, m.Name, true, 128, 0)
	if err != nil {
		return nil, nil, kerrors.WithMsg(err, "Failed to get encoded content configs")
	}
	return m, enc, nil
}

func (r *repo) addEncoded(ctx context.Context, m *Model, enc []*Encoded) error {
	if err := r.encTable.DelEqName(ctx, r.db, m.Name); err != nil {
		return kerrors.WithMsg(err, "Failed to delete encoded content configs")
	}
	if len(enc) == 0 {
		return nil
	}
	for n, i := range enc {
		i.Name = m.Name
		i.Order = n + 1
	}
	if err := r.encTable.InsertBulk(ctx, r.db, enc, true); err != nil {
		return kerrors.WithMsg(err, "Failed to insert encoded content configs")
	}
	return nil
}

func (r *repo) Insert(ctx context.Context, m *Model, enc []*Encoded) error {
	if err := r.addEncoded(ctx, m, enc); err != nil {
		return nil
	}
	if err := r.ctTable.Insert(ctx, r.db, m); err != nil {
		return kerrors.WithMsg(err, "Failed to insert content config")
	}
	return nil
}

func (r *repo) Update(ctx context.Context, m *Model, enc []*Encoded) error {
	if err := r.addEncoded(ctx, m, enc); err != nil {
		return nil
	}
	if err := r.ctTable.UpdctPropsEqName(ctx, r.db, &ctProps{
		Hash:        m.Hash,
		ContentType: m.ContentType,
	}, m.Name); err != nil {
		return kerrors.WithMsg(err, "Failed to update content config")
	}
	return nil
}

func (r *repo) Delete(ctx context.Context, name string) error {
	m, err := r.ctTable.GetModelEqName(ctx, r.db, name)
	if err != nil {
		return kerrors.WithMsg(err, "Failed to get content config")
	}
	if err := r.encTable.DelEqName(ctx, r.db, m.Name); err != nil {
		return kerrors.WithMsg(err, "Failed to delete encoded content configs")
	}
	if err := r.ctTable.DelEqName(ctx, r.db, name); err != nil {
		return kerrors.WithMsg(err, "Failed to delete content config")
	}
	return nil
}

func (r *repo) Setup(ctx context.Context) error {
	if err := r.ctTable.Setup(ctx, r.db); err != nil {
		return kerrors.WithMsg(err, "Failed to setup content config table")
	}
	if err := r.encTable.Setup(ctx, r.db); err != nil {
		return kerrors.WithMsg(err, "Failed to setup encoded content config table")
	}
	return nil
}
