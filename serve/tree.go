package serve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"golang.org/x/crypto/blake2b"
	"xorkevin.dev/kerrors"
	"xorkevin.dev/klog"
)

type (
	Tree struct {
		log *klog.LevelLogger
		dir fs.FS
	}
)

func NewTree(log klog.Logger, dir fs.FS) *Tree {
	return &Tree{
		log: klog.NewLevelLogger(log),
		dir: dir,
	}
}

func (t *Tree) Checksum(ctx context.Context, routes []Route, force bool) error {
	if err := parseRoutes(routes); err != nil {
		return err
	}

	visitedSet := map[string]struct{}{}

	for _, i := range routes {
		t.log.Info(context.Background(), "Checksum route",
			klog.AString("route.prefix", i.Prefix),
			klog.AString("route.fspath", i.Path),
			klog.ABool("route.dir", i.Dir),
		)

		stat, err := fs.Stat(t.dir, i.Path)
		if err != nil {
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", i.Path))
		}

		if i.Dir {
			if !stat.IsDir() {
				return kerrors.WithMsg(err, fmt.Sprintf("File %s is not a directory", i.Path))
			}
			if err := t.checksumDir(ctx, visitedSet, i, "", fs.FileInfoToDirEntry(stat)); err != nil {
				return err
			}
		} else {
			if stat.IsDir() {
				return kerrors.WithMsg(err, fmt.Sprintf("File %s is a directory", i.Path))
			}
			if err := t.hashFileAndStore(ctx, visitedSet, i.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Tree) checksumDir(ctx context.Context, visitedSet map[string]struct{}, route Route, name string, entry fs.DirEntry) error {
	p := path.Join(route.Path, name)

	if !entry.IsDir() {
		if !routeMatchPath(route, name) {
			t.log.Debug(ctx, "Skipping unmatched file",
				klog.AString("route.prefix", route.Prefix),
				klog.AString("path", p),
			)
			return nil
		}

		if err := t.checksumFile(ctx, visitedSet, route, name); err != nil {
			return err
		}
		return nil
	}

	entries, err := fs.ReadDir(t.dir, p)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed reading dir %s", p))
	}
	t.log.Debug(ctx, "Exploring dir",
		klog.AString("route.prefix", route.Prefix),
		klog.AString("path", p),
	)
	for _, i := range entries {
		if err := t.checksumDir(ctx, visitedSet, route, path.Join(name, i.Name()), i); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tree) checksumFile(ctx context.Context, visitedSet map[string]struct{}, route Route, name string) error {
	p := path.Join(route.Path, name)

	if err := t.hashFileAndStore(ctx, visitedSet, p); err != nil {
		return err
	}

	for _, i := range route.Encodings {
		if i.match != nil {
			if !i.match.MatchString(name) {
				continue
			}
		}
		alt := p + i.Ext
		stat, err := fs.Stat(t.dir, alt)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", alt))
		}
		if stat.IsDir() {
			continue
		}
		if err := t.hashFileAndStore(ctx, visitedSet, alt); err != nil {
			return err
		}
	}

	return nil
}

func (t *Tree) hashFileAndStore(ctx context.Context, visitedSet map[string]struct{}, p string) error {
	if _, ok := visitedSet[p]; ok {
		t.log.Debug(ctx, "Skipping rehashing file",
			klog.AString("path", p),
		)
		return nil
	}

	_, err := t.hashFile(t.dir, p)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to hash file %s", p))
	}

	// TODO place file hash on xattrs

	visitedSet[p] = struct{}{}
	t.log.Info(ctx, "Hashed file",
		klog.AString("path", p),
	)
	return nil
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
