package serve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"

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

	EncodedAlts struct {
		Code   string `mapstructure:"code"`
		Suffix string `mapstructure:"suffix"`
		Name   string `mapstructure:"name"`
	}

	SyncDirConfig struct {
		Dst         string        `mapstructure:"dst"`
		ContentType string        `mapstructure:"contenttype"`
		Exact       bool          `mapstructure:"exact"`
		Src         string        `mapstructure:"src"`
		Match       string        `mapstructure:"match"`
		Alts        []EncodedAlts `mapstructure:"alts"`
	}

	SyncConfig struct {
		Dirs []SyncDirConfig `mapstructure:"dirs"`
	}
)

func NewTree(log klog.Logger, treedb TreeDB, contentDir fs.FS) *Tree {
	return &Tree{
		log:        klog.NewLevelLogger(log),
		db:         treedb,
		contentDir: contentDir,
	}
}

func (t *Tree) SyncContent(ctx context.Context, cfg SyncConfig) error {
	for _, i := range cfg.Dirs {
		dst := path.Clean(i.Dst)
		if i.Exact {
			enc := make([]EncodedFile, 0, len(i.Alts))
			for _, i := range i.Alts {
				enc = append(enc, EncodedFile{
					Code: i.Code,
					Name: i.Name,
				})
			}
			if err := t.Add(ctx, dst, i.ContentType, i.Src, enc); err != nil {
				return err
			}
		} else {
			r, err := regexp.Compile(i.Match)
			if err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Invalid src match regex for dir %s", i.Src))
			}
			dir := os.DirFS(filepath.FromSlash(i.Src))
			info, err := fs.Stat(dir, ".")
			if err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to read root for dir %s", i.Src))
			}
			if err := t.syncContentDir(ctx, dst, i.ContentType, dir, r, i.Alts, ".", fs.FileInfoToDirEntry(info)); err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to sync dir %s to %s", i.Src, dst))
			}
		}
	}
	return nil
}

func (t *Tree) syncContentDir(ctx context.Context, dstPrefix string, ctype string, dir fs.FS, r *regexp.Regexp, alts []EncodedAlts, p string, entry fs.DirEntry) error {
	if !entry.IsDir() {
		if !r.MatchString(p) {
			t.log.Debug(ctx, "Skipping unmatched file",
				klog.AString("dst", dstPrefix),
				klog.AString("src", p),
			)
			return nil
		}
		dst := path.Join(dstPrefix, p)
		cfg := ContentConfig{
			ContentType: ctype,
			Encoded:     make([]EncodedContent, 0, len(alts)),
		}
		var err error
		cfg.Hash, err = t.checkAndAddFileFS(ctx, dir, p)
		if err != nil {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to add file: %s", p))
		}
		for _, i := range alts {
			alt := p + i.Suffix
			h, err := t.checkAndAddFileFS(ctx, dir, alt)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					t.log.Debug(ctx, "Skipping missing alt file",
						klog.AString("dst", dstPrefix),
						klog.AString("src", alt),
					)
					continue
				}
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to add alt file: %s", alt))
			}
			cfg.Encoded = append(cfg.Encoded, EncodedContent{
				Code: i.Code,
				Hash: h,
			})
		}
		if err := t.db.Add(ctx, dst, cfg); err != nil {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to add content config for %s", p))
		}
		t.log.Info(ctx, "Added content config",
			klog.AString("dst", dst),
		)
		return nil
	}
	entries, err := fs.ReadDir(dir, p)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed reading dir: %s", p))
	}
	t.log.Debug(ctx, "Exploring dir",
		klog.AString("dst", dstPrefix),
		klog.AString("src", p),
	)
	for _, i := range entries {
		if err := t.syncContentDir(ctx, dstPrefix, ctype, dir, r, alts, path.Join(p, i.Name()), i); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tree) Add(ctx context.Context, dst string, ctype string, src string, encoded []EncodedFile) error {
	if dst == "" {
		return kerrors.WithMsg(nil, "Must provide dst")
	}
	dst = path.Clean(dst)
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
	dir, file := path.Split(srcName)
	dir = path.Clean(dir)
	file = path.Clean(file)
	fsys := os.DirFS(filepath.FromSlash(dir))
	return t.checkAndAddFileFS(ctx, fsys, file)
}

func (t *Tree) checkAndAddFileFS(ctx context.Context, dir fs.FS, srcName string) (string, error) {
	srcInfo, err := fs.Stat(dir, srcName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", kerrors.WithKind(err, ErrNotFound, "Src file does not exist")
		}
		return "", kerrors.WithMsg(err, "Failed to stat src file")
	}
	if srcInfo.IsDir() {
		return "", kerrors.WithMsg(nil, fmt.Sprintf("Src file is dir"))
	}

	dstName, err := t.hashFile(dir, srcName)
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
		if dstInfo.Size() == srcInfo.Size() && !dstInfo.ModTime().Before(srcInfo.ModTime()) {
			t.log.Info(ctx, "Skipping present content file",
				klog.AString("src", srcName),
				klog.AString("dst", dstName),
			)
			return dstName, nil
		}
	}

	if err := t.copyFile(dir, dstName, srcName); err != nil {
		return "", kerrors.WithMsg(err, fmt.Sprintf("Failed copying %s to %s", srcName, dstName))
	}
	t.log.Info(ctx, "Added content file",
		klog.AString("src", srcName),
		klog.AString("dst", dstName),
	)
	return dstName, nil
}

func (t *Tree) hashFile(dir fs.FS, name string) (_ string, retErr error) {
	f, err := dir.Open(name)
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

func (t *Tree) copyFile(dir fs.FS, dstName, srcName string) (retErr error) {
	dstFile, err := kfs.OpenFile(t.contentDir, dstName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return kerrors.WithMsg(err, "Failed opening dst file")
	}
	defer func() {
		if err := dstFile.Close(); err != nil {
			retErr = errors.Join(retErr, kerrors.WithMsg(err, "Failed to close dst file"))
		}
	}()
	srcFile, err := dir.Open(srcName)
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
	dst = path.Clean(dst)
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
