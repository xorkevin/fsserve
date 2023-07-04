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
	"time"

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

func (t *Tree) SyncContent(ctx context.Context, cfg SyncConfig, rmAfter bool) error {
	for _, i := range cfg.Dirs {
		for _, j := range i.Alts {
			if j.Code == "" {
				return kerrors.WithMsg(nil, "Must provide encoded file code")
			}
		}
	}

	dstSet := map[string]struct{}{}
	for _, i := range cfg.Dirs {
		dst := path.Clean(i.Dst)
		if i.Exact {
			enc := make([]EncodedFile, 0, len(i.Alts))
			for _, j := range i.Alts {
				enc = append(enc, EncodedFile{
					Code: j.Code,
					Name: j.Name,
				})
			}
			if err := t.Add(ctx, dst, i.ContentType, i.Src, enc); err != nil {
				return err
			}
			dstSet[dst] = struct{}{}
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
			if err := t.syncContentDir(ctx, dstSet, dst, i.ContentType, dir, r, i.Alts, ".", fs.FileInfoToDirEntry(info)); err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to sync dir %s to %s", i.Src, dst))
			}
		}
	}

	if rmAfter {
		contentSet := map[string]struct{}{}
		if err := t.db.Iterate(ctx, func(ctx context.Context, name string, cfg ContentConfig) error {
			if _, ok := dstSet[name]; ok {
				contentSet[cfg.Hash] = struct{}{}
				for _, i := range cfg.Encoded {
					contentSet[i.Hash] = struct{}{}
				}
				return nil
			}
			return t.rmContent(ctx, name, cfg)
		}); err != nil {
			return kerrors.WithMsg(err, "Failed iterating through tree db")
		}

		if err := t.rmSetDifference(contentSet); err != nil {
			return err
		}
	}

	return nil
}

func (t *Tree) syncContentDir(ctx context.Context, dstSet map[string]struct{}, dstPrefix string, ctype string, dir fs.FS, r *regexp.Regexp, alts []EncodedAlts, p string, entry fs.DirEntry) error {
	if !entry.IsDir() {
		if !r.MatchString(p) {
			t.log.Debug(ctx, "Skipping unmatched file",
				klog.AString("dst", dstPrefix),
				klog.AString("src", p),
			)
			return nil
		}
		dst := path.Join(dstPrefix, p)

		var existingHash string
		codeToHash := map[string]string{}
		existingCfg, err := t.db.Get(ctx, dst)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to check existing content config for %s", dst))
			}
			existingCfg = nil
		} else {
			existingHash = existingCfg.Hash
			for _, i := range existingCfg.Encoded {
				codeToHash[i.Code] = i.Hash
			}
		}

		cfg := ContentConfig{
			ContentType: ctype,
			Encoded:     make([]EncodedContent, 0, len(alts)),
		}
		cfg.Hash, err = t.checkAndAddFileFS(ctx, existingHash, dir, p)
		if err != nil {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to add file: %s", p))
		}
		for _, i := range alts {
			alt := p + i.Suffix
			h, err := t.checkAndAddFileFS(ctx, codeToHash[i.Code], dir, alt)
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

		if existingCfg != nil {
			if t.equalCfg(cfg, *existingCfg) {
				dstSet[dst] = struct{}{}
				t.log.Info(ctx, "Skipping unchanged content config",
					klog.AString("dst", dst),
				)
				return nil
			}
		}

		if err := t.db.Add(ctx, dst, cfg); err != nil {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to add content config for %s", p))
		}
		dstSet[dst] = struct{}{}
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
		if err := t.syncContentDir(ctx, dstSet, dstPrefix, ctype, dir, r, alts, path.Join(p, i.Name()), i); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tree) Add(ctx context.Context, dst string, ctype string, src string, encoded []EncodedFile) error {
	if dst == "" {
		return kerrors.WithMsg(nil, "Must provide dst")
	}
	for _, i := range encoded {
		if i.Code == "" {
			return kerrors.WithMsg(nil, "Must provide encoded file code")
		}
	}

	dst = path.Clean(dst)

	var existingHash string
	codeToHash := map[string]string{}
	existingCfg, err := t.db.Get(ctx, dst)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to check existing content config for %s", dst))
		}
		existingCfg = nil
	} else {
		existingHash = existingCfg.Hash
		for _, i := range existingCfg.Encoded {
			codeToHash[i.Code] = i.Hash
		}
	}

	cfg := ContentConfig{
		ContentType: ctype,
		Encoded:     make([]EncodedContent, 0, len(encoded)),
	}
	cfg.Hash, err = t.checkAndAddFile(ctx, existingHash, src)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to add file: %s", src))
	}
	for _, i := range encoded {
		dstName, err := t.checkAndAddFile(ctx, codeToHash[i.Code], i.Name)
		if err != nil {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to add encoded file: %s", i.Name))
		}
		cfg.Encoded = append(cfg.Encoded, EncodedContent{
			Code: i.Code,
			Hash: dstName,
		})
	}

	if existingCfg != nil {
		if t.equalCfg(cfg, *existingCfg) {
			t.log.Info(ctx, "Skipping unchanged content config",
				klog.AString("dst", dst),
			)
			return nil
		}
	}

	if err := t.db.Add(ctx, dst, cfg); err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to add content config for %s", dst))
	}
	t.log.Info(ctx, "Added content config",
		klog.AString("dst", dst),
	)
	return nil
}

func (t *Tree) equalCfg(a, b ContentConfig) bool {
	if a.Hash != b.Hash {
		return false
	}
	if a.ContentType != b.ContentType {
		return false
	}
	if len(a.Encoded) != len(b.Encoded) {
		return false
	}
	for n, i := range a.Encoded {
		if i != b.Encoded[n] {
			return false
		}
	}
	return true
}

func (t *Tree) checkAndAddFile(ctx context.Context, existingHash string, srcName string) (string, error) {
	dir, file := path.Split(srcName)
	dir = path.Clean(dir)
	file = path.Clean(file)
	fsys := os.DirFS(filepath.FromSlash(dir))
	return t.checkAndAddFileFS(ctx, existingHash, fsys, file)
}

func (t *Tree) checkAndAddFileFS(ctx context.Context, existingHash string, dir fs.FS, srcName string) (string, error) {
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

	existingMatchesSize := false
	if existingHash != "" {
		if existingInfo, err := fs.Stat(t.contentDir, existingHash); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return "", kerrors.WithMsg(err, "Failed to stat existing candidate dst file")
			}
		} else {
			if !existingInfo.IsDir() && existingInfo.Size() == srcInfo.Size() {
				if !existingInfo.ModTime().Before(srcInfo.ModTime()) {
					t.log.Info(ctx, "Skipping unchanged content file on matching size and modtime",
						klog.AString("src", srcName),
						klog.AString("dst", existingHash),
					)
					return existingHash, nil
				}
				existingMatchesSize = true
			}
		}
	}

	dstName, err := t.hashFile(dir, srcName)
	if err != nil {
		return "", kerrors.WithMsg(err, "Failed to hash src file")
	}

	if existingHash != "" && dstName == existingHash && existingMatchesSize {
		if err := kfs.Chtimes(t.contentDir, existingHash, time.Time{}, srcInfo.ModTime()); err != nil {
			return "", kerrors.WithMsg(err, fmt.Sprintf("Failed to update mod time for dst file %s", existingHash))
		}
		t.log.Info(ctx, "Skipping unchanged content file on matching size and hash",
			klog.AString("src", srcName),
			klog.AString("dst", existingHash),
		)
		return existingHash, nil
	}

	// need to recheck since dstName may not be equal to existingHash
	if dstInfo, err := fs.Stat(t.contentDir, dstName); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", kerrors.WithMsg(err, fmt.Sprintf("Failed to stat dst file: %s", dstName))
		}
	} else {
		if dstInfo.IsDir() {
			return "", kerrors.WithMsg(nil, fmt.Sprintf("Dst file %s is dir", dstName))
		}
		if dstInfo.Size() == srcInfo.Size() && !dstInfo.ModTime().Before(srcInfo.ModTime()) {
			t.log.Info(ctx, "Skipping unchanged content file",
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

func (t *Tree) Rm(ctx context.Context, name string) error {
	if name == "" {
		return kerrors.WithMsg(nil, "Must provide name")
	}
	name = path.Clean(name)
	cfg, err := t.db.Get(ctx, name)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to get content config for %s", name))
	}
	return t.rmContent(ctx, name, *cfg)
}

func (t *Tree) rmContent(ctx context.Context, name string, cfg ContentConfig) error {
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
	if err := t.db.Rm(ctx, name); err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to remove content config for %s", name))
	}
	t.log.Info(ctx, "Removed content config",
		klog.AString("name", name),
	)
	return nil
}

func (t *Tree) GCContentDir(ctx context.Context) error {
	contentSet := map[string]struct{}{}
	if err := t.db.Iterate(ctx, func(ctx context.Context, name string, cfg ContentConfig) error {
		contentSet[cfg.Hash] = struct{}{}
		for _, i := range cfg.Encoded {
			contentSet[i.Hash] = struct{}{}
		}
		return nil
	}); err != nil {
		return kerrors.WithMsg(err, "Failed iterating through tree db")
	}
	if err := t.rmSetDifference(contentSet); err != nil {
		return err
	}
	return nil
}

func (t *Tree) rmSetDifference(contentSet map[string]struct{}) error {
	entries, err := fs.ReadDir(t.contentDir, ".")
	if err != nil {
		return kerrors.WithMsg(err, "Failed to read root content dir")
	}
	for _, i := range entries {
		name := i.Name()
		if _, ok := contentSet[name]; !ok {
			if err := kfs.RemoveAll(t.contentDir, name); err != nil {
				return kerrors.WithMsg(err, fmt.Sprintf("Failed to remove content file: %s", name))
			}
		}
	}
	return nil
}

func (t *Tree) Setup(ctx context.Context) error {
	if err := t.db.Setup(ctx); err != nil {
		return kerrors.WithMsg(err, "Failed to init tree db")
	}
	return nil
}
