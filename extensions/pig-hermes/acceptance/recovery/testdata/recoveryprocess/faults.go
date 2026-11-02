package main

import (
	"context"
	"database/sql"
	"os"
)

func (d *driver) corruptionLoop(ctx context.Context) (reply, error) {
	if err := d.recovery.open(ctx); err != nil {
		return reply{}, err
	}
	if err := d.recovery.manager.CheckIntegrity(ctx); err != nil {
		return reply{}, err
	}
	result := reply{}
	err := d.recovery.manager.Do(ctx, func(db *sql.DB) error {
		result.Calls++
		var size, page int64
		if err := db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT rootpage FROM sqlite_schema WHERE name='memory_fts_data'`).Scan(&page); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return err
		}
		file, err := os.OpenFile(d.resolved().Database, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_, err = file.WriteAt(make([]byte, size), (page-1)*size)
		if err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `PRAGMA shrink_memory`); err != nil {
			return err
		}
		var content string
		return db.QueryRowContext(ctx, `SELECT content FROM memory_fts WHERE memory_fts MATCH 'checkpoint'`).Scan(&content)
	})
	return result, err
}

func (d *driver) drain(ctx context.Context) (reply, error) {
	if err := d.recovery.open(ctx); err != nil {
		return reply{}, err
	}
	if err := d.recovery.manager.CheckIntegrity(ctx); err != nil {
		return reply{}, err
	}
	result := reply{IntegrityComplete: d.recovery.manager.Status().IntegrityComplete}
	if err := d.recovery.manager.Do(ctx, func(db *sql.DB) error {
		var value int
		err := db.QueryRowContext(ctx, `SELECT 1`).Scan(&value)
		result.Drained = value == 1
		return err
	}); err != nil {
		return result, err
	}
	if err := d.recovery.close(); err != nil {
		return result, err
	}
	err := d.recovery.manager.Do(ctx, func(db *sql.DB) error { result.PostCloseCalls++; return nil })
	result.PostCloseRejected = err != nil
	return result, nil
}
