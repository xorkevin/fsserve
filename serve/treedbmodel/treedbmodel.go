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
		Exists(ctx context.Context, name string) (bool, error)
		ContentExists(ctx context.Context, hash string) (bool, error)
		List(ctx context.Context, limit int, after string) ([]Model, error)
		Get(ctx context.Context, name string) (*Model, []Encoded, error)
		Insert(ctx context.Context, m *Model, enc []*Encoded) error
		Update(ctx context.Context, m *Model, enc []*Encoded) error
		Delete(ctx context.Context, name string) error
		ListGCCandidates(ctx context.Context, limit int) ([]GCCandidate, error)
		DequeueGCCandidate(ctx context.Context, hash string) error
		Setup(ctx context.Context) error
	}

	repo struct {
		db       sqldb.Executor
		ctTable  *ctModelTable
		encTable *encModelTable
		gcTable  *gcModelTable
	}

	// Model is a content tree model
	//forge:model ct
	//forge:model:query ct
	Model struct {
		Name        string `model:"name,VARCHAR(4095) PRIMARY KEY"`
		Hash        string `model:"hash,VARCHAR(2047) NOT NULL"`
		ContentType string `model:"contenttype,VARCHAR(255) NOT NULL"`
	}

	//forge:model:query ct
	ctProps struct {
		Hash        string `model:"hash"`
		ContentType string `model:"contenttype"`
	}

	// Encoded is encoded content
	//forge:model enc
	//forge:model:query enc
	Encoded struct {
		Name  string `model:"name,VARCHAR(4095)"`
		Code  string `model:"code,VARCHAR(255)"`
		Order int    `model:"ord,INT NOT NULL"`
		Hash  string `model:"hash,VARCHAR(2047) NOT NULL"`
	}

	// GCCandidate are candidates for GC
	//forge:model gc
	//forge:model:query gc
	GCCandidate struct {
		Hash string `model:"hash,VARCHAR(2047) PRIMARY KEY"`
	}
)

func New(database sqldb.Executor, contentTable, encTable, gcTable string) Repo {
	return &repo{
		db: database,
		ctTable: &ctModelTable{
			TableName: contentTable,
		},
		encTable: &encModelTable{
			TableName: encTable,
		},
		gcTable: &gcModelTable{
			TableName: gcTable,
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

func (r *repo) nameExists(ctx context.Context, d sqldb.Executor, name string) (bool, error) {
	var exists bool
	if err := d.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+r.ctTable.TableName+" WHERE name = $1);", name).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (r *repo) Exists(ctx context.Context, name string) (bool, error) {
	m, err := r.nameExists(ctx, r.db, name)
	if err != nil {
		return false, kerrors.WithMsg(err, "Failed to check content config")
	}
	return m, nil
}

func (r *repo) contentExists(ctx context.Context, d sqldb.Executor, hash string) (bool, error) {
	var exists bool
	if err := d.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+r.ctTable.TableName+" WHERE hash = $1);", hash).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return true, nil
	}
	if err := d.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+r.encTable.TableName+" WHERE hash = $1);", hash).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (r *repo) ContentExists(ctx context.Context, hash string) (bool, error) {
	m, err := r.contentExists(ctx, r.db, hash)
	if err != nil {
		return false, kerrors.WithMsg(err, "Failed to check content")
	}
	return m, nil
}

func (r *repo) List(ctx context.Context, limit int, after string) ([]Model, error) {
	if after == "" {
		m, err := r.ctTable.GetModelAll(ctx, r.db, limit, 0)
		if err != nil {
			return nil, kerrors.WithMsg(err, "Failed to get content configs")
		}
		return m, nil
	}
	m, err := r.ctTable.GetModelGtName(ctx, r.db, after, limit, 0)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Failed to get content configs")
	}
	return m, nil
}

func (r *repo) Get(ctx context.Context, name string) (*Model, []Encoded, error) {
	m, err := r.ctTable.GetModelByName(ctx, r.db, name)
	if err != nil {
		return nil, nil, kerrors.WithMsg(err, "Failed to get content config")
	}
	enc, err := r.encTable.GetEncodedByName(ctx, r.db, m.Name, 128, 0)
	if err != nil {
		return nil, nil, kerrors.WithMsg(err, "Failed to get encoded content configs")
	}
	return m, enc, nil
}

func (r *repo) queueGCContent(ctx context.Context, d sqldb.Executor, name string) error {
	_, err := d.ExecContext(ctx, "INSERT INTO "+r.gcTable.TableName+" (hash) SELECT hash FROM "+r.encTable.TableName+" WHERE name = $1 ON CONFLICT DO NOTHING;", name)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, "INSERT INTO "+r.gcTable.TableName+" (hash) SELECT hash FROM "+r.ctTable.TableName+" WHERE name = $1 ON CONFLICT DO NOTHING;", name)
	if err != nil {
		return err
	}
	return nil
}

func (r *repo) queueGC(ctx context.Context, name string) error {
	if err := r.queueGCContent(ctx, r.db, name); err != nil {
		return kerrors.WithMsg(err, "Failed to queue gc candidates")
	}
	return nil
}

func (r *repo) delEncoded(ctx context.Context, name string) error {
	if err := r.encTable.DelByName(ctx, r.db, name); err != nil {
		return kerrors.WithMsg(err, "Failed to delete encoded content configs")
	}
	return nil
}

func (r *repo) addEncoded(ctx context.Context, m *Model, enc []*Encoded) error {
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
	if err := r.queueGC(ctx, m.Name); err != nil {
		return err
	}
	if err := r.delEncoded(ctx, m.Name); err != nil {
		return err
	}
	if err := r.ctTable.Insert(ctx, r.db, m); err != nil {
		return kerrors.WithMsg(err, "Failed to insert content config")
	}
	if err := r.addEncoded(ctx, m, enc); err != nil {
		return err
	}
	return nil
}

func (r *repo) Update(ctx context.Context, m *Model, enc []*Encoded) error {
	if err := r.queueGC(ctx, m.Name); err != nil {
		return err
	}
	if err := r.delEncoded(ctx, m.Name); err != nil {
		return err
	}
	if err := r.ctTable.UpdctPropsByName(ctx, r.db, &ctProps{
		Hash:        m.Hash,
		ContentType: m.ContentType,
	}, m.Name); err != nil {
		return kerrors.WithMsg(err, "Failed to update content config")
	}
	if err := r.addEncoded(ctx, m, enc); err != nil {
		return nil
	}
	return nil
}

func (r *repo) Delete(ctx context.Context, name string) error {
	if err := r.queueGC(ctx, name); err != nil {
		return err
	}
	if err := r.encTable.DelByName(ctx, r.db, name); err != nil {
		return kerrors.WithMsg(err, "Failed to delete encoded content configs")
	}
	if err := r.ctTable.DelByName(ctx, r.db, name); err != nil {
		return kerrors.WithMsg(err, "Failed to delete content config")
	}
	return nil
}

func (r *repo) ListGCCandidates(ctx context.Context, limit int) ([]GCCandidate, error) {
	m, err := r.gcTable.GetGCCandidateAll(ctx, r.db, limit, 0)
	if err != nil {
		return nil, kerrors.WithMsg(err, "Failed getting gc candidates")
	}
	return m, nil
}

func (r *repo) DequeueGCCandidate(ctx context.Context, hash string) error {
	if err := r.gcTable.DelByHash(ctx, r.db, hash); err != nil {
		return kerrors.WithMsg(err, "Failed dequeueing gc candidate")
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
	if err := r.gcTable.Setup(ctx, r.db); err != nil {
		return kerrors.WithMsg(err, "Failed to setup gc candidate table")
	}
	return nil
}
