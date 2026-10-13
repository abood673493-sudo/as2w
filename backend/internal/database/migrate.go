package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sshmp/ssh-manager-panel/migrations"
)

// Direction selects whether migrations are applied or reverted.
type Direction string

// Supported migration directions.
const (
	DirectionUp   Direction = "up"
	DirectionDown Direction = "down"
)

// advisoryLockKey is an arbitrary but stable 64-bit key: only one panel process
// may migrate a database at a time.
const advisoryLockKey int64 = 0x5353484D5031

var migrationFilePattern = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_]+)\.(up|down)\.sql$`)

// Migration is a single versioned schema change.
type Migration struct {
	Version  string
	Name     string
	UpSQL    string
	DownSQL  string
	Checksum string // sha256 of UpSQL, recorded so edits are detected
}

// MigrationStatus describes one migration relative to the live database.
type MigrationStatus struct {
	Version         string
	Name            string
	Applied         bool
	AppliedAt       string
	ChecksumMatches bool
	HasDownScript   bool
}

type appliedMigrationRecord struct {
	Version   string
	Name      string
	Checksum  string
	AppliedAt time.Time
}

// LoadMigrations reads and validates every migration embedded in the binary.
func LoadMigrations() ([]Migration, error) {
	return loadMigrationsFrom(migrations.FS)
}

func loadMigrationsFrom(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}

	byVersion := make(map[string]*Migration)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		matches := migrationFilePattern.FindStringSubmatch(name)
		if matches == nil {
			return nil, fmt.Errorf("migration %q does not match <version>_<name>.(up|down).sql", name)
		}
		version, migName, direction := matches[1], matches[2], matches[3]

		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		if strings.TrimSpace(string(content)) == "" {
			return nil, fmt.Errorf("migration %q is empty", name)
		}

		mig, ok := byVersion[version]
		if !ok {
			mig = &Migration{Version: version, Name: migName}
			byVersion[version] = mig
		}
		if mig.Name != migName {
			return nil, fmt.Errorf("migration version %s is used by both %q and %q", version, mig.Name, migName)
		}
		switch direction {
		case "up":
			if mig.UpSQL != "" {
				return nil, fmt.Errorf("migration version %s has more than one up script", version)
			}
			mig.UpSQL = string(content)
		case "down":
			if mig.DownSQL != "" {
				return nil, fmt.Errorf("migration version %s has more than one down script", version)
			}
			mig.DownSQL = string(content)
		}
	}

	out := make([]Migration, 0, len(byVersion))
	for _, mig := range byVersion {
		if strings.TrimSpace(mig.UpSQL) == "" {
			return nil, fmt.Errorf("migration %s_%s is missing its up script", mig.Version, mig.Name)
		}
		sum := sha256.Sum256([]byte(mig.UpSQL))
		mig.Checksum = hex.EncodeToString(sum[:])
		out = append(out, *mig)
	}

	sort.Slice(out, func(i, j int) bool {
		if cmp := compareVersions(out[i].Version, out[j].Version); cmp != 0 {
			return cmp < 0
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// compareVersions compares two numeric migration versions, falling back to a
// lexicographic comparison when a version is not a plain number.
func compareVersions(a, b string) int {
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a, b)
}

// MigrateOptions controls a migration run.
type MigrateOptions struct {
	Direction Direction
	// Steps limits how many migrations change. For DirectionUp zero means "all
	// pending migrations"; for DirectionDown zero means "one".
	Steps  int
	Logger *slog.Logger
}

// Migrate applies or reverts migrations and returns the versions that changed.
//
// Each migration runs in its own transaction, so a failure rolls back cleanly
// and leaves the previous schema state intact. A PostgreSQL advisory lock
// serialises concurrent migrators (for example two panel replicas starting at
// the same time).
func (db *DB) Migrate(ctx context.Context, opts MigrateOptions) ([]string, error) {
	if db.pool == nil {
		return nil, errors.New("database is not initialised")
	}

	all, err := LoadMigrations()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("no migrations found in the embedded filesystem")
	}

	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
	}()

	if err := ensureMigrationTable(ctx, conn); err != nil {
		return nil, err
	}
	applied, err := loadAppliedMigrations(ctx, conn)
	if err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	switch opts.Direction {
	case DirectionUp:
		return applyUp(ctx, conn, all, applied, opts.Steps, logger)
	case DirectionDown:
		return applyDown(ctx, conn, all, applied, opts.Steps, logger)
	default:
		return nil, fmt.Errorf("unknown migration direction %q", opts.Direction)
	}
}

// Status reports the state of every known migration without changing anything.
func (db *DB) Status(ctx context.Context) ([]MigrationStatus, error) {
	if db.pool == nil {
		return nil, errors.New("database is not initialised")
	}
	all, err := LoadMigrations()
	if err != nil {
		return nil, err
	}

	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if err := ensureMigrationTable(ctx, conn); err != nil {
		return nil, err
	}
	applied, err := loadAppliedMigrations(ctx, conn)
	if err != nil {
		return nil, err
	}

	out := make([]MigrationStatus, 0, len(all))
	for _, mig := range all {
		st := MigrationStatus{
			Version:         mig.Version,
			Name:            mig.Name,
			HasDownScript:   strings.TrimSpace(mig.DownSQL) != "",
			ChecksumMatches: true,
		}
		if rec, ok := applied[mig.Version]; ok {
			st.Applied = true
			st.AppliedAt = rec.AppliedAt.UTC().Format(time.RFC3339)
			st.ChecksumMatches = rec.Checksum == mig.Checksum
		}
		out = append(out, st)
	}
	return out, nil
}

// PendingMigrations returns the versions that have not been applied yet.
func (db *DB) PendingMigrations(ctx context.Context) ([]MigrationStatus, error) {
	statuses, err := db.Status(ctx)
	if err != nil {
		return nil, err
	}
	pending := make([]MigrationStatus, 0, len(statuses))
	for _, st := range statuses {
		if !st.Applied {
			pending = append(pending, st)
		}
	}
	return pending, nil
}

const createMigrationTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    text        PRIMARY KEY,
    name       text        NOT NULL,
    checksum   text        NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`

func ensureMigrationTable(ctx context.Context, conn *pgxpool.Conn) error {
	if _, err := conn.Exec(ctx, createMigrationTableSQL); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}
	return nil
}

func loadAppliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[string]appliedMigrationRecord, error) {
	rows, err := conn.Query(ctx, `SELECT version, name, checksum, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[string]appliedMigrationRecord)
	for rows.Next() {
		var rec appliedMigrationRecord
		if err := rows.Scan(&rec.Version, &rec.Name, &rec.Checksum, &rec.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan schema_migrations row: %w", err)
		}
		out[rec.Version] = rec
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return out, nil
}

func applyUp(
	ctx context.Context,
	conn *pgxpool.Conn,
	all []Migration,
	applied map[string]appliedMigrationRecord,
	steps int,
	logger *slog.Logger,
) ([]string, error) {
	changed := make([]string, 0, len(all))
	for _, mig := range all {
		if rec, ok := applied[mig.Version]; ok {
			if rec.Checksum != mig.Checksum {
				return changed, fmt.Errorf(
					"migration %s_%s changed after it was applied (checksum mismatch): add a new migration instead of editing an applied one",
					mig.Version, mig.Name)
			}
			continue
		}
		if steps > 0 && len(changed) >= steps {
			break
		}
		if err := runMigration(ctx, conn, mig, DirectionUp); err != nil {
			return changed, err
		}
		logger.Info("migration applied", "version", mig.Version, "name", mig.Name)
		changed = append(changed, mig.Version)
	}
	return changed, nil
}

func applyDown(
	ctx context.Context,
	conn *pgxpool.Conn,
	all []Migration,
	applied map[string]appliedMigrationRecord,
	steps int,
	logger *slog.Logger,
) ([]string, error) {
	if len(applied) == 0 {
		return nil, nil
	}

	byVersion := make(map[string]Migration, len(all))
	for _, mig := range all {
		byVersion[mig.Version] = mig
	}

	versions := make([]string, 0, len(applied))
	for version := range applied {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i], versions[j]) > 0
	})

	limit := steps
	if limit <= 0 {
		limit = 1
	}

	changed := make([]string, 0, limit)
	for _, version := range versions {
		if len(changed) >= limit {
			break
		}
		mig, ok := byVersion[version]
		if !ok {
			return changed, fmt.Errorf("database has migration %s applied but the binary does not contain it", version)
		}
		if strings.TrimSpace(mig.DownSQL) == "" {
			return changed, fmt.Errorf("migration %s_%s has no down script", mig.Version, mig.Name)
		}
		if err := runMigration(ctx, conn, mig, DirectionDown); err != nil {
			return changed, err
		}
		logger.Warn("migration reverted", "version", mig.Version, "name", mig.Name)
		changed = append(changed, mig.Version)
	}
	return changed, nil
}

func runMigration(ctx context.Context, conn *pgxpool.Conn, mig Migration, dir Direction) error {
	script := mig.UpSQL
	if dir == DirectionDown {
		script = mig.DownSQL
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction for %s_%s: %w", mig.Version, mig.Name, err)
	}
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	// Migration files contain several statements (and, in 000001, a PL/pgSQL
	// body), so they must be sent with the simple query protocol.
	if _, err := tx.Exec(ctx, script, pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("run migration %s_%s (%s): %w", mig.Version, mig.Name, dir, err)
	}

	switch dir {
	case DirectionUp:
		_, err = tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
			mig.Version, mig.Name, mig.Checksum)
	case DirectionDown:
		_, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, mig.Version)
	}
	if err != nil {
		return fmt.Errorf("record migration %s_%s: %w", mig.Version, mig.Name, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s_%s: %w", mig.Version, mig.Name, err)
	}
	return nil
}