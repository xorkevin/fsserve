package serve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"golang.org/x/crypto/blake2b"
	"xorkevin.dev/kerrors"
	"xorkevin.dev/kfs"
	"xorkevin.dev/klog"
)

type (
	Tree struct {
		log        *klog.LevelLogger
		db         TreeDB
		contentDir fs.FS
	}

	EncodedFile struct {
		Code string
		Name string
	}
)

func NewTree(log klog.Logger, treedb TreeDB, contentDir fs.FS) *Tree {
	return &Tree{
		log:        klog.NewLevelLogger(log),
		db:         treedb,
		contentDir: contentDir,
	}
}

func (t *Tree) Add(ctx context.Context, dst string, ctype string, src string, encoded []EncodedFile) error {
	if dst == "" {
		return kerrors.WithMsg(nil, "Must provide dst")
	}
	cfg := ContentConfig{
		ContentType: ctype,
		Encoded:     make([]EncodedContent, 0, len(encoded)),
	}
	var err error
	cfg.Hash, err = t.checkAndAddFile(ctx, src)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to add file: %s", src))
	}
	for _, i := range encoded {
		if i.Code == "" {
			return kerrors.WithMsg(nil, "Must provide encoded file code")
		}
		dstName, err := t.checkAndAddFile(ctx, i.Name)
		if err != nil {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to add encoded file: %s", i.Name))
		}
		cfg.Encoded = append(cfg.Encoded, EncodedContent{
			Code: i.Code,
			Hash: dstName,
		})
	}

	if err := t.db.Add(ctx, dst, cfg); err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to add content config for %s", dst))
	}
	t.log.Info(ctx, "Added content config",
		klog.AString("dst", dst),
	)
	return nil
}

func (t *Tree) checkAndAddFile(ctx context.Context, srcName string) (string, error) {
	srcInfo, err := os.Stat(srcName)
	if err != nil {
		return "", kerrors.WithMsg(err, "Failed to stat src file")
	}
	if srcInfo.IsDir() {
		return "", kerrors.WithMsg(nil, fmt.Sprintf("Src file is dir"))
	}

	dstName, err := t.hashFile(srcName)
	if err != nil {
		return "", kerrors.WithMsg(err, "Failed to hash src file")
	}

	if dstInfo, err := fs.Stat(t.contentDir, dstName); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", kerrors.WithMsg(err, fmt.Sprintf("Failed to stat dst file: %s", dstName))
		}
	} else {
		if dstInfo.IsDir() {
			return "", kerrors.WithMsg(nil, fmt.Sprintf("Dst file %s is dir", dstName))
		}
		if dstInfo.Size() == srcInfo.Size() && dstInfo.ModTime().Equal(srcInfo.ModTime()) {
			t.log.Info(ctx, "Skipping present content file",
				klog.AString("src", srcName),
				klog.AString("dst", dstName),
			)
			return dstName, nil
		}
	}

	if err := t.copyFile(dstName, srcName); err != nil {
		return "", kerrors.WithMsg(err, fmt.Sprintf("Failed copying %s to %s", srcName, dstName))
	}
	t.log.Info(ctx, "Added content file",
		klog.AString("src", srcName),
		klog.AString("dst", dstName),
	)
	return dstName, nil
}

func (t *Tree) hashFile(name string) (_ string, retErr error) {
	f, err := os.Open(name)
	if err != nil {
		return "", kerrors.WithMsg(err, "Failed opening file")
	}
	defer func() {
		if err := f.Close(); err != nil {
			retErr = errors.Join(retErr, kerrors.WithMsg(err, "Failed to close file"))
		}
	}()
	h, err := blake2b.New512(nil)
	if err != nil {
		return "", kerrors.WithMsg(err, "Failed creating blake2b hash")
	}
	if _, err := io.Copy(h, f); err != nil {
		return "", kerrors.WithMsg(err, "Failed reading file")
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}

func (t *Tree) copyFile(dstName, srcName string) (retErr error) {
	dstFile, err := kfs.OpenFile(t.contentDir, dstName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return kerrors.WithMsg(err, "Failed opening dst file")
	}
	defer func() {
		if err := dstFile.Close(); err != nil {
			retErr = errors.Join(retErr, kerrors.WithMsg(err, "Failed to close dst file"))
		}
	}()
	srcFile, err := os.Open(srcName)
	if err != nil {
		return kerrors.WithMsg(err, "Failed opening src file")
	}
	defer func() {
		if err := srcFile.Close(); err != nil {
			retErr = errors.Join(retErr, kerrors.WithMsg(err, "Failed to close src file"))
		}
	}()
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return kerrors.WithMsg(err, "Failed copying file")
	}
	return nil
}

func (t *Tree) Rm(ctx context.Context, dst string) error {
	if dst == "" {
		return kerrors.WithMsg(nil, "Must provide dst")
	}
	cfg, err := t.db.Get(ctx, dst)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to get content config for %s", dst))
	}
	for _, i := range cfg.Encoded {
		if err := kfs.RemoveAll(t.contentDir, i.Hash); err != nil {
			return kerrors.WithMsg(err, "Failed to remove encoded file")
		}
		t.log.Info(ctx, "Removed encoded file",
			klog.AString("code", i.Code),
			klog.AString("name", i.Hash),
		)
	}
	if err := kfs.RemoveAll(t.contentDir, cfg.Hash); err != nil {
		return kerrors.WithMsg(err, "Failed to remove content file")
	}
	t.log.Info(ctx, "Removed content file",
		klog.AString("name", cfg.Hash),
	)
	if err := t.db.Rm(ctx, dst); err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to remove content config for %s", dst))
	}
	return nil
}

func (t *Tree) Setup(ctx context.Context) error {
	if err := t.db.Setup(ctx); err != nil {
		return kerrors.WithMsg(err, "Failed to init tree db")
	}
	return nil
}
