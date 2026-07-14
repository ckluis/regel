// Package gitproj is the ADR-09 git projection: the deterministic outbound fold
// over the ADR-03 admission ledger into native git objects, a kernel-owned local
// bare mirror with self-healing restore, and the inbound git-submission door that
// runs the real ADR-07 pipeline. Pure-Go object construction (blob/tree/commit,
// SHA-1 ids, zlib loose objects) — no git binary, no cgo, zero third-party deps
// (compress/zlib + crypto/sha1 are stdlib). The image is truth; the repo is a
// view any stock git client can verify.
package gitproj

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Git object mode strings.
const (
	modeBlob = "100644" // a regular non-executable file
	modeTree = "40000"  // a subtree (git stores directories without a leading 0)
)

// objectStore is where loose objects live. The bare repo (repo.go) implements it;
// tests can supply an in-memory store to fold without touching the filesystem.
type objectStore interface {
	// put writes the loose object for (objType, content) and returns its 40-hex
	// SHA-1 object id. Idempotent: writing identical bytes twice is a no-op.
	put(objType string, content []byte) (string, error)
}

// oidOf computes the git object id (40-hex SHA-1 over the typed, length-prefixed
// payload) WITHOUT writing it — used to compare a computed head against a mirror
// ref, and by tests.
func oidOf(objType string, content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d\x00", objType, len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// looseBytes returns (oid, zlib-compressed loose-object bytes) for a git object.
// The uncompressed framing is `<type> <len>\x00<content>` and the id is its SHA-1;
// the on-disk form is that framing zlib-compressed (git's loose format).
func looseBytes(objType string, content []byte) (string, []byte, error) {
	framed := make([]byte, 0, len(content)+32)
	framed = append(framed, []byte(fmt.Sprintf("%s %d\x00", objType, len(content)))...)
	framed = append(framed, content...)
	sum := sha1.Sum(framed)
	oid := hex.EncodeToString(sum[:])
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(framed); err != nil {
		return "", nil, err
	}
	if err := zw.Close(); err != nil {
		return "", nil, err
	}
	return oid, buf.Bytes(), nil
}

// writeBlob stores file content as a git blob and returns its oid.
func writeBlob(store objectStore, content []byte) (string, error) {
	return store.put("blob", content)
}

// --- trees -------------------------------------------------------------------

// treeNode is one directory during tree construction: a map of child name to
// either a blob (leaf) or a subtree.
type treeNode struct {
	children map[string]*treeChild
}

type treeChild struct {
	blobOid string    // set iff this child is a file
	sub     *treeNode // set iff this child is a subdirectory
}

func newTreeNode() *treeNode { return &treeNode{children: map[string]*treeChild{}} }

// insert threads a repo-relative path (e.g. "app/demo/greet.ts") to its blob oid,
// materializing intermediate subtrees.
func (t *treeNode) insert(path, blobOid string) {
	parts := strings.Split(path, "/")
	node := t
	for i, p := range parts {
		if i == len(parts)-1 {
			node.children[p] = &treeChild{blobOid: blobOid}
			return
		}
		c := node.children[p]
		if c == nil || c.sub == nil {
			c = &treeChild{sub: newTreeNode()}
			node.children[p] = c
		}
		node = c.sub
	}
}

// treeEntryKey is the git sort key for a tree entry: the raw name, with a trailing
// "/" for a subtree. Git orders entries by this key byte-for-byte, so a directory
// and a file sharing a prefix interleave exactly as git expects (git fsck rejects
// any other order).
func treeEntryKey(name string, isTree bool) string {
	if isTree {
		return name + "/"
	}
	return name
}

// write encodes this tree (recursively writing subtrees first) and returns its oid.
func (t *treeNode) write(store objectStore) (string, error) {
	type enc struct {
		mode string
		name string
		oid  string
		key  string
	}
	entries := make([]enc, 0, len(t.children))
	for name, c := range t.children {
		if c.sub != nil {
			sub, err := c.sub.write(store)
			if err != nil {
				return "", err
			}
			entries = append(entries, enc{mode: modeTree, name: name, oid: sub, key: treeEntryKey(name, true)})
		} else {
			entries = append(entries, enc{mode: modeBlob, name: name, oid: c.blobOid, key: treeEntryKey(name, false)})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })

	var body bytes.Buffer
	for _, e := range entries {
		raw, err := hex.DecodeString(e.oid)
		if err != nil {
			return "", err
		}
		body.WriteString(e.mode)
		body.WriteByte(' ')
		body.WriteString(e.name)
		body.WriteByte(0)
		body.Write(raw)
	}
	return store.put("tree", body.Bytes())
}

// --- commits -----------------------------------------------------------------

// ident is a git author/committer identity plus its timestamp.
type ident struct {
	name  string
	email string
	unix  int64 // seconds since epoch (git commit granularity)
	tz    string // e.g. "+0000"
}

func (id ident) line(role string) string {
	return fmt.Sprintf("%s %s <%s> %d %s", role, id.name, id.email, id.unix, id.tz)
}

// commitSpec is everything a projection commit derives from ledger + catalog data.
type commitSpec struct {
	tree      string // tree oid
	parent    string // "" for the root commit
	author    ident
	committer ident
	message   string
}

// writeCommit encodes a commit object and returns its oid.
func writeCommit(store objectStore, c commitSpec) (string, error) {
	var b bytes.Buffer
	b.WriteString("tree " + c.tree + "\n")
	if c.parent != "" {
		b.WriteString("parent " + c.parent + "\n")
	}
	b.WriteString(c.author.line("author") + "\n")
	b.WriteString(c.committer.line("committer") + "\n")
	b.WriteString("\n")
	b.WriteString(c.message)
	if !strings.HasSuffix(c.message, "\n") {
		b.WriteByte('\n')
	}
	return store.put("commit", b.Bytes())
}
