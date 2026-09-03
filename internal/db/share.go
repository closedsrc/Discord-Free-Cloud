package db

import (
	"database/sql"
	"time"
)

type ShareRecord struct {
	TokenHash string `json:"-"`
	FileID    string `json:"file_id"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
	Downloads int64  `json:"downloads"`
}

func (d *Database) CreateShare(tokenHash, fileID string, expiresAt int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(
		`INSERT INTO shares (token_hash, file_id, expires_at, created_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, fileID, expiresAt, time.Now().Unix(),
	)
	return err
}

func (d *Database) GetShare(tokenHash string) (*ShareRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var s ShareRecord
	err := d.db.QueryRow(
		`SELECT token_hash, file_id, expires_at, created_at, downloads FROM shares WHERE token_hash = $1`,
		tokenHash,
	).Scan(&s.TokenHash, &s.FileID, &s.ExpiresAt, &s.CreatedAt, &s.Downloads)
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

func (d *Database) DeleteShare(tokenHash string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM shares WHERE token_hash = $1`, tokenHash)
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
	rows, err := d.db.Query(
		`SELECT token_hash, file_id, expires_at, created_at, downloads FROM shares WHERE file_id = $1 ORDER BY created_at DESC`,
		fileID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShareRecord
	for rows.Next() {
		var s ShareRecord
		if err := rows.Scan(&s.TokenHash, &s.FileID, &s.ExpiresAt, &s.CreatedAt, &s.Downloads); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
