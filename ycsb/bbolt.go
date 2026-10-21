package ycsb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"go.etcd.io/bbolt"
)

// BBoltOptions configures the BBolt adapter.
type BBoltOptions struct {
	Path         string
	NoSync       bool
	FreelistType bbolt.FreelistType
	Timeout      time.Duration
}

// BBoltAdapter implements the ycsb.DB interface for bbolt (pure B+ Tree).
type BBoltAdapter struct {
	db         *bbolt.DB
	serializer *RowSerializer
	path       string
}

// NewBBoltAdapter opens or creates a BBolt database with the specified options.
func NewBBoltAdapter(options BBoltOptions) (*BBoltAdapter, error) {
	dbPath := options.Path
	if dbPath == "" {
		tmpDir, err := os.MkdirTemp("", "bbolt-ycsb")
		if err != nil {
			return nil, err
		}
		dbPath = filepath.Join(tmpDir, "bbolt.db")
	} else if filepath.Ext(dbPath) == "" {
		// If a directory was provided, create bbolt.db inside it
		_ = os.MkdirAll(dbPath, 0755)
		dbPath = filepath.Join(dbPath, "bbolt.db")
	}

	bboltOpts := &bbolt.Options{
		NoSync:       options.NoSync,
		FreelistType: bbolt.FreelistMapType,
		Timeout:      10 * time.Second,
	}

	db, err := bbolt.Open(dbPath, 0600, bboltOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to open bbolt at %s: %w", dbPath, err)
	}

	// Pre-create standard default bucket
	err = db.Update(func(tx *bbolt.Tx) error {
		_, bErr := tx.CreateBucketIfNotExists([]byte("usertable"))
		return bErr
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to create default bucket: %w", err)
	}

	return &BBoltAdapter{
		db:         db,
		serializer: &RowSerializer{},
		path:       dbPath,
	}, nil
}

// DBInstance returns the underlying *bbolt.DB instance.
func (a *BBoltAdapter) DBInstance() *bbolt.DB {
	return a.db
}

func (a *BBoltAdapter) Close() error {
	return a.db.Close()
}

func (a *BBoltAdapter) InitThread(ctx context.Context, threadID int, threadCount int) context.Context {
	return ctx
}

func (a *BBoltAdapter) CleanupThread(ctx context.Context) {}

func (a *BBoltAdapter) Read(ctx context.Context, table string, key string, fields []string) (map[string][]byte, error) {
	var row map[string][]byte
	var err error

	vErr := a.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(table))
		if b == nil {
			return leveldb.ErrNotFound
		}

		valBytes := b.Get([]byte(key))
		if valBytes == nil {
			return leveldb.ErrNotFound
		}

		row, err = a.serializer.Deserialize(valBytes)
		return err
	})

	if vErr != nil {
		return nil, vErr
	}

	if len(fields) == 0 {
		return row, nil
	}

	return filterFields(row, fields), nil
}

func (a *BBoltAdapter) Scan(ctx context.Context, table string, startKey string, count int, fields []string) ([]map[string][]byte, error) {
	results := make([]map[string][]byte, 0, count)

	vErr := a.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(table))
		if b == nil {
			return nil
		}

		c := b.Cursor()
		startK := []byte(startKey)

		for k, v := c.Seek(startK); k != nil && len(results) < count; k, v = c.Next() {
			if v == nil {
				continue
			}
			row, err := a.serializer.Deserialize(v)
			if err != nil {
				continue
			}
			results = append(results, filterFields(row, fields))
		}
		return nil
	})

	if vErr != nil {
		return nil, vErr
	}

	return results, nil
}

func (a *BBoltAdapter) Insert(ctx context.Context, table string, key string, values map[string][]byte) error {
	valBytes := a.serializer.Serialize(values)

	return a.db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(table))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), valBytes)
	})
}

func (a *BBoltAdapter) Update(ctx context.Context, table string, key string, values map[string][]byte) error {
	return a.db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(table))
		if err != nil {
			return err
		}

		existingBytes := b.Get([]byte(key))
		var row map[string][]byte
		if existingBytes != nil {
			row, _ = a.serializer.Deserialize(existingBytes)
		}
		if row == nil {
			row = make(map[string][]byte, len(values))
		}

		for k, v := range values {
			row[k] = v
		}

		valBytes := a.serializer.Serialize(row)
		return b.Put([]byte(key), valBytes)
	})
}

func (a *BBoltAdapter) Delete(ctx context.Context, table string, key string) error {
	return a.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(table))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}
