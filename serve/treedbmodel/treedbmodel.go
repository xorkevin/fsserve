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
		New(fpath, hash, contenttype string) *Model
		Get(ctx context.Context, fpath string) (*Model, []Encoded, error)
		Insert(ctx context.Context, m *Model, enc []*Encoded) error
		Update(ctx context.Context, m *Model, enc []*Encoded) error
		Delete(ctx context.Context, fpath string) error
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
		FPath       string `model:"fpath,VARCHAR(4095) PRIMARY KEY" query:"fpath;getoneeq,fpath;deleq,fpath"`
		Hash        string `model:"hash,VARCHAR(2047) NOT NULL;index" query:"hash"`
		ContentType string `model:"contenttype,VARCHAR(255) NOT NULL" query:"contenttype"`
	}

	//forge:model:query ct
	ctProps struct {
		Hash        string `query:"hash;updeq,fpath"`
		ContentType string `query:"contenttype"`
	}

	// Encoded is encoded content
	//forge:model enc
	//forge:model:query enc
	Encoded struct {
		FHash string `model:"fhash,VARCHAR(2047)" query:"fhash;deleq,fhash"`
		Code  string `model:"code,VARCHAR(255), PRIMARY KEY (fhash, code)" query:"code"`
		Order int    `model:"order,INT NOT NULL, UNIQUE (fhash, order)" query:"order;getgroupeq,fhash"`
		Hash  string `model:"hash,VARCHAR(2047) NOT NULL" query:"hash"`
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

func (r *repo) New(fpath, hash, contenttype string) *Model {
	return &Model{
		FPath:       fpath,
		Hash:        hash,
		ContentType: contenttype,
	}
}

func (r *repo) Get(ctx context.Context, fpath string) (*Model, []Encoded, error) {
	m, err := r.ctTable.GetModelEqFPath(ctx, r.db, fpath)
	if err != nil {
		return nil, nil, kerrors.WithMsg(err, "Failed to get content config")
	}
	enc, err := r.encTable.GetEncodedEqFHashOrdOrder(ctx, r.db, m.Hash, true, 128, 0)
	if err != nil {
		return nil, nil, kerrors.WithMsg(err, "Failed to get encoded content configs")
	}
	return m, enc, nil
}

func (r *repo) addEncoded(ctx context.Context, m *Model, enc []*Encoded) error {
	if err := r.encTable.DelEqFHash(ctx, r.db, m.Hash); err != nil {
		return kerrors.WithMsg(err, "Failed to delete encoded content configs")
	}
	if len(enc) == 0 {
		return nil
	}
	for n, i := range enc {
		i.FHash = m.Hash
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
	if err := r.ctTable.UpdctPropsEqFPath(ctx, r.db, &ctProps{
		Hash:        m.Hash,
		ContentType: m.ContentType,
	}, m.FPath); err != nil {
		return kerrors.WithMsg(err, "Failed to update content config")
	}
	return nil
}

func (r *repo) Delete(ctx context.Context, fpath string) error {
	m, err := r.ctTable.GetModelEqFPath(ctx, r.db, fpath)
	if err != nil {
		return kerrors.WithMsg(err, "Failed to get content config")
	}
	if err := r.encTable.DelEqFHash(ctx, r.db, m.Hash); err != nil {
		return kerrors.WithMsg(err, "Failed to delete encoded content configs")
	}
	if err := r.ctTable.DelEqFPath(ctx, r.db, fpath); err != nil {
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
