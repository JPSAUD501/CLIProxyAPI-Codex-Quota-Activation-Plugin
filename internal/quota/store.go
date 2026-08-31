package quota

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Store struct{ db *sql.DB }

func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "quota.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`CREATE TABLE IF NOT EXISTS cycles(account_key TEXT NOT NULL,cycle_id TEXT NOT NULL,state TEXT NOT NULL,model TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,http_status INTEGER NOT NULL DEFAULT 0,error_code TEXT NOT NULL DEFAULT '',PRIMARY KEY(account_key,cycle_id))`, `CREATE TABLE IF NOT EXISTS runs(id INTEGER PRIMARY KEY AUTOINCREMENT,started_at TEXT NOT NULL,finished_at TEXT NOT NULL,mode TEXT NOT NULL,scanned INTEGER NOT NULL,eligible INTEGER NOT NULL,verified INTEGER NOT NULL,partial INTEGER NOT NULL,failed INTEGER NOT NULL,skipped INTEGER NOT NULL)`, `CREATE TABLE IF NOT EXISTS observations(account_key TEXT PRIMARY KEY,auth_id TEXT NOT NULL,auth_index TEXT NOT NULL,label TEXT NOT NULL,plan TEXT NOT NULL,observed_at TEXT NOT NULL,snapshot_json BLOB NOT NULL,backoff_until TEXT NOT NULL DEFAULT '')`, `CREATE TABLE IF NOT EXISTS account_backoffs(account_key TEXT PRIMARY KEY,until_at TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS confirmations(token_hash TEXT PRIMARY KEY,accounts_json BLOB NOT NULL,expires_at TEXT NOT NULL,used_at TEXT NOT NULL DEFAULT '')`, `CREATE INDEX IF NOT EXISTS cycles_state_time ON cycles(state,updated_at DESC)`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS catalog_cache(name TEXT PRIMARY KEY,payload BLOB NOT NULL,source TEXT NOT NULL,fetched_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS run_outcomes(id INTEGER PRIMARY KEY AUTOINCREMENT,run_id INTEGER NOT NULL,account_key TEXT NOT NULL,state TEXT NOT NULL,reason_code TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',price_nano_usd INTEGER NOT NULL DEFAULT 0,source TEXT NOT NULL DEFAULT '',attempted INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS run_outcomes_run ON run_outcomes(run_id,id)`,
		`CREATE INDEX IF NOT EXISTS run_outcomes_account ON run_outcomes(account_key,id DESC)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("migrate v1.3.0: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) SaveCatalog(ctx context.Context, name string, payload []byte, source string, fetchedAt time.Time) error {
	if len(payload) == 0 || len(payload) > maxCatalogBytes {
		return errors.New("invalid catalog cache payload")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO catalog_cache(name,payload,source,fetched_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET payload=excluded.payload,source=excluded.source,fetched_at=excluded.fetched_at,updated_at=excluded.updated_at`, name, payload, source, fetchedAt.UTC().Format(time.RFC3339Nano), now)
	return err
}

func (s *Store) LoadCatalog(ctx context.Context, name string) ([]byte, string, time.Time, error) {
	var payload []byte
	var source, fetched string
	if err := s.db.QueryRowContext(ctx, `SELECT payload,source,fetched_at FROM catalog_cache WHERE name=?`, name).Scan(&payload, &source, &fetched); err != nil {
		return nil, "", time.Time{}, err
	}
	fetchedAt, err := time.Parse(time.RFC3339Nano, fetched)
	return payload, source, fetchedAt, err
}
func (s *Store) Reserve(ctx context.Context, account, cycle, model string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO cycles(account_key,cycle_id,state,model,created_at,updated_at) VALUES(?,?,'reserved',?,?,?)`, account, cycle, model, now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		res, err = tx.ExecContext(ctx, `UPDATE cycles SET state='reserved',model=?,http_status=0,error_code='',updated_at=? WHERE account_key=? AND cycle_id=? AND state='failed_retriable'`, model, now, account, cycle)
		if err != nil {
			return false, err
		}
		n, _ = res.RowsAffected()
		if n == 0 {
			return false, nil
		}
	}
	return true, tx.Commit()
}
func (s *Store) SetCycle(ctx context.Context, account, cycle, state string, status int, code string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cycles SET state=?,http_status=?,error_code=?,updated_at=? WHERE account_key=? AND cycle_id=?`, state, status, code, time.Now().UTC().Format(time.RFC3339Nano), account, cycle)
	return err
}
func (s *Store) Observe(ctx context.Context, a Account, snapshot Snapshot) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO observations(account_key,auth_id,auth_index,label,plan,observed_at,snapshot_json) VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_key) DO UPDATE SET auth_id=excluded.auth_id,auth_index=excluded.auth_index,label=excluded.label,plan=excluded.plan,observed_at=excluded.observed_at,snapshot_json=excluded.snapshot_json`, a.Key, a.ID, a.AuthIndex, a.Label, a.Plan, time.Now().UTC().Format(time.RFC3339Nano), raw)
	return err
}

// ResolveCycle keeps an eligible cycle stable while repeated observations still
// describe the same fresh state. A new identity is accepted only after a
// persisted non-eligible observation, which represents the active -> fresh
// transition required by the activation contract.
func (s *Store) ResolveCycle(ctx context.Context, account string, snapshot Snapshot) (Snapshot, error) {
	if !snapshot.Eligible {
		return snapshot, nil
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_json FROM observations WHERE account_key=?`, account).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var previous Snapshot
	if err := json.Unmarshal(raw, &previous); err != nil {
		return Snapshot{}, fmt.Errorf("decode previous quota observation: %w", err)
	}
	if previous.Eligible && previous.CycleID != "" {
		snapshot.CycleID = previous.CycleID
	}
	return snapshot, nil
}

func (s *Store) BackoffUntil(ctx context.Context, account string) (time.Time, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT until_at FROM account_backoffs WHERE account_key=?`, account).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || value == "" {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, value)
}

func (s *Store) SetBackoff(ctx context.Context, account string, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO account_backoffs(account_key,until_at) VALUES(?,?) ON CONFLICT(account_key) DO UPDATE SET until_at=excluded.until_at`, account, until.UTC().Format(time.RFC3339Nano))
	return err
}

type RunRow struct {
	ID                                                    int64 `json:"id"`
	StartedAt, FinishedAt, Mode                           string
	Scanned, Eligible, Verified, Partial, Failed, Skipped int
	Attempted                                             int
	Outcomes                                              []RunOutcome `json:"outcomes,omitempty"`
}

type RunOutcome struct {
	AccountKey   string `json:"account_key"`
	State        string `json:"state"`
	ReasonCode   string `json:"reason_code,omitempty"`
	Model        string `json:"model,omitempty"`
	PriceNanoUSD int64  `json:"price_nano_usd,omitempty"`
	Source       string `json:"source,omitempty"`
	Attempted    bool   `json:"attempted"`
}

func (s *Store) SaveRun(ctx context.Context, r RunRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO runs(started_at,finished_at,mode,scanned,eligible,verified,partial,failed,skipped) VALUES(?,?,?,?,?,?,?,?,?)`, r.StartedAt, r.FinishedAt, r.Mode, r.Scanned, r.Eligible, r.Verified, r.Partial, r.Failed, r.Skipped)
	if err != nil {
		return err
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for _, outcome := range r.Outcomes {
		attempted := 0
		if outcome.Attempted {
			attempted = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_outcomes(run_id,account_key,state,reason_code,model,price_nano_usd,source,attempted,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, runID, outcome.AccountKey, outcome.State, outcome.ReasonCode, outcome.Model, outcome.PriceNanoUSD, outcome.Source, attempted, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Runs(ctx context.Context) ([]RunRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.started_at,r.finished_at,r.mode,r.scanned,r.eligible,r.verified,r.partial,r.failed,r.skipped,COALESCE((SELECT SUM(o.attempted) FROM run_outcomes o WHERE o.run_id=r.id),0) FROM runs r ORDER BY r.id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunRow{}
	for rows.Next() {
		var r RunRow
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.FinishedAt, &r.Mode, &r.Scanned, &r.Eligible, &r.Verified, &r.Partial, &r.Failed, &r.Skipped, &r.Attempted); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) LastOutcome(ctx context.Context, account string) (RunOutcome, error) {
	var outcome RunOutcome
	var attempted int
	err := s.db.QueryRowContext(ctx, `SELECT account_key,state,reason_code,model,price_nano_usd,source,attempted FROM run_outcomes WHERE account_key=? AND (attempted=1 OR state='failed_before_send') ORDER BY id DESC LIMIT 1`, account).Scan(&outcome.AccountKey, &outcome.State, &outcome.ReasonCode, &outcome.Model, &outcome.PriceNanoUSD, &outcome.Source, &attempted)
	outcome.Attempted = attempted == 1
	return outcome, err
}
func (s *Store) Health(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) Close() error                     { return s.db.Close() }
