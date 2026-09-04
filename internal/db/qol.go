package db

// qol.go adds drive-style user metadata on top of the catalog: favorites and
// trash. Both are additive columns so existing databases migrate in place and
// no existing file/chunk records are touched.

import (
	"database/sql"
	"fmt"
	"time"
)

// MigrateQOL adds the favorite / trashed_at columns to the files table.
// It is idempotent and safe to call on every boot.
func (d *Database) MigrateQOL() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`
		ALTER TABLE files ADD COLUMN IF NOT EXISTS favorite INTEGER DEFAULT 0;
		ALTER TABLE files ADD COLUMN IF NOT EXISTS trashed_at BIGINT;
	`)
	return err
}

// SetFavorite marks a file (or folder) as favorite. Favorites are purely
// user metadata — storage layout is unchanged.
func (d *Database) SetFavorite(id string, fav bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := 0
	if fav {
		v = 1
	}
	_, err := d.db.Exec(`UPDATE files SET favorite = $1 WHERE id = $2`, v, id)
	return err
}

// SetTrashed marks a file as in/out of trash. NULL = live; non-NULL = the unix
// time it was trashed. Trashed files stay in their folder but are filtered out
// of normal listings.
func (d *Database) SetTrashed(id string, trashed bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var err error
	if trashed {
		_, err = d.db.Exec(`UPDATE files SET trashed_at = $1 WHERE id = $2`, time.Now().Unix(), id)
	} else {
		// $1, not $2: only one argument is bound here. With $2 the driver reports
		// "could not determine data type of parameter $1" and restore silently
		// did nothing — which was invisible while the root listing was also
		// failing to hide trashed files, because the row appeared to come back.
		_, err = d.db.Exec(`UPDATE files SET trashed_at = NULL WHERE id = $1`, id)
	}
	return err
}

// scanFileRows scans a full FileRecord row set shared by the queries below.
func (d *Database) scanFileRows(query string, args ...any) ([]FileRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []FileRecord
	for rows.Next() {
		var f FileRecord
		var isDirInt, favInt int
		var trashed sql.NullInt64
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.Path, &f.Size, &isDirInt, &f.ModTime, &f.SHA256, &f.MimeType, &f.CreatedAt, &favInt, &trashed); err != nil {
			return nil, err
		}
		f.IsDir = (isDirInt == 1)
		f.Favorite = (favInt == 1)
		if trashed.Valid {
			v := trashed.Int64
			f.TrashedAt = &v
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

const fileCols = `id, parent_id, name, path, size, is_dir, mod_time, sha256, mime_type, created_at, favorite, trashed_at`

// ListFavorites returns all favorited files, newest first.
func (d *Database) ListFavorites() ([]FileRecord, error) {
	return d.scanFileRows(`SELECT ` + fileCols + ` FROM files WHERE favorite = 1 AND trashed_at IS NULL ORDER BY mod_time DESC`)
}

// ListTrash returns everything currently in the trash.
func (d *Database) ListTrash() ([]FileRecord, error) {
	return d.scanFileRows(`SELECT ` + fileCols + ` FROM files WHERE trashed_at IS NOT NULL ORDER BY trashed_at DESC`)
}

// ListRecent returns the most recently modified files, trashed excluded.
func (d *Database) ListRecent(limit int) ([]FileRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	return d.scanFileRows(fmt.Sprintf(`SELECT %s FROM files WHERE trashed_at IS NULL AND is_dir = 0 ORDER BY mod_time DESC LIMIT %d`, fileCols, limit))
}

// SearchFiles returns files whose name contains the term (case-insensitive),
// excluding trashed items, newest first.
func (d *Database) SearchFiles(term string) ([]FileRecord, error) {
	return d.scanFileRows(`SELECT `+fileCols+` FROM files WHERE trashed_at IS NULL AND name ILIKE '%' || $1 || '%' ORDER BY mod_time DESC`, term)
}

// RenameFile updates a file's display name and path in one transaction.
func (d *Database) RenameFile(id, newName, newPath string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE files SET name = $1, path = $2, mod_time = $3 WHERE id = $4`, newName, newPath, time.Now().Unix(), id)
	return err
}

// UpdateFileParent changes a file's parent folder reference.
func (d *Database) UpdateFileParent(id, parentID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE files SET parent_id = $1 WHERE id = $2`, parentID, id)
	return err
}
