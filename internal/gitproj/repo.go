package gitproj

import (
	"os"
	"path/filepath"
	"strings"
)

// BareRepo is a kernel-owned local bare git repository (ADR-09 §3 BUILD-C): loose
// objects under objects/, a single writable truth branch refs/heads/main advanced
// by the projector identity only, and the minimal metadata (HEAD, config) a stock
// git client needs to read it. There is no working tree; the repo is a served
// cache of the computed fold, never a source of truth.
type BareRepo struct {
	dir string
}

const mainRef = "refs/heads/main"

// OpenBare opens (creating if absent) a bare repository at dir. Idempotent.
func OpenBare(dir string) (*BareRepo, error) {
	r := &BareRepo{dir: dir}
	if err := r.ensure(); err != nil {
		return nil, err
	}
	return r, nil
}

// Dir is the repository's filesystem path (the --git-dir a git client points at).
func (r *BareRepo) Dir() string { return r.dir }

func (r *BareRepo) ensure() error {
	for _, d := range []string{r.dir, filepath.Join(r.dir, "objects"), filepath.Join(r.dir, "refs", "heads")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// HEAD points at main so `git log`/`git fsck` resolve without a checkout.
	head := filepath.Join(r.dir, "HEAD")
	if _, err := os.Stat(head); os.IsNotExist(err) {
		if err := os.WriteFile(head, []byte("ref: "+mainRef+"\n"), 0o644); err != nil {
			return err
		}
	}
	cfg := filepath.Join(r.dir, "config")
	if _, err := os.Stat(cfg); os.IsNotExist(err) {
		body := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n"
		if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// put writes a loose object (idempotent) and returns its oid. Implements objectStore.
func (r *BareRepo) put(objType string, content []byte) (string, error) {
	oid, loose, err := looseBytes(objType, content)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(r.dir, "objects", oid[:2])
	path := filepath.Join(dir, oid[2:])
	if _, err := os.Stat(path); err == nil {
		return oid, nil // already stored — objects are immutable by id
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, loose); err != nil {
		return "", err
	}
	return oid, nil
}

// hasObject reports whether the loose object oid is present.
func (r *BareRepo) hasObject(oid string) bool {
	if len(oid) < 3 {
		return false
	}
	_, err := os.Stat(filepath.Join(r.dir, "objects", oid[:2], oid[2:]))
	return err == nil
}

// readMain returns the current main SHA, or "" if the ref is absent.
func (r *BareRepo) readMain() (string, error) {
	b, err := os.ReadFile(filepath.Join(r.dir, mainRef))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// setMain atomically advances (or restores) refs/heads/main to sha. This is the
// projector's sole write to the truth branch (ADR-09 §3).
func (r *BareRepo) setMain(sha string) error {
	path := filepath.Join(r.dir, mainRef)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(sha+"\n"))
}

// writeFileAtomic writes via a temp file + rename so a concurrent reader never
// sees a half-written ref/object.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// memStore is an in-memory objectStore for folding without a filesystem (the
// two-fold determinism gate builds two independent memStores and compares SHAs).
type memStore struct {
	objs map[string][]byte // oid → framed (uncompressed) content, for optional inspection
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) put(objType string, content []byte) (string, error) {
	oid := oidOf(objType, content)
	if _, ok := m.objs[oid]; !ok {
		cp := make([]byte, len(content))
		copy(cp, content)
		m.objs[oid] = cp
	}
	return oid, nil
}
