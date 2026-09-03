package db

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	StatusPending   = "PENDING"
	StatusUploading = "UPLOADING"
	StatusCompleted = "COMPLETED"
	StatusFailed    = "FAILED"
)

const (
	JobStatusActive    = "ACTIVE"
	JobStatusPaused    = "PAUSED"
	JobStatusCompleted = "COMPLETED"
	JobStatusFailed    = "FAILED"
)

type Database struct {
	db *sql.DB
	mu sync.RWMutex
}

type SettingRecord struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ChannelRecord struct {
	ID          int64  `json:"id"`
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	WebhookURL  string `json:"webhook_url"`
	GuildID     string `json:"guild_id"`
	BotToken    string `json:"bot_token"`
	IsMetadata  bool   `json:"is_metadata"`
}

type FileRecord struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	IsDir     bool   `json:"is_dir"`
	ModTime   int64  `json:"mod_time"`
	SHA256    string `json:"sha256"`
	MimeType  string `json:"mime_type"`
	CreatedAt int64  `json:"created_at"`
	Favorite  bool   `json:"favorite"`
	TrashedAt *int64 `json:"trashed_at,omitempty"`
}

type JobRecord struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	FileID          string `json:"file_id"`
	FilePath        string `json:"file_path"`
	TotalBytes      int64  `json:"total_bytes"`
	TotalChunks     int    `json:"total_chunks"`
	CompletedChunks int    `json:"completed_chunks"`
	Status          string `json:"status"`
	CreatedAt       int64  `json:"created_at"`
	CompletedAt     *int64 `json:"completed_at,omitempty"`
}

type ChunkRecord struct {
	ID            string `json:"id"`
	JobID         string `json:"job_id"`
	FileID        string `json:"file_id"`
	ChunkIndex    int    `json:"chunk_index"`
	ByteOffset    int64  `json:"byte_offset"`
	ChunkSize     int    `json:"chunk_size"`
	SHA256        string `json:"sha256"`
	GuildID       string `json:"guild_id"`
	ChannelID     string `json:"channel_id"`
	MessageID     string `json:"message_id"`
	AttachmentID  string `json:"attachment_id"`
	AttachmentURL string `json:"attachment_url"`
	Status        string `json:"status"`
	RetryCount    int    `json:"retry_count"`
	CreatedAt     int64  `json:"created_at"`
	CompletedAt   *int64 `json:"completed_at,omitempty"`
}

type BotNodeRecord struct {
	ID           string `json:"id"`
	BotToken     string `json:"bot_token"`
	BotID        string `json:"bot_id"`
	GuildID      string `json:"guild_id"`
	BotName      string `json:"bot_name"`
	GuildName    string `json:"guild_name"`
	Status       string `json:"status"`
	PingMs       int64  `json:"ping_ms"`
	ChannelCount int    `json:"channel_count"`
	StorageBytes int64  `json:"storage_bytes"`
	InviteURL    string `json:"invite_url"`
	CreatedAt    int64  `json:"created_at"`
	LastSeen     int64  `json:"last_seen"`
}

// InitDB opens a PostgreSQL database from a DSN (e.g.
// postgres://user:pass@host:port/db?sslmode=require) and ensures the schema.
func InitDB(dsn string) (*Database, error) {
	if dsn == "" {
		return nil, fmt.Errorf("database connection string is empty")
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("could not open database %w", err)
	}

	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(10 * time.Minute)

	d := &Database{db: sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("database setup failed %w", err)
	}

	return d, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS channels (
		id BIGSERIAL PRIMARY KEY,
		channel_id TEXT UNIQUE NOT NULL,
		channel_name TEXT NOT NULL,
		webhook_url TEXT,
		guild_id TEXT DEFAULT '',
		bot_token TEXT DEFAULT '',
		category_id TEXT DEFAULT '',
		is_metadata INTEGER DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS files (
		id TEXT PRIMARY KEY,
		parent_id TEXT,
		name TEXT NOT NULL,
		path TEXT UNIQUE NOT NULL,
		size BIGINT NOT NULL,
		is_dir INTEGER DEFAULT 0,
		mod_time BIGINT NOT NULL,
		sha256 TEXT NOT NULL,
		mime_type TEXT,
		created_at BIGINT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS jobs (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		file_id TEXT NOT NULL,
		file_path TEXT NOT NULL,
		total_bytes BIGINT NOT NULL,
		total_chunks INTEGER NOT NULL,
		completed_chunks INTEGER DEFAULT 0,
		status TEXT NOT NULL,
		created_at BIGINT NOT NULL,
		completed_at BIGINT
	);

	CREATE TABLE IF NOT EXISTS chunks (
		id TEXT PRIMARY KEY,
		job_id TEXT NOT NULL,
		file_id TEXT NOT NULL,
		chunk_index INTEGER NOT NULL,
		byte_offset BIGINT NOT NULL,
		chunk_size INTEGER NOT NULL,
		sha256 TEXT NOT NULL,
		guild_id TEXT DEFAULT '',
		channel_id TEXT,
		message_id TEXT,
		attachment_id TEXT,
		attachment_url TEXT,
		status TEXT NOT NULL,
		retry_count INTEGER DEFAULT 0,
		created_at BIGINT NOT NULL,
		completed_at BIGINT,
		CONSTRAINT fk_chunks_job FOREIGN KEY(job_id) REFERENCES jobs(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS bot_nodes (
		id TEXT PRIMARY KEY,
		bot_token TEXT NOT NULL,
		guild_id TEXT NOT NULL,
		bot_name TEXT,
		guild_name TEXT,
		status TEXT DEFAULT 'Active',
		ping_ms BIGINT DEFAULT 0,
		channel_count INTEGER DEFAULT 0,
		storage_bytes BIGINT DEFAULT 0,
		created_at BIGINT NOT NULL,
		last_seen BIGINT DEFAULT 0,
		bot_id TEXT DEFAULT '',
		invite_url TEXT DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS shares (
		token_hash TEXT PRIMARY KEY,
		file_id TEXT NOT NULL,
		expires_at BIGINT NOT NULL,
		created_at BIGINT NOT NULL,
		downloads BIGINT DEFAULT 0
	);
	`
	if _, err := d.db.Exec(schema); err != nil {
		return err
	}

	// Ensure newer columns exist (idempotent for upgrades).
	_, _ = d.db.Exec("ALTER TABLE channels ADD COLUMN IF NOT EXISTS category_id TEXT DEFAULT ''")

	indexes := `
	CREATE INDEX IF NOT EXISTS idx_files_parent ON files(parent_id);
	CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);
	CREATE INDEX IF NOT EXISTS idx_chunks_job ON chunks(job_id);
	CREATE INDEX IF NOT EXISTS idx_chunks_file ON chunks(file_id);
	CREATE INDEX IF NOT EXISTS idx_chunks_status ON chunks(status);
	CREATE INDEX IF NOT EXISTS idx_chunks_guild ON chunks(guild_id);
	CREATE INDEX IF NOT EXISTS idx_channels_guild ON channels(guild_id);
	CREATE INDEX IF NOT EXISTS idx_channels_category ON channels(category_id);
	CREATE INDEX IF NOT EXISTS idx_chunks_sha256 ON chunks(sha256);
	CREATE INDEX IF NOT EXISTS idx_chunks_file_idx ON chunks(file_id, chunk_index);
	CREATE INDEX IF NOT EXISTS idx_shares_file ON shares(file_id);
	`
	if _, err := d.db.Exec(indexes); err != nil {
		return err
	}

	_, _ = d.db.Exec("UPDATE settings SET value = '7864320' WHERE key = 'chunk_size_bytes' AND (value = '20971520' OR value = '20000000' OR value = '')")
	_, _ = d.db.Exec("UPDATE files SET parent_id = '' WHERE parent_id != '' AND parent_id NOT IN (SELECT id FROM files WHERE is_dir = 1)")
	if err := d.MigrateQOL(); err != nil {
		return err
	}
	return nil
}

func (d *Database) ResetIncomplete() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE chunks SET status = $1 WHERE status = $2`, StatusPending, StatusUploading)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		UPDATE jobs
		SET completed_chunks = (
			SELECT COUNT(*) FROM chunks WHERE chunks.job_id = jobs.id AND chunks.status = $1
		)
		WHERE status = $2
	`, StatusCompleted, JobStatusActive)

	return err
}

func (d *Database) SetSetting(key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

func (d *Database) GetSetting(key string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var val string
	err := d.db.QueryRow(`SELECT value FROM settings WHERE key = $1`, key).Scan(&val)
	if err != nil {
		return "", err
	}
	return val, nil
}

func (d *Database) GetAllSettings() (map[string]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		settings[k] = v
	}
	return settings, nil
}

func (d *Database) ClearChannels() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM channels`)
	return err
}

func (d *Database) ClearChannelsForGuild(guildID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if guildID == "" {
		_, err := d.db.Exec(`DELETE FROM channels`)
		return err
	}
	_, err := d.db.Exec(`DELETE FROM channels WHERE guild_id = $1`, guildID)
	return err
}

func (d *Database) AddChannel(channelID, channelName, webhookURL string, isMetadata bool) error {
	return d.AddChannelWithGuild(channelID, channelName, webhookURL, "", "", isMetadata)
}

func (d *Database) AddChannelWithGuild(channelID, channelName, webhookURL, guildID, botToken string, isMetadata bool) error {
	return d.AddChannelFull(channelID, channelName, webhookURL, guildID, botToken, "", isMetadata)
}

// AddChannelFull records a storage channel with its parent category id.
func (d *Database) AddChannelFull(channelID, channelName, webhookURL, guildID, botToken, categoryID string, isMetadata bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	isMetaInt := 0
	if isMetadata {
		isMetaInt = 1
	}

	_, err := d.db.Exec(`
		INSERT INTO channels (channel_id, channel_name, webhook_url, guild_id, bot_token, category_id, is_metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(channel_id) DO UPDATE SET
			channel_name = EXCLUDED.channel_name,
			webhook_url = EXCLUDED.webhook_url,
			guild_id = EXCLUDED.guild_id,
			bot_token = EXCLUDED.bot_token,
			category_id = EXCLUDED.category_id,
			is_metadata = EXCLUDED.is_metadata
	`, channelID, channelName, webhookURL, guildID, botToken, categoryID, isMetaInt)
	return err
}

func (d *Database) GetChannelsByGuild(guildID string) ([]ChannelRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `SELECT id, channel_id, channel_name, webhook_url, COALESCE(guild_id, ''), COALESCE(bot_token, ''), is_metadata FROM channels WHERE guild_id = $1 ORDER BY id ASC`
	rows, err := d.db.Query(query, guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChannelRecord
	for rows.Next() {
		var c ChannelRecord
		var isMeta int
		if err := rows.Scan(&c.ID, &c.ChannelID, &c.ChannelName, &c.WebhookURL, &c.GuildID, &c.BotToken, &isMeta); err != nil {
			return nil, err
		}
		c.IsMetadata = (isMeta == 1)
		list = append(list, c)
	}
	return list, nil
}

func (d *Database) GetStorageChannels() ([]ChannelRecord, error) {
	return d.GetStorageChannelsForGuild("")
}

func (d *Database) GetStorageChannelsForGuild(guildID string) ([]ChannelRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `SELECT id, channel_id, channel_name, webhook_url, COALESCE(guild_id, ''), COALESCE(bot_token, ''), is_metadata FROM channels WHERE is_metadata = 0`
	var args []any
	if guildID != "" {
		query += ` AND guild_id = $1`
		args = append(args, guildID)
	}
	query += ` ORDER BY id ASC`

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChannelRecord
	for rows.Next() {
		var c ChannelRecord
		var isMeta int
		if err := rows.Scan(&c.ID, &c.ChannelID, &c.ChannelName, &c.WebhookURL, &c.GuildID, &c.BotToken, &isMeta); err != nil {
			return nil, err
		}
		c.IsMetadata = (isMeta == 1)
		list = append(list, c)
	}
	return list, nil
}

func (d *Database) GetMetadataChannel() (*ChannelRecord, error) {
	return d.GetMetadataChannelForGuild("")
}

func (d *Database) GetMetadataChannelForGuild(guildID string) (*ChannelRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `SELECT id, channel_id, channel_name, webhook_url, COALESCE(guild_id, ''), COALESCE(bot_token, ''), is_metadata FROM channels WHERE is_metadata = 1`
	var args []any
	if guildID != "" {
		query += ` AND guild_id = $1`
		args = append(args, guildID)
	}
	query += ` LIMIT 1`

	var c ChannelRecord
	var isMeta int
	err := d.db.QueryRow(query, args...).
		Scan(&c.ID, &c.ChannelID, &c.ChannelName, &c.WebhookURL, &c.GuildID, &c.BotToken, &isMeta)
	if err != nil {
		return nil, err
	}
	c.IsMetadata = (isMeta == 1)
	return &c, nil
}

func (d *Database) UpsertFile(f *FileRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	isDirInt := 0
	if f.IsDir {
		isDirInt = 1
	}

	_, err := d.db.Exec(`
		INSERT INTO files (id, parent_id, name, path, size, is_dir, mod_time, sha256, mime_type, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT(path) DO UPDATE SET
			parent_id = EXCLUDED.parent_id,
			name = EXCLUDED.name,
			size = EXCLUDED.size,
			is_dir = EXCLUDED.is_dir,
			mod_time = EXCLUDED.mod_time,
			sha256 = EXCLUDED.sha256,
			mime_type = EXCLUDED.mime_type
	`, f.ID, f.ParentID, f.Name, f.Path, f.Size, isDirInt, f.ModTime, f.SHA256, f.MimeType, f.CreatedAt)
	return err
}

func (d *Database) GetFile(id string) (*FileRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var f FileRecord
	var isDirInt, favInt int
	var trashed sql.NullInt64
	err := d.db.QueryRow(`SELECT id, parent_id, name, path, size, is_dir, mod_time, sha256, mime_type, created_at, favorite, trashed_at FROM files WHERE id = $1`, id).
		Scan(&f.ID, &f.ParentID, &f.Name, &f.Path, &f.Size, &isDirInt, &f.ModTime, &f.SHA256, &f.MimeType, &f.CreatedAt, &favInt, &trashed)
	if err != nil {
		return nil, err
	}
	f.IsDir = (isDirInt == 1)
	f.Favorite = (favInt == 1)
	if trashed.Valid {
		v := trashed.Int64
		f.TrashedAt = &v
	}
	return &f, nil
}

func (d *Database) GetFileByPath(path string) (*FileRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var f FileRecord
	var isDirInt, favInt int
	var trashed sql.NullInt64
	err := d.db.QueryRow(`SELECT id, parent_id, name, path, size, is_dir, mod_time, sha256, mime_type, created_at, favorite, trashed_at FROM files WHERE path = $1`, path).
		Scan(&f.ID, &f.ParentID, &f.Name, &f.Path, &f.Size, &isDirInt, &f.ModTime, &f.SHA256, &f.MimeType, &f.CreatedAt, &favInt, &trashed)
	if err != nil {
		return nil, err
	}
	f.IsDir = (isDirInt == 1)
	f.Favorite = (favInt == 1)
	if trashed.Valid {
		v := trashed.Int64
		f.TrashedAt = &v
	}
	return &f, nil
}

func (d *Database) ListFiles(parentID string) ([]FileRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var query string
	var args []any
	if parentID == "" {
		query = `SELECT id, parent_id, name, path, size, is_dir, mod_time, sha256, mime_type, created_at, favorite, trashed_at FROM files WHERE parent_id = '' OR parent_id IS NULL OR parent_id NOT IN (SELECT id FROM files WHERE is_dir = 1) AND trashed_at IS NULL ORDER BY is_dir DESC, name ASC`
	} else {
		query = `SELECT id, parent_id, name, path, size, is_dir, mod_time, sha256, mime_type, created_at, favorite, trashed_at FROM files WHERE parent_id = $1 AND trashed_at IS NULL ORDER BY is_dir DESC, name ASC`
		args = append(args, parentID)
	}

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
	return list, nil
}

func (d *Database) GetAllFiles() ([]FileRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, parent_id, name, path, size, is_dir, mod_time, sha256, mime_type, created_at, favorite, trashed_at FROM files ORDER BY path ASC`)
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
	return list, nil
}

func (d *Database) GetTotalStorageBytes() int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var total sql.NullInt64
	_ = d.db.QueryRow(`SELECT SUM(size) FROM files WHERE is_dir = 0`).Scan(&total)
	if total.Valid {
		return total.Int64
	}
	return 0
}

func (d *Database) GuildStorageBytes(guildID string) int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if guildID == "" {
		var total sql.NullInt64
		_ = d.db.QueryRow(`SELECT SUM(size) FROM files WHERE is_dir = 0`).Scan(&total)
		if total.Valid {
			return total.Int64
		}
		return 0
	}
	var total sql.NullInt64
	_ = d.db.QueryRow(`SELECT SUM(chunk_size) FROM chunks WHERE guild_id = $1 AND status = $2`, guildID, StatusCompleted).Scan(&total)
	if total.Valid {
		return total.Int64
	}
	return 0
}

func (d *Database) GetFileCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var count int
	_ = d.db.QueryRow(`SELECT COUNT(*) FROM files WHERE is_dir = 0`).Scan(&count)
	return count
}

func (d *Database) DeleteFile(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`DELETE FROM files WHERE id = $1`, id)
	if err != nil {
		return err
	}
	_, _ = d.db.Exec(`DELETE FROM chunks WHERE file_id = $1`, id)
	return nil
}

func (d *Database) CreateJob(j *JobRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`
		INSERT INTO jobs (id, type, file_id, file_path, total_bytes, total_chunks, completed_chunks, status, created_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, j.ID, j.Type, j.FileID, j.FilePath, j.TotalBytes, j.TotalChunks, j.CompletedChunks, j.Status, j.CreatedAt, j.CompletedAt)
	return err
}

func (d *Database) UpdateJobStatus(jobID string, status string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	var completedAt *int64
	if status == JobStatusCompleted || status == JobStatusFailed {
		now := time.Now().Unix()
		completedAt = &now
	}

	_, err := d.db.Exec(`UPDATE jobs SET status = $1, completed_at = $2 WHERE id = $3`, status, completedAt, jobID)
	return err
}

func (d *Database) IncrementJobCompletedChunks(jobID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE jobs SET completed_chunks = completed_chunks + 1 WHERE id = $1`, jobID)
	return err
}

func (d *Database) GetJob(jobID string) (*JobRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var j JobRecord
	err := d.db.QueryRow(`SELECT id, type, file_id, file_path, total_bytes, total_chunks, completed_chunks, status, created_at, completed_at FROM jobs WHERE id = $1`, jobID).
		Scan(&j.ID, &j.Type, &j.FileID, &j.FilePath, &j.TotalBytes, &j.TotalChunks, &j.CompletedChunks, &j.Status, &j.CreatedAt, &j.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (d *Database) GetActiveJobs() ([]JobRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, type, file_id, file_path, total_bytes, total_chunks, completed_chunks, status, created_at, completed_at FROM jobs WHERE status = $1 ORDER BY created_at DESC`, JobStatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []JobRecord
	for rows.Next() {
		var j JobRecord
		if err := rows.Scan(&j.ID, &j.Type, &j.FileID, &j.FilePath, &j.TotalBytes, &j.TotalChunks, &j.CompletedChunks, &j.Status, &j.CreatedAt, &j.CompletedAt); err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, nil
}

func (d *Database) CreateChunk(c *ChunkRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`
		INSERT INTO chunks (id, job_id, file_id, chunk_index, byte_offset, chunk_size, sha256, guild_id, channel_id, message_id, attachment_id, attachment_url, status, retry_count, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`, c.ID, c.JobID, c.FileID, c.ChunkIndex, c.ByteOffset, c.ChunkSize, c.SHA256, c.GuildID, c.ChannelID, c.MessageID, c.AttachmentID, c.AttachmentURL, c.Status, c.RetryCount, c.CreatedAt)
	return err
}

func (d *Database) UpdateChunkStatus(chunkID string, status string, channelID, messageID, attachmentID, attachmentURL string) error {
	return d.UpdateChunkWithGuild(chunkID, status, "", channelID, messageID, attachmentID, attachmentURL)
}

func (d *Database) UpdateChunkWithGuild(chunkID string, status string, guildID, channelID, messageID, attachmentID, attachmentURL string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	var completedAt *int64
	if status == StatusCompleted {
		now := time.Now().Unix()
		completedAt = &now
	}

	query := `
		UPDATE chunks
		SET status = $1, channel_id = $2, message_id = $3, attachment_id = $4, attachment_url = $5, completed_at = $6`
	var args []any
	args = append(args, status, channelID, messageID, attachmentID, attachmentURL, completedAt)

	if guildID != "" {
		query += `, guild_id = $7`
		args = append(args, guildID)
	}

	query += ` WHERE id = $8`
	args = append(args, chunkID)

	_, err := d.db.Exec(query, args...)
	return err
}

func (d *Database) MarkChunkFailed(chunkID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`UPDATE chunks SET status = $1, retry_count = retry_count + 1 WHERE id = $2`, StatusFailed, chunkID)
	return err
}

// UpdateChunkComplete writes the completed chunk state (status, location ids,
// sha256) in a single atomic statement so a partial failure can never leave a
// chunk half-written (e.g. sha set but status PENDING).
func (d *Database) UpdateChunkComplete(chunkID, guildID, channelID, messageID, attachmentID, attachmentURL, sha256 string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	completedAt := time.Now().Unix()
	_, err := d.db.Exec(`
		UPDATE chunks
		SET status = $1, channel_id = $2, message_id = $3, attachment_id = $4,
		    attachment_url = $5, completed_at = $6, sha256 = $7, guild_id = $8
		WHERE id = $9`,
		StatusCompleted, channelID, messageID, attachmentID, attachmentURL, completedAt, sha256, guildID, chunkID)
	return err
}

// CountCompletedChunksForFile returns how many completed chunk copies exist
// for a file (across all guilds).
func (d *Database) CountCompletedChunksForFile(fileID string) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE file_id = $1 AND status = $2`, fileID, StatusCompleted).Scan(&n)
	return n, err
}

// CountCompletedChunksForJob returns how many chunk copies of a single upload
// job reached COMPLETED across all target servers.
func (d *Database) CountCompletedChunksForJob(jobID string) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE job_id = $1 AND status = $2`, jobID, StatusCompleted).Scan(&n)
	return n, err
}

func (d *Database) GetChunksForJob(jobID string) ([]ChunkRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT id, job_id, file_id, chunk_index, byte_offset, chunk_size, sha256, COALESCE(guild_id, ''), channel_id, message_id, attachment_id, attachment_url, status, retry_count, created_at, completed_at
		FROM chunks WHERE job_id = $1 ORDER BY chunk_index ASC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChunkRecord
	for rows.Next() {
		var c ChunkRecord
		var gID, chID, msgID, attID, attURL sql.NullString
		if err := rows.Scan(&c.ID, &c.JobID, &c.FileID, &c.ChunkIndex, &c.ByteOffset, &c.ChunkSize, &c.SHA256, &gID, &chID, &msgID, &attID, &attURL, &c.Status, &c.RetryCount, &c.CreatedAt, &c.CompletedAt); err != nil {
			return nil, err
		}
		c.GuildID = gID.String
		c.ChannelID = chID.String
		c.MessageID = msgID.String
		c.AttachmentID = attID.String
		c.AttachmentURL = attURL.String
		list = append(list, c)
	}
	return list, nil
}

func (d *Database) GetChunksForFile(fileID string) ([]ChunkRecord, error) {
	return d.GetChunksForFileAndGuild(fileID, "")
}

func (d *Database) GetAllChunksForFile(fileID string) ([]ChunkRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT id, job_id, file_id, chunk_index, byte_offset, chunk_size, sha256, COALESCE(guild_id, ''), channel_id, message_id, attachment_id, attachment_url, status, retry_count, created_at, completed_at
		FROM chunks WHERE file_id = $1 ORDER BY chunk_index ASC
	`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChunkRecord
	for rows.Next() {
		var c ChunkRecord
		var gID, chID, msgID, attID, attURL sql.NullString
		if err := rows.Scan(&c.ID, &c.JobID, &c.FileID, &c.ChunkIndex, &c.ByteOffset, &c.ChunkSize, &c.SHA256, &gID, &chID, &msgID, &attID, &attURL, &c.Status, &c.RetryCount, &c.CreatedAt, &c.CompletedAt); err != nil {
			return nil, err
		}
		c.GuildID = gID.String
		c.ChannelID = chID.String
		c.MessageID = msgID.String
		c.AttachmentID = attID.String
		c.AttachmentURL = attURL.String
		list = append(list, c)
	}
	return list, nil
}

func (d *Database) GetChunksForFileAndGuild(fileID, guildID string) ([]ChunkRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `
		SELECT id, job_id, file_id, chunk_index, byte_offset, chunk_size, sha256, COALESCE(guild_id, ''), channel_id, message_id, attachment_id, attachment_url, status, retry_count, created_at, completed_at
		FROM chunks WHERE file_id = $1 AND status = $2`
	var args []any
	args = append(args, fileID, StatusCompleted)

	if guildID != "" {
		query += ` AND guild_id = $3`
		args = append(args, guildID)
	}
	query += ` ORDER BY chunk_index ASC`

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChunkRecord
	for rows.Next() {
		var c ChunkRecord
		var gID, chID, msgID, attID, attURL sql.NullString
		if err := rows.Scan(&c.ID, &c.JobID, &c.FileID, &c.ChunkIndex, &c.ByteOffset, &c.ChunkSize, &c.SHA256, &gID, &chID, &msgID, &attID, &attURL, &c.Status, &c.RetryCount, &c.CreatedAt, &c.CompletedAt); err != nil {
			return nil, err
		}
		c.GuildID = gID.String
		c.ChannelID = chID.String
		c.MessageID = msgID.String
		c.AttachmentID = attID.String
		c.AttachmentURL = attURL.String
		list = append(list, c)
	}
	return list, nil
}

func (d *Database) GetAvailableGuildsForFile(fileID string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT DISTINCT guild_id FROM chunks WHERE file_id = $1 AND status = $2 AND guild_id != ''`, fileID, StatusCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var guilds []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err == nil && g != "" {
			guilds = append(guilds, g)
		}
	}
	return guilds, nil
}

func (d *Database) GetPendingChunks(jobID string) ([]ChunkRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT id, job_id, file_id, chunk_index, byte_offset, chunk_size, sha256, COALESCE(guild_id, ''), channel_id, message_id, attachment_id, attachment_url, status, retry_count, created_at, completed_at
		FROM chunks WHERE job_id = $1 AND status != $2 ORDER BY chunk_index ASC
	`, jobID, StatusCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChunkRecord
	for rows.Next() {
		var c ChunkRecord
		var gID, chID, msgID, attID, attURL sql.NullString
		if err := rows.Scan(&c.ID, &c.JobID, &c.FileID, &c.ChunkIndex, &c.ByteOffset, &c.ChunkSize, &c.SHA256, &gID, &chID, &msgID, &attID, &attURL, &c.Status, &c.RetryCount, &c.CreatedAt, &c.CompletedAt); err != nil {
			return nil, err
		}
		c.GuildID = gID.String
		c.ChannelID = chID.String
		c.MessageID = msgID.String
		c.AttachmentID = attID.String
		c.AttachmentURL = attURL.String
		list = append(list, c)
	}
	return list, nil
}

func (d *Database) FindChunkByHash(sha256Hash string) (*ChunkRecord, error) {
	if sha256Hash == "" {
		return nil, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	var c ChunkRecord
	var gID, chID, msgID, attID, attURL sql.NullString
	err := d.db.QueryRow(`
		SELECT id, job_id, file_id, chunk_index, byte_offset, chunk_size, sha256, COALESCE(guild_id, ''), channel_id, message_id, attachment_id, attachment_url, status, retry_count, created_at, completed_at
		FROM chunks
		WHERE sha256 = $1 AND status = $2 AND message_id IS NOT NULL AND message_id != ''
		ORDER BY created_at DESC LIMIT 1
	`, sha256Hash, StatusCompleted).Scan(
		&c.ID, &c.JobID, &c.FileID, &c.ChunkIndex, &c.ByteOffset, &c.ChunkSize, &c.SHA256,
		&gID, &chID, &msgID, &attID, &attURL, &c.Status,
		&c.RetryCount, &c.CreatedAt, &c.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	c.GuildID = gID.String
	c.ChannelID = chID.String
	c.MessageID = msgID.String
	c.AttachmentID = attID.String
	c.AttachmentURL = attURL.String
	return &c, nil
}

func (d *Database) FindChunksByHash(sha256Hash string) ([]ChunkRecord, error) {
	if sha256Hash == "" {
		return nil, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT id, job_id, file_id, chunk_index, byte_offset, chunk_size, sha256, COALESCE(guild_id, ''), channel_id, message_id, attachment_id, attachment_url, status, retry_count, created_at, completed_at
		FROM chunks
		WHERE sha256 = $1 AND status = $2 AND message_id IS NOT NULL AND message_id != ''
		ORDER BY created_at DESC
	`, sha256Hash, StatusCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ChunkRecord
	for rows.Next() {
		var c ChunkRecord
		var gID, chID, msgID, attID, attURL sql.NullString
		if err := rows.Scan(
			&c.ID, &c.JobID, &c.FileID, &c.ChunkIndex, &c.ByteOffset, &c.ChunkSize, &c.SHA256,
			&gID, &chID, &msgID, &attID, &attURL, &c.Status,
			&c.RetryCount, &c.CreatedAt, &c.CompletedAt,
		); err == nil {
			c.GuildID = gID.String
			c.ChannelID = chID.String
			c.MessageID = msgID.String
			c.AttachmentID = attID.String
			c.AttachmentURL = attURL.String
			list = append(list, c)
		}
	}
	return list, nil
}

func (d *Database) UpdateChunkHash(chunkID string, sha256Hash string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`UPDATE chunks SET sha256 = $1 WHERE id = $2`, sha256Hash, chunkID)
	return err
}

func (d *Database) UpsertBotNode(n *BotNodeRecord) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.Exec(`
		INSERT INTO bot_nodes (id, bot_token, guild_id, bot_name, guild_name, status, ping_ms, channel_count, storage_bytes, created_at, last_seen, bot_id, invite_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT(id) DO UPDATE SET
			bot_token = EXCLUDED.bot_token,
			guild_id = EXCLUDED.guild_id,
			bot_name = EXCLUDED.bot_name,
			guild_name = EXCLUDED.guild_name,
			status = EXCLUDED.status,
			ping_ms = EXCLUDED.ping_ms,
			channel_count = EXCLUDED.channel_count,
			storage_bytes = EXCLUDED.storage_bytes,
			last_seen = EXCLUDED.last_seen,
			bot_id = EXCLUDED.bot_id,
			invite_url = EXCLUDED.invite_url
	`, n.ID, n.BotToken, n.GuildID, n.BotName, n.GuildName, n.Status, n.PingMs, n.ChannelCount, n.StorageBytes, n.CreatedAt, n.LastSeen, n.BotID, n.InviteURL)
	return err
}

func (d *Database) GetAllBotNodes() ([]BotNodeRecord, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT id, bot_token, guild_id, bot_name, guild_name, status, ping_ms, channel_count, storage_bytes, created_at, last_seen, bot_id, invite_url FROM bot_nodes ORDER BY created_at ASC`)
	if err != nil {
		return make([]BotNodeRecord, 0), err
	}
	defer rows.Close()

	list := make([]BotNodeRecord, 0)
	for rows.Next() {
		var n BotNodeRecord
		var bName, gName, status, bID, invURL sql.NullString
		if err := rows.Scan(&n.ID, &n.BotToken, &n.GuildID, &bName, &gName, &status, &n.PingMs, &n.ChannelCount, &n.StorageBytes, &n.CreatedAt, &n.LastSeen, &bID, &invURL); err != nil {
			return list, err
		}
		n.BotName = bName.String
		n.GuildName = gName.String
		n.Status = status.String
		n.BotID = bID.String
		n.InviteURL = invURL.String
		list = append(list, n)
	}
	return list, nil
}

func (d *Database) GetUniqueBotTokens() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`SELECT DISTINCT bot_token FROM bot_nodes WHERE bot_token != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []string
	seen := make(map[string]bool)
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err == nil && tok != "" && !seen[tok] {
			seen[tok] = true
			tokens = append(tokens, tok)
		}
	}
	return tokens, nil
}

func (d *Database) DeleteBotNode(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM bot_nodes WHERE id = $1`, id)
	return err
}

func (d *Database) DeleteBotNodesByToken(botToken string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`DELETE FROM bot_nodes WHERE bot_token = $1`, botToken)
	return err
}
