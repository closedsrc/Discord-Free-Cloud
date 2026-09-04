package db

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"
)

// newUUIDv4 mints a random share id without a dependency — the db package must
// stay dependency-light (the rest of the tree uses google/uuid, but share ids
// are the only thing this package needs one for).
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

type ShareRecord struct {
	// ID is the public handle of the share (a UUIDv4). It is what the UI sends
	// back to revoke a link. TokenHash is the secret-material storage form and
	// must never reach JSON — revoke-by-hash would turn "list my links" into a
	// secret-recovery oracle for any link ever created.
	ID        string `json:"id"`
	TokenHash string `json:"-"`
	FileID    string `json:"file_id"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
	Downloads int64  `json:"downloads"`
}

func (d *Database) CreateShare(id, tokenHash, fileID string, expiresAt int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(
		`INSERT INTO shares (id, token_hash, file_id, expires_at, created_at) VALUES ($1, $2, $3, $4, $5)`,
		id, tokenHash, fileID, expiresAt, time.Now().Unix(),
	)
	return err
}

// migrateSharesID adds the public id column to installs created before it
// existed. Existing rows get a random handle so every old link stays revocable
// through the same opaque-id path as new ones.
func (d *Database) migrateSharesID() error {
	if _, err := d.db.Exec(`ALTER TABLE shares ADD COLUMN IF NOT EXISTS id TEXT`); err != nil {
		return err
	}
	rows, err := d.db.Query(`SELECT token_hash FROM shares WHERE id IS NULL OR id = ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return err
		}
		if h != "" {
			missing = append(missing, h)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, h := range missing {
		id, err := newUUIDv4()
		if err != nil {
			return err
		}
		if _, err := d.db.Exec(`UPDATE shares SET id = $1 WHERE token_hash = $2`, id, h); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) GetShare(tokenHash string) (*ShareRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var s ShareRecord
	err := d.db.QueryRow(
		`SELECT id, token_hash, file_id, expires_at, created_at, downloads FROM shares WHERE token_hash = $1`,
		tokenHash,
	).Scan(&s.ID, &s.TokenHash, &s.FileID, &s.ExpiresAt, &s.CreatedAt, &s.Downloads)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetShareByID resolves the public opaque handle to the row. It is the only
// lookup the revoke path may use.
func (d *Database) GetShareByID(id string) (*ShareRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var s ShareRecord
	err := d.db.QueryRow(
		`SELECT id, token_hash, file_id, expires_at, created_at, downloads FROM shares WHERE id = $1`,
		id,
	).Scan(&s.ID, &s.TokenHash, &s.FileID, &s.ExpiresAt, &s.CreatedAt, &s.Downloads)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *Database) TouchShare(tokenHash string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE shares SET downloads = downloads + 1 WHERE token_hash = $1`, tokenHash)
	return err
}

func (d *Database) DeleteShareByID(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM shares WHERE id = $1`, id)
	return err
}

func (d *Database) DeleteSharesForFile(fileID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM shares WHERE file_id = $1`, fileID)
	return err
}

func (d *Database) ListSharesForFile(fileID string) ([]ShareRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.listShares(`WHERE file_id = $1 ORDER BY created_at DESC`, fileID)
}

// ListAllShares returns every share, newest first, with the file name joined in
// so the Links view can render one row per share without an N+1 fan-out: the old
// client walked the whole tree and called /api/shares/list for every file, which
// meant 2× files requests (and >30s wall time) on a library this size. It takes
// `now` from the caller so tests can freeze "expired" deterministically.
func (d *Database) ListAllShares(now int64) ([]struct {
	ShareRecord
	FileName string `json:"file_name"`
	Expired  bool   `json:"expired"`
}, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.db.Query(
		`SELECT s.id, s.token_hash, s.file_id, s.expires_at, s.created_at, s.downloads,
		        COALESCE(f.name, '') FROM shares s
		 LEFT JOIN files f ON f.id = s.file_id ORDER BY s.created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		ShareRecord
		FileName string `json:"file_name"`
		Expired  bool   `json:"expired"`
	}
	for rows.Next() {
		var e struct {
			ShareRecord
			FileName string `json:"file_name"`
			Expired  bool   `json:"expired"`
		}
		if err := rows.Scan(&e.ID, &e.TokenHash, &e.FileID, &e.ExpiresAt, &e.CreatedAt, &e.Downloads, &e.FileName); err != nil {
			return nil, err
		}
		e.Expired = now > e.ExpiresAt
		e.TokenHash = "" // never ship secret material; ID is the public handle
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *Database) listShares(where string, args ...any) ([]ShareRecord, error) {
	rows, err := d.db.Query(
		`SELECT id, token_hash, file_id, expires_at, created_at, downloads FROM shares `+where,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShareRecord
	for rows.Next() {
		var s ShareRecord
		if err := rows.Scan(&s.ID, &s.TokenHash, &s.FileID, &s.ExpiresAt, &s.CreatedAt, &s.Downloads); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
