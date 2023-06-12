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
		New(filepath, hash, contenttype string) *Model
		Get(ctx context.Context, filepath string) (*Model, error)
		Insert(ctx context.Context, m *Model) error
		Setup(ctx context.Context) error
	}

	repo struct {
		table *fileModelTable
		db    sqldb.Executor
	}

	// Model is a content tree model
	//forge:model file
	//forge:model:query file
	Model struct {
		Filepath    string `model:"filepath,VARCHAR(4095) PRIMARY KEY" query:"filepath;getoneeq,filepath"`
		Hash        string `model:"hash,VARCHAR(2047) NOT NULL;index" query:"hash"`
		ContentType string `model:"contenttype,VARCHAR(255) NOT NULL" query:"contenttype"`
	}
)

func New(database sqldb.Executor, table string) Repo {
	return &repo{
		table: &fileModelTable{
			TableName: table,
		},
		db: database,
	}
}

func (r *repo) New(filepath, hash, contenttype string) *Model {
	return &Model{
		Filepath:    filepath,
		Hash:        hash,
		ContentType: contenttype,
	}
}

func (r *repo) Get(ctx context.Context, filepath string) (*Model, error) {
	m, err := r.table.GetModelEqFilepath(ctx, r.db, filepath)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Failed to get content config")
	}
	return m, nil
}

func (r *repo) Insert(ctx context.Context, m *Model) error {
	if err := r.table.Insert(ctx, r.db, m); err != nil {
		return kerrors.WithMsg(err, "Failed to insert content config")
	}
	return nil
}

func (r *repo) Setup(ctx context.Context) error {
	if err := r.table.Setup(ctx, r.db); err != nil {
		return kerrors.WithMsg(err, "Failed to setup content config table")
	}
	return nil
}
