package videoartifact

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// FilesystemBlobStore stores complete immutable objects on a private volume.
// All serving/worker instances must mount the SAME coherent filesystem, with
// atomic hard-link creation. Local per-instance disks are not shared storage.
// Root access is restricted to this service account; Windows ACLs must be set
// by deployment. The constructor does not create or relax directory permissions.
type FilesystemBlobStore struct{ root *os.Root }

var _ BlobStore = (*FilesystemBlobStore)(nil)
var artifactKeyPattern = regexp.MustCompile(`^video-artifacts/[1-9][0-9]{0,19}/task_[A-Za-z0-9_-]{1,123}/(video|last_frame)/[1-9][0-9]{0,19}-[a-f0-9]{32}$`)

func NewFilesystemBlobStore(absoluteRoot string) (*FilesystemBlobStore, error) {
	if !filepath.IsAbs(absoluteRoot) {
		return nil, failure("invalid_artifact_config", 503, false)
	}
	clean := filepath.Clean(absoluteRoot)
	if filepath.Dir(clean) == clean {
		return nil, failure("invalid_artifact_config", 503, false)
	}
	// Reject even in-root aliases. os.Root additionally enforces containment on
	// every operation, including when paths are concurrently renamed.
	for current := clean; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, failure("invalid_artifact_config", 503, false)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	info, err := os.Stat(clean)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, failure("invalid_artifact_config", 503, false)
	}
	root, err := os.OpenRoot(clean)
	if err != nil {
		return nil, failure("invalid_artifact_config", 503, false)
	}
	return &FilesystemBlobStore{root: root}, nil
}

func blobName(key string) (string, error) {
	if !artifactKeyPattern.MatchString(key) {
		return "", failure("invalid_artifact_key", 422, false)
	}
	sum := sha256.Sum256([]byte("video-artifact-object:v1\x00" + key))
	return hex.EncodeToString(sum[:]) + ".blob", nil
}

func (s *FilesystemBlobStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	if err := s.root.Close(); err != nil {
		return failure("artifact_blob_unavailable", 503, true)
	}
	return nil
}

func storageFailure(ctx context.Context) error {
	if ctx.Err() != nil {
		return failure("artifact_blob_cancelled", 503, true)
	}
	return failure("artifact_blob_unavailable", 503, true)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	return n, err
}

func (s *FilesystemBlobStore) Put(ctx context.Context, key string, reader io.Reader, metadata Metadata) error {
	name, err := blobName(key)
	if err != nil {
		return err
	}
	hashBytes, err := hex.DecodeString(metadata.SHA256)
	if err != nil || len(hashBytes) != 32 || metadata.SHA256 != strings.ToLower(metadata.SHA256) || metadata.Size <= 0 || metadata.Size > 10<<30 || reader == nil {
		return failure("invalid_artifact", 422, false)
	}
	if s == nil || s.root == nil || ctx.Err() != nil {
		return storageFailure(ctx)
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return storageFailure(ctx)
	}
	temporary := ".tmp-" + hex.EncodeToString(random[:])
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return storageFailure(ctx)
	}
	defer s.root.Remove(temporary)
	defer file.Close()
	stop := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stop()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx: ctx, reader: reader}, metadata.Size+1))
	if err != nil || ctx.Err() != nil {
		return storageFailure(ctx)
	}
	if written != metadata.Size || hex.EncodeToString(hash.Sum(nil)) != metadata.SHA256 {
		return failure("artifact_integrity_mismatch", 422, false)
	}
	if err = file.Sync(); err != nil {
		return storageFailure(ctx)
	}
	if err = file.Close(); err != nil {
		return storageFailure(ctx)
	}
	if ctx.Err() != nil {
		return storageFailure(ctx)
	}
	// Hard-link publication is atomic and never replaces an existing key. A
	// rename could overwrite a concurrent writer on Unix; do not use it here.
	if err = s.root.Link(temporary, name); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return storageFailure(ctx)
		}
		return s.verifyExisting(ctx, name, metadata)
	}
	// A cancellation after publication can leave a COMPLETE object. A retry is
	// safe and verifies the original bytes; partial content is never visible.
	if ctx.Err() != nil {
		return storageFailure(ctx)
	}
	return s.syncDirectory(ctx)
}

func (s *FilesystemBlobStore) syncDirectory(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return storageFailure(ctx)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil || ctx.Err() != nil {
		return storageFailure(ctx)
	}
	return nil
}

func (s *FilesystemBlobStore) openRegular(name string) (*os.File, error) {
	before, err := s.root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return nil, failure("artifact_blob_unavailable", 503, true)
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, failure("artifact_blob_unavailable", 503, true)
	}
	actual, statErr := file.Stat()
	after, lstatErr := s.root.Lstat(name)
	if statErr != nil || lstatErr != nil || !actual.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, actual) || !os.SameFile(actual, after) {
		_ = file.Close()
		return nil, failure("artifact_blob_unavailable", 503, true)
	}
	return file, nil
}

func (s *FilesystemBlobStore) verifyExisting(ctx context.Context, name string, metadata Metadata) error {
	file, err := s.openRegular(name)
	if err != nil {
		return err
	}
	defer file.Close()
	stop := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stop()
	info, err := file.Stat()
	if err != nil {
		return storageFailure(ctx)
	}
	if info.Size() != metadata.Size {
		return failure("artifact_blob_conflict", 409, false)
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(contextReader{ctx: ctx, reader: file}, metadata.Size+1))
	if err != nil || ctx.Err() != nil {
		return storageFailure(ctx)
	}
	if read != metadata.Size || hex.EncodeToString(hash.Sum(nil)) != metadata.SHA256 {
		return failure("artifact_blob_conflict", 409, false)
	}
	return nil
}

type contextualFile struct {
	file *os.File
	ctx  context.Context
	stop func() bool
}

func (f *contextualFile) Read(p []byte) (int, error) {
	n, err := (contextReader{ctx: f.ctx, reader: f.file}).Read(p)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return n, failure("artifact_blob_unavailable", 503, true)
	}
	return n, err
}
func (f *contextualFile) Close() error {
	f.stop()
	if err := f.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return failure("artifact_blob_unavailable", 503, true)
	}
	return nil
}

func (s *FilesystemBlobStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	name, err := blobName(key)
	if err != nil {
		return nil, err
	}
	if s == nil || s.root == nil || ctx.Err() != nil {
		return nil, storageFailure(ctx)
	}
	file, err := s.openRegular(name)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = file.Close() })
	if ctx.Err() != nil {
		stop()
		_ = file.Close()
		return nil, storageFailure(ctx)
	}
	return &contextualFile{file: file, ctx: ctx, stop: stop}, nil
}

func (s *FilesystemBlobStore) Delete(ctx context.Context, key string) error {
	name, err := blobName(key)
	if err != nil {
		return err
	}
	if s == nil || s.root == nil || ctx.Err() != nil {
		return storageFailure(ctx)
	}
	info, err := s.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return storageFailure(ctx)
	}
	if ctx.Err() != nil {
		return storageFailure(ctx)
	}
	if err = s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return storageFailure(ctx)
	}
	return s.syncDirectory(ctx)
}
