package videoartifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const fileKey = "video-artifacts/1/task_filesystem/video/1-0123456789abcdef0123456789abcdef"

func fileMetadata(data []byte) Metadata {
	hash := sha256.Sum256(data)
	return Metadata{Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), MediaType: "video/mp4"}
}

func filesystemStore(t *testing.T) (*FilesystemBlobStore, string) {
	t.Helper()
	// TempDir's numbered child uses 0777 subject to umask, not the private
	// permissions required for a production artifact volume on Unix.
	root := filepath.Join(t.TempDir(), "private-artifacts")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewFilesystemBlobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, root
}

func assertNoPartial(t *testing.T, store *FilesystemBlobStore, root string) {
	t.Helper()
	if body, err := store.Open(context.Background(), fileKey); err == nil {
		_ = body.Close()
		t.Fatal("partial object visible")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unremoved temporary files: %v", entries)
	}
}

func TestFilesystemPrivateAbsoluteRoot(t *testing.T) {
	for _, root := range []string{"relative", "", filepath.Join(t.TempDir(), "missing")} {
		_, err := NewFilesystemBlobStore(root)
		code(t, err, "invalid_artifact_config")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewFilesystemBlobStore(file)
	code(t, err, "invalid_artifact_config")
	if runtime.GOOS != "windows" {
		root := t.TempDir()
		if err = os.Chmod(root, 0755); err != nil {
			t.Fatal(err)
		}
		_, err = NewFilesystemBlobStore(root)
		code(t, err, "invalid_artifact_config")
	}
}

func TestFilesystemPutOpenDeleteAndSharedInstances(t *testing.T) {
	store, root := filesystemStore(t)
	second, err := NewFilesystemBlobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	data := []byte("complete simulated video artifact")
	meta := fileMetadata(data)
	if err = store.Put(context.Background(), fileKey, bytes.NewReader(data), meta); err != nil {
		t.Fatal(err)
	}
	if err = second.Put(context.Background(), fileKey, bytes.NewReader(data), meta); err != nil {
		t.Fatal(err)
	}
	body, err := second.Open(context.Background(), fileKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("got=%q err=%v", got, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".blob") {
		t.Fatal(entries)
	}
	if err = store.Delete(context.Background(), fileKey); err != nil {
		t.Fatal(err)
	}
	if err = second.Delete(context.Background(), fileKey); err != nil {
		t.Fatal(err)
	}
	assertNoPartial(t, store, root)
}

func TestFilesystemConcurrentImmutablePublication(t *testing.T) {
	store, root := filesystemStore(t)
	second, err := NewFilesystemBlobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	data := []byte("same object bytes")
	meta := fileMetadata(data)
	errorsOut := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			target := store
			if index%2 == 1 {
				target = second
			}
			errorsOut <- target.Put(context.Background(), fileKey, bytes.NewReader(data), meta)
		}(i)
	}
	wg.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	other := []byte("different content")
	err = second.Put(context.Background(), fileKey, bytes.NewReader(other), fileMetadata(other))
	code(t, err, "artifact_blob_conflict")
	body, err := store.Open(context.Background(), fileKey)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	_ = body.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("concurrent overwrite")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
}

type errorReader struct{ sent bool }

func (r *errorReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		copy(p, "partial")
		return len("partial"), nil
	}
	return 0, errors.New("untrusted input path")
}

type cancelReader struct{ cancel context.CancelFunc }

func (r cancelReader) Read(p []byte) (int, error) {
	copy(p, "partial")
	r.cancel()
	return len("partial"), nil
}

func TestFilesystemRejectsPartialCorruptOversizeAndCancellation(t *testing.T) {
	data := []byte("expected complete data")
	for _, name := range []string{"short", "oversize", "wrong hash", "read error", "cancel during read", "already cancelled"} {
		t.Run(name, func(t *testing.T) {
			store, root := filesystemStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			meta := fileMetadata(data)
			var reader io.Reader = bytes.NewReader(data)
			switch name {
			case "short":
				reader = strings.NewReader("short")
			case "oversize":
				reader = bytes.NewReader(append(data, 'x'))
			case "wrong hash":
				meta.SHA256 = strings.Repeat("a", 64)
			case "read error":
				reader = &errorReader{}
			case "cancel during read":
				reader = cancelReader{cancel: cancel}
			case "already cancelled":
				cancel()
			}
			if err := store.Put(ctx, fileKey, reader, meta); err == nil {
				t.Fatal("invalid put accepted")
			}
			assertNoPartial(t, store, root)
		})
	}
}

func TestFilesystemRejectsPathTraversalAndReservedNames(t *testing.T) {
	store, root := filesystemStore(t)
	for _, key := range []string{"../outside", "/absolute", "C:\\outside", fileKey + "/../secret", strings.Replace(fileKey, "/", "\\", -1), "NUL", fileKey + ":stream", "video-artifacts/1/task_x/video/0-0123456789abcdef0123456789abcdef"} {
		if err := store.Put(context.Background(), key, strings.NewReader("x"), fileMetadata([]byte("x"))); err == nil {
			t.Fatalf("put accepted %q", key)
		}
		if body, err := store.Open(context.Background(), key); err == nil {
			_ = body.Close()
			t.Fatalf("open accepted %q", key)
		}
		if err := store.Delete(context.Background(), key); err == nil {
			t.Fatalf("delete accepted %q", key)
		}
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestFilesystemRejectsSymlinkRootAndObject(t *testing.T) {
	parent := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("host cannot create symlinks: %v", err)
	}
	if store, err := NewFilesystemBlobStore(link); err == nil {
		_ = store.Close()
		t.Fatal("symlink root accepted")
	}
	if err := os.Mkdir(filepath.Join(target, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if store, err := NewFilesystemBlobStore(filepath.Join(link, "child")); err == nil {
		_ = store.Close()
		t.Fatal("symlink ancestor accepted")
	}
	store, root := filesystemStore(t)
	name, _ := blobName(fileKey)
	outside := filepath.Join(parent, "outside")
	data := []byte("secret unrelated data")
	if err := os.WriteFile(outside, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if body, err := store.Open(context.Background(), fileKey); err == nil {
		_ = body.Close()
		t.Fatal("symlink object read")
	}
	if err := store.Put(context.Background(), fileKey, bytes.NewReader(data), fileMetadata(data)); err == nil {
		t.Fatal("symlink object accepted as idempotent")
	}
	if err := store.Delete(context.Background(), fileKey); err == nil {
		t.Fatal("symlink object deleted")
	}
	got, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("outside file modified")
	}
}

func TestFilesystemContextCancelsOpenReaderAndDelete(t *testing.T) {
	store, _ := filesystemStore(t)
	data := []byte("private output")
	if err := store.Put(context.Background(), fileKey, bytes.NewReader(data), fileMetadata(data)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	body, err := store.Open(ctx, fileKey)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err = body.Read(make([]byte, 16)); !errors.Is(err, context.Canceled) {
		t.Fatalf("read error=%v", err)
	}
	_ = body.Close()
	if err = store.Delete(ctx, fileKey); err == nil {
		t.Fatal("cancelled delete ran")
	}
	if body, err = store.Open(context.Background(), fileKey); err != nil {
		t.Fatal(err)
	} else {
		_ = body.Close()
	}
}
