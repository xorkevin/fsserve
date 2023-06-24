// Code generated by go generate forge model v0.4.4; DO NOT EDIT.

package treedbmodel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"xorkevin.dev/forge/model/sqldb"
)

type (
	ctModelTable struct {
		TableName string
	}
)

func (t *ctModelTable) Setup(ctx context.Context, d sqldb.Executor) error {
	_, err := d.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+t.TableName+" (name VARCHAR(4095) PRIMARY KEY, hash VARCHAR(2047) NOT NULL, contenttype VARCHAR(255) NOT NULL);")
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+t.TableName+"_hash_index ON "+t.TableName+" (hash);")
	if err != nil {
		return err
	}
	return nil
}

func (t *ctModelTable) Insert(ctx context.Context, d sqldb.Executor, m *Model) error {
	_, err := d.ExecContext(ctx, "INSERT INTO "+t.TableName+" (name, hash, contenttype) VALUES ($1, $2, $3);", m.Name, m.Hash, m.ContentType)
	if err != nil {
		return err
	}
	return nil
}

func (t *ctModelTable) InsertBulk(ctx context.Context, d sqldb.Executor, models []*Model, allowConflict bool) error {
	conflictSQL := ""
	if allowConflict {
		conflictSQL = " ON CONFLICT DO NOTHING"
	}
	placeholders := make([]string, 0, len(models))
	args := make([]interface{}, 0, len(models)*3)
	for c, m := range models {
		n := c * 3
		placeholders = append(placeholders, fmt.Sprintf("($%d, $%d, $%d)", n+1, n+2, n+3))
		args = append(args, m.Name, m.Hash, m.ContentType)
	}
	_, err := d.ExecContext(ctx, "INSERT INTO "+t.TableName+" (name, hash, contenttype) VALUES "+strings.Join(placeholders, ", ")+conflictSQL+";", args...)
	if err != nil {
		return err
	}
	return nil
}

func (t *ctModelTable) GetModelEqName(ctx context.Context, d sqldb.Executor, name string) (*Model, error) {
	m := &Model{}
	if err := d.QueryRowContext(ctx, "SELECT name, hash, contenttype FROM "+t.TableName+" WHERE name = $1;", name).Scan(&m.Name, &m.Hash, &m.ContentType); err != nil {
		return nil, err
	}
	return m, nil
}

func (t *ctModelTable) DelEqName(ctx context.Context, d sqldb.Executor, name string) error {
	_, err := d.ExecContext(ctx, "DELETE FROM "+t.TableName+" WHERE name = $1;", name)
	return err
}

func (t *ctModelTable) GetModelOrdName(ctx context.Context, d sqldb.Executor, orderasc bool, limit, offset int) (_ []Model, retErr error) {
	order := "DESC"
	if orderasc {
		order = "ASC"
	}
	res := make([]Model, 0, limit)
	rows, err := d.QueryContext(ctx, "SELECT name, hash, contenttype FROM "+t.TableName+" ORDER BY name "+order+" LIMIT $1 OFFSET $2;", limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("Failed to close db rows: %w", err))
		}
	}()
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.Name, &m.Hash, &m.ContentType); err != nil {
			return nil, err
		}
		res = append(res, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (t *ctModelTable) GetModelGtNameOrdName(ctx context.Context, d sqldb.Executor, name string, orderasc bool, limit, offset int) (_ []Model, retErr error) {
	order := "DESC"
	if orderasc {
		order = "ASC"
	}
	res := make([]Model, 0, limit)
	rows, err := d.QueryContext(ctx, "SELECT name, hash, contenttype FROM "+t.TableName+" WHERE name > $3 ORDER BY name "+order+" LIMIT $1 OFFSET $2;", limit, offset, name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("Failed to close db rows: %w", err))
		}
	}()
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.Name, &m.Hash, &m.ContentType); err != nil {
			return nil, err
		}
		res = append(res, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (t *ctModelTable) UpdctPropsEqName(ctx context.Context, d sqldb.Executor, m *ctProps, name string) error {
	_, err := d.ExecContext(ctx, "UPDATE "+t.TableName+" SET (hash, contenttype) = ($1, $2) WHERE name = $3;", m.Hash, m.ContentType, name)
	if err != nil {
		return err
	}
	return nil
}

type (
	encModelTable struct {
		TableName string
	}
)

func (t *encModelTable) Setup(ctx context.Context, d sqldb.Executor) error {
	_, err := d.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+t.TableName+" (fhash VARCHAR(2047), code VARCHAR(255), ord INT NOT NULL, hash VARCHAR(2047) NOT NULL, PRIMARY KEY (fhash, code), UNIQUE (fhash, ord));")
	if err != nil {
		return err
	}
	return nil
}

func (t *encModelTable) Insert(ctx context.Context, d sqldb.Executor, m *Encoded) error {
	_, err := d.ExecContext(ctx, "INSERT INTO "+t.TableName+" (fhash, code, ord, hash) VALUES ($1, $2, $3, $4);", m.FHash, m.Code, m.Order, m.Hash)
	if err != nil {
		return err
	}
	return nil
}

func (t *encModelTable) InsertBulk(ctx context.Context, d sqldb.Executor, models []*Encoded, allowConflict bool) error {
	conflictSQL := ""
	if allowConflict {
		conflictSQL = " ON CONFLICT DO NOTHING"
	}
	placeholders := make([]string, 0, len(models))
	args := make([]interface{}, 0, len(models)*4)
	for c, m := range models {
		n := c * 4
		placeholders = append(placeholders, fmt.Sprintf("($%d, $%d, $%d, $%d)", n+1, n+2, n+3, n+4))
		args = append(args, m.FHash, m.Code, m.Order, m.Hash)
	}
	_, err := d.ExecContext(ctx, "INSERT INTO "+t.TableName+" (fhash, code, ord, hash) VALUES "+strings.Join(placeholders, ", ")+conflictSQL+";", args...)
	if err != nil {
		return err
	}
	return nil
}

func (t *encModelTable) DelEqFHash(ctx context.Context, d sqldb.Executor, fhash string) error {
	_, err := d.ExecContext(ctx, "DELETE FROM "+t.TableName+" WHERE fhash = $1;", fhash)
	return err
}

func (t *encModelTable) GetEncodedHasFHashOrdFHash(ctx context.Context, d sqldb.Executor, fhashs []string, orderasc bool, limit, offset int) (_ []Encoded, retErr error) {
	paramCount := 2
	args := make([]interface{}, 0, paramCount+len(fhashs))
	args = append(args, limit, offset)
	var placeholdersfhashs string
	{
		placeholders := make([]string, 0, len(fhashs))
		for _, i := range fhashs {
			paramCount++
			placeholders = append(placeholders, fmt.Sprintf("($%d)", paramCount))
			args = append(args, i)
		}
		placeholdersfhashs = strings.Join(placeholders, ", ")
	}
	order := "DESC"
	if orderasc {
		order = "ASC"
	}
	res := make([]Encoded, 0, limit)
	rows, err := d.QueryContext(ctx, "SELECT fhash, code, ord, hash FROM "+t.TableName+" WHERE fhash IN (VALUES "+placeholdersfhashs+") ORDER BY fhash "+order+" LIMIT $1 OFFSET $2;", args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("Failed to close db rows: %w", err))
		}
	}()
	for rows.Next() {
		var m Encoded
		if err := rows.Scan(&m.FHash, &m.Code, &m.Order, &m.Hash); err != nil {
			return nil, err
		}
		res = append(res, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (t *encModelTable) GetEncodedEqFHashOrdOrder(ctx context.Context, d sqldb.Executor, fhash string, orderasc bool, limit, offset int) (_ []Encoded, retErr error) {
	order := "DESC"
	if orderasc {
		order = "ASC"
	}
	res := make([]Encoded, 0, limit)
	rows, err := d.QueryContext(ctx, "SELECT fhash, code, ord, hash FROM "+t.TableName+" WHERE fhash = $3 ORDER BY ord "+order+" LIMIT $1 OFFSET $2;", limit, offset, fhash)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("Failed to close db rows: %w", err))
		}
	}()
	for rows.Next() {
		var m Encoded
		if err := rows.Scan(&m.FHash, &m.Code, &m.Order, &m.Hash); err != nil {
			return nil, err
		}
		res = append(res, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}
