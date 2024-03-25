package serve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/blake2b"
	"xorkevin.dev/kerrors"
	"xorkevin.dev/kfs"
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
			if err := t.checksumDir(ctx, visitedSet, i, "", fs.FileInfoToDirEntry(stat), force); err != nil {
				return err
			}
		} else {
			if stat.IsDir() {
				return kerrors.WithMsg(err, fmt.Sprintf("File %s is a directory", i.Path))
			}
			if err := t.checksumFile(ctx, visitedSet, i, "", force); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Tree) checksumDir(ctx context.Context, visitedSet map[string]struct{}, route Route, name string, entry fs.DirEntry, force bool) error {
	p := path.Join(route.Path, name)

	if !entry.IsDir() {
		if !routeMatchPath(route, name) {
			t.log.Debug(ctx, "Skipping unmatched file",
				klog.AString("route.prefix", route.Prefix),
				klog.AString("path", p),
			)
			return nil
		}

		if err := t.checksumFile(ctx, visitedSet, route, name, force); err != nil {
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
		if err := t.checksumDir(ctx, visitedSet, route, path.Join(name, i.Name()), i, force); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tree) checksumFile(ctx context.Context, visitedSet map[string]struct{}, route Route, name string, force bool) error {
	p := path.Join(route.Path, name)

	if err := t.hashFileAndStore(ctx, visitedSet, p, force); err != nil {
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
		if err := t.hashFileAndStore(ctx, visitedSet, alt, force); err != nil {
			return err
		}
	}

	return nil
}

func (t *Tree) hashFileAndStore(ctx context.Context, visitedSet map[string]struct{}, p string, force bool) error {
	if _, ok := visitedSet[p]; ok {
		t.log.Debug(ctx, "Skipping rehashing file",
			klog.AString("path", p),
		)
		return nil
	}

	fullFilePath, err := kfs.FullFilePath(t.dir, p)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to get full file path for file %s", p))
	}
	currentStat, err := fs.Stat(t.dir, p)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to stat file %s", p))
	}
	currentTag := statToTag(currentStat)
	if currentTag == "" {
		return kerrors.WithMsg(nil, fmt.Sprintf("Unable to read modification time of file %s", p))
	}
	existingHash, existingTag, err := readChecksumXAttr(fullFilePath)
	if err != nil {
		if errors.Is(err, ErrMalformedChecksum) {
			t.log.Warn(ctx, "Found malformed checksum on file",
				klog.AString("path", p),
			)
		} else {
			return err
		}
	}
	if currentTag == existingTag && !force {
		return nil
	}

	hash, tag, err := t.hashFile(p)
	if err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed to hash file %s", p))
	}
	if tag != currentTag {
		return kerrors.WithMsg(nil, fmt.Sprintf("File changed while hashing %s", p))
	}

	if hash != existingHash || tag != existingTag {
		if tag == existingTag && hash != existingHash {
			t.log.Warn(ctx, "Checksum mismatch on file for matching tag",
				klog.AString("path", p),
			)
		}

		if err := setChecksumXAttr(fullFilePath, hash, tag); err != nil {
			return err
		}
	}

	visitedSet[p] = struct{}{}
	t.log.Info(ctx, "Hashed file",
		klog.AString("path", p),
	)
	fmt.Println("hashed", p)
	return nil
}

const (
	xattrChecksum     = "user.fsserve.checksum"
	checksumSeparator = ":"
	checksumVersion   = "v1"
	checksumPrefix    = checksumVersion + checksumSeparator
)

func readChecksumXAttr(fullFilePath string) (string, string, error) {
	var buf [128]byte
	val, err := readXAttr(fullFilePath, xattrChecksum, buf[:])
	if err != nil {
		return "", "", err
	}
	if val == "" {
		return "", "", nil
	}
	val, ok := strings.CutPrefix(val, checksumPrefix)
	if !ok {
		return "", "", kerrors.WithKind(nil, ErrMalformedChecksum, "Malformed checksum")
	}
	hash, tag, ok := strings.Cut(val, checksumSeparator)
	if !ok {
		return "", "", kerrors.WithKind(nil, ErrMalformedChecksum, "Malformed checksum")
	}
	return hash, tag, nil
}

func setChecksumXAttr(fullFilePath string, hash, tag string) error {
	return setXAttr(fullFilePath, xattrChecksum, checksumPrefix+hash+":"+tag)
}

func readXAttr(fullFilePath string, attr string, buf []byte) (string, error) {
	for {
		size, err := syscall.Getxattr(filepath.FromSlash(fullFilePath), attr, buf)
		if err != nil {
			if errors.Is(err, syscall.ENODATA) {
				return "", nil
			}
			return "", kerrors.WithMsg(err, fmt.Sprintf("Failed getting xattr %s of file %s", attr, fullFilePath))
		}
		if size <= len(buf) {
			return string(buf[:size]), nil
		}
		buf = make([]byte, size)
	}
}

func setXAttr(fullFilePath string, attr string, val string) error {
	if err := syscall.Setxattr(filepath.FromSlash(fullFilePath), attr, []byte(val), 0); err != nil {
		return kerrors.WithMsg(err, fmt.Sprintf("Failed setting xattr %s of file %s", attr, fullFilePath))
	}
	return nil
}

func (t *Tree) hashFile(p string) (_ string, _ string, retErr error) {
	f, err := t.dir.Open(p)
	if err != nil {
		return "", "", kerrors.WithMsg(err, "Failed opening file")
	}
	defer func() {
		if err := f.Close(); err != nil {
			retErr = errors.Join(retErr, kerrors.WithMsg(err, "Failed to close file"))
		}
	}()
	stat, err := f.Stat()
	if err != nil {
		return "", "", kerrors.WithMsg(err, "Failed to stat file")
	}
	tag := statToTag(stat)
	if tag == "" {
		return "", "", kerrors.WithMsg(nil, "Unable to read file modification time")
	}
	h, err := blake2b.New256(nil)
	if err != nil {
		return "", "", kerrors.WithMsg(err, "Failed creating blake2b hash")
	}
	if _, err := io.Copy(h, f); err != nil {
		return "", "", kerrors.WithMsg(err, "Failed reading file")
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)), tag, nil
}
