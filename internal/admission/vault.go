package admission

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"regel.dev/regel/internal/pgwire"
)

// vault.go is the kernel-side pii vault runtime (ADR-10 §4 item 5 / §5 modifier): a
// pii value is AES-256-GCM sealed under a PER-SUBJECT key token and stored
// ciphertext-only in the `vault` table (never in the derived base table nor its
// history). Reads mask by default; a reveal requires the plaintext path (grant-gated
// upstream). CRYPTO-SHRED deletes the subject's key row + writes an attestation, after
// which the ciphertext is permanently undecryptable — the subject's key is gone.
//
// The AEAD scheme is identical to std/crypto (cek.StdAeadSeal): key = SHA-256(token),
// AES-256-GCM, hex(nonce ‖ ciphertext). Kernel-side here because the derived write
// path needs it as a Go API, not a dialect value; std/crypto §3 keeps key material
// out of the dialect, and this keeps the token (never the key) as the only handle.

// VaultMaskToken is what a pii field materializes to when not revealed (ADR-12 §4) —
// a token carrying none of the underlying value. Matches the MCP plane's token.
const VaultMaskToken = "‹masked›"

// vaultKeyFor loads the subject's key token, minting + persisting a fresh random one
// on first write. A missing key row on READ means the subject was crypto-shredded
// (ok=false) — the caller then returns the mask token, never a decryption.
func vaultKeyFor(ctx context.Context, conn *pgwire.Conn, resource, subjectID string, mint bool) (string, bool, error) {
	var token string
	found, err := conn.QueryRow(ctx,
		`SELECT key_token FROM vault_key WHERE resource=$1 AND subject_id=$2`,
		[]any{resource, subjectID}, &token)
	if err != nil {
		return "", false, err
	}
	if found {
		return token, true, nil
	}
	if !mint {
		return "", false, nil
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", false, err
	}
	token = hex.EncodeToString(raw[:])
	if _, err := conn.Exec(ctx,
		`INSERT INTO vault_key (resource, subject_id, key_token) VALUES ($1,$2,$3)
		 ON CONFLICT (resource, subject_id) DO NOTHING`,
		resource, subjectID, token); err != nil {
		return "", false, err
	}
	// Re-read in case a concurrent writer won the ON CONFLICT race.
	ok, err := conn.QueryRow(ctx,
		`SELECT key_token FROM vault_key WHERE resource=$1 AND subject_id=$2`,
		[]any{resource, subjectID}, &token)
	if err != nil || !ok {
		return "", false, err
	}
	return token, true, nil
}

// VaultPut seals a pii value under the subject's key and stores it ciphertext-only.
// The plaintext never touches the base table or history — only `vault.ciphertext`.
func VaultPut(ctx context.Context, conn *pgwire.Conn, resource, subjectID, field, plaintext string) error {
	token, _, err := vaultKeyFor(ctx, conn, resource, subjectID, true)
	if err != nil {
		return err
	}
	ct, err := aeadSeal(token, plaintext)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx,
		`INSERT INTO vault (resource, subject_id, field, ciphertext) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (resource, subject_id, field) DO UPDATE SET ciphertext=EXCLUDED.ciphertext`,
		resource, subjectID, field, ct)
	return err
}

// VaultReveal returns the decrypted plaintext for a pii field, or (mask token, false)
// when no key exists (crypto-shredded) or no ciphertext is stored. Callers gate this
// behind a reveal grant; the default read path uses the mask token, not this.
func VaultReveal(ctx context.Context, conn *pgwire.Conn, resource, subjectID, field string) (string, bool, error) {
	token, ok, err := vaultKeyFor(ctx, conn, resource, subjectID, false)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return VaultMaskToken, false, nil // key shredded — undecryptable by construction
	}
	var ct string
	found, err := conn.QueryRow(ctx,
		`SELECT ciphertext FROM vault WHERE resource=$1 AND subject_id=$2 AND field=$3`,
		[]any{resource, subjectID, field}, &ct)
	if err != nil {
		return "", false, err
	}
	if !found {
		return VaultMaskToken, false, nil
	}
	pt, err := aeadOpen(token, ct)
	if err != nil {
		return VaultMaskToken, false, nil // authentication failed — fail closed
	}
	return pt, true, nil
}

// CryptoShred deletes a subject's key row and writes an attestation, in one
// transaction (ADR-10 §4 item 5). After it commits, every vault ciphertext for the
// subject is undecryptable (the key is gone) and reads return the mask token. Returns
// the attestation id and the number of key rows shredded.
func CryptoShred(ctx context.Context, conn *pgwire.Conn, resource, subjectID, shreddedBy string) (int64, int, error) {
	if err := conn.BeginSerializable(ctx); err != nil {
		return 0, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = conn.Rollback(ctx)
		}
	}()
	res, err := conn.Exec(ctx,
		`DELETE FROM vault_key WHERE resource=$1 AND subject_id=$2`, resource, subjectID)
	if err != nil {
		return 0, 0, err
	}
	n := int(res.RowsAffected)
	var attID int64
	if _, err := conn.QueryRow(ctx,
		`INSERT INTO shred_attestation (resource, subject_id, keys_shredded, shredded_by)
		 VALUES ($1,$2,$3,$4) RETURNING id`,
		[]any{resource, subjectID, n, shreddedBy}, &attID); err != nil {
		return 0, 0, err
	}
	if err := conn.Commit(ctx); err != nil {
		return 0, 0, err
	}
	committed = true
	return attID, n, nil
}

// --- AEAD (identical scheme to cek.StdAeadSeal, kernel-side) ------------------

func aeadKeyOf(token string) [32]byte { return sha256.Sum256([]byte(token)) }

func aeadGCM(token string) (cipher.AEAD, error) {
	key := aeadKeyOf(token)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func aeadSeal(token, plaintext string) (string, error) {
	gcm, err := aeadGCM(token)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ct), nil
}

func aeadOpen(token, hexCT string) (string, error) {
	gcm, err := aeadGCM(token)
	if err != nil {
		return "", err
	}
	raw, err := hex.DecodeString(hexCT)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("vault: ciphertext too short")
	}
	pt, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
