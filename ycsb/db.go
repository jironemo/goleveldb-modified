package ycsb

import (
	"bytes"
	"context"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// DB is the database interface matching standard go-ycsb.
type DB interface {
	Close() error
	InitThread(ctx context.Context, threadID int, threadCount int) context.Context
	CleanupThread(ctx context.Context)
	Read(ctx context.Context, table string, key string, fields []string) (map[string][]byte, error)
	Scan(ctx context.Context, table string, startKey string, count int, fields []string) ([]map[string][]byte, error)
	Update(ctx context.Context, table string, key string, values map[string][]byte) error
	Insert(ctx context.Context, table string, key string, values map[string][]byte) error
	Delete(ctx context.Context, table string, key string) error
}

type EngineType string

const (
	EngineOriginal EngineType = "original" // Pure standard GoLevelDB (Snappy, Block Cache, Index Blocks, Iterators)
	EngineModified EngineType = "modified" // Hybrid GoLevelDB (B+ Tree, Direct Offset Reads, AI Shield, RangeQuery)
)

type ScanMode string

const (
	ScanModeRangeQuery ScanMode = "rangequery" // Uses goleveldb-modified B+ Tree RangeQuery
	ScanModeIterator   ScanMode = "iterator"   // Uses standard LevelDB NewIterator
)

// LevelDBOptions configures the LevelDB adapter.
type LevelDBOptions struct {
	Path           string
	Engine         EngineType
	Compression    opt.Compression
	BlockCacheSize int // bytes (e.g. 8MB)
	BloomBits      int
	ScanMode       ScanMode
	Sync           bool
}

// LevelDBAdapter implements DB for both goleveldb-original and goleveldb-modified.
type LevelDBAdapter struct {
	db         *leveldb.DB
	engine     EngineType
	serializer *RowSerializer
	wo         *opt.WriteOptions
	ro         *opt.ReadOptions
	scanMode   ScanMode
}

// NewLevelDBAdapter opens or creates a LevelDB database configured for the specified engine.
func NewLevelDBAdapter(options LevelDBOptions) (*LevelDBAdapter, error) {
	engine := options.Engine
	if engine == "" {
		engine = EngineModified
	}

	opts := &opt.Options{}

	if engine == EngineOriginal {
		// Standard LevelDB defaults
		if options.Compression == opt.DefaultCompression || options.Compression == 0 {
			opts.Compression = opt.SnappyCompression
		} else {
			opts.Compression = options.Compression
		}

		cacheSize := options.BlockCacheSize
		if cacheSize <= 0 {
			cacheSize = 8 * opt.MiB // 8MB default standard block cache
		}
		opts.BlockCacher = opt.LRUCacher
		opts.BlockCacheCapacity = cacheSize

		bloom := options.BloomBits
		if bloom <= 0 {
			bloom = 10
		}
		opts.Filter = filter.NewBloomFilter(bloom)
	} else {
		// Modified hybrid engine defaults
		opts.Compression = opt.NoCompression // Required for direct byte offset reads
		bloom := options.BloomBits
		if bloom <= 0 {
			bloom = 10
		}
		opts.Filter = filter.NewBloomFilter(bloom)
	}

	db, err := leveldb.OpenFile(options.Path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open leveldb at %s: %w", options.Path, err)
	}

	wo := &opt.WriteOptions{
		Sync: options.Sync,
	}

	scanMode := options.ScanMode
	if scanMode == "" {
		if engine == EngineOriginal {
			scanMode = ScanModeIterator
		} else {
			scanMode = ScanModeRangeQuery
		}
	}

	return &LevelDBAdapter{
		db:         db,
		engine:     engine,
		serializer: &RowSerializer{},
		wo:         wo,
		ro:         &opt.ReadOptions{},
		scanMode:   scanMode,
	}, nil
}

// NewOriginalLevelDBAdapter creates an adapter running pure standard GoLevelDB.
func NewOriginalLevelDBAdapter(path string) (*LevelDBAdapter, error) {
	return NewLevelDBAdapter(LevelDBOptions{
		Path:        path,
		Engine:      EngineOriginal,
		Compression: opt.SnappyCompression,
		BloomBits:   10,
		ScanMode:    ScanModeIterator,
	})
}

// NewModifiedLevelDBAdapter creates an adapter running the hybrid GoLevelDB.
func NewModifiedLevelDBAdapter(path string) (*LevelDBAdapter, error) {
	return NewLevelDBAdapter(LevelDBOptions{
		Path:        path,
		Engine:      EngineModified,
		Compression: opt.NoCompression,
		BloomBits:   10,
		ScanMode:    ScanModeRangeQuery,
	})
}

// DBInstance returns the underlying *leveldb.DB instance.
func (a *LevelDBAdapter) DBInstance() *leveldb.DB {
	return a.db
}

// Engine returns the configured engine type.
func (a *LevelDBAdapter) Engine() EngineType {
	return a.engine
}

func (a *LevelDBAdapter) Close() error {
	return a.db.Close()
}

func (a *LevelDBAdapter) InitThread(ctx context.Context, threadID int, threadCount int) context.Context {
	return ctx
}

func (a *LevelDBAdapter) CleanupThread(ctx context.Context) {}

func (a *LevelDBAdapter) Read(ctx context.Context, table string, key string, fields []string) (map[string][]byte, error) {
	fullKey := []byte(buildKey(table, key))

	var valBytes []byte
	var err error

	if a.engine == EngineOriginal {
		// Standard LevelDB read path: acquires snapshot to trigger standard v.get() pipeline
		// which traverses SSTable Index Blocks, Bloom filters, block cache, and Snappy decompression.
		snap, sErr := a.db.GetSnapshot()
		if sErr != nil {
			return nil, sErr
		}
		valBytes, err = snap.Get(fullKey, a.ro)
		snap.Release()
	} else {
		// Modified hybrid read path: B+ Tree + Direct ReadRaw
		valBytes, err = a.db.Get(fullKey, a.ro)
	}

	if err != nil {
		return nil, err
	}

	row, err := a.serializer.Deserialize(valBytes)
	if err != nil {
		return nil, err
	}

	if len(fields) == 0 {
		return row, nil
	}

	return filterFields(row, fields), nil
}

func (a *LevelDBAdapter) Scan(ctx context.Context, table string, startKey string, count int, fields []string) ([]map[string][]byte, error) {
	fullStartKey := []byte(buildKey(table, startKey))
	results := make([]map[string][]byte, 0, count)

	if a.engine == EngineModified && a.scanMode == ScanModeRangeQuery {
		// Use B-tree accelerated RangeQuery
		keys, values, err := a.db.RangeQuery(fullStartKey, nil)
		if err != nil {
			return nil, err
		}

		tablePrefix := []byte(table + ":")
		for i := 0; i < len(keys) && len(results) < count; i++ {
			if !bytes.HasPrefix(keys[i], tablePrefix) {
				break
			}
			row, err := a.serializer.Deserialize(values[i])
			if err != nil {
				continue
			}
			results = append(results, filterFields(row, fields))
		}
	} else {
		// Standard LevelDB multi-way merge iterator (with Snappy & Block Cache decoding)
		iter := a.db.NewIterator(&util.Range{Start: fullStartKey}, a.ro)
		defer iter.Release()

		tablePrefix := []byte(table + ":")
		for iter.Next() && len(results) < count {
			if !bytes.HasPrefix(iter.Key(), tablePrefix) {
				break
			}
			row, err := a.serializer.Deserialize(iter.Value())
			if err != nil {
				continue
			}
			results = append(results, filterFields(row, fields))
		}
		if err := iter.Error(); err != nil {
			return nil, err
		}
	}

	return results, nil
}

func (a *LevelDBAdapter) Insert(ctx context.Context, table string, key string, values map[string][]byte) error {
	fullKey := []byte(buildKey(table, key))
	valBytes := a.serializer.Serialize(values)
	return a.db.Put(fullKey, valBytes, a.wo)
}

func (a *LevelDBAdapter) Update(ctx context.Context, table string, key string, values map[string][]byte) error {
	fullKey := []byte(buildKey(table, key))

	var existingBytes []byte
	var err error

	if a.engine == EngineOriginal {
		snap, sErr := a.db.GetSnapshot()
		if sErr == nil {
			existingBytes, err = snap.Get(fullKey, a.ro)
			snap.Release()
		}
	} else {
		existingBytes, err = a.db.Get(fullKey, a.ro)
	}

	if err != nil && err != leveldb.ErrNotFound {
		return err
	}

	var row map[string][]byte
	if err == nil {
		row, _ = a.serializer.Deserialize(existingBytes)
	}
	if row == nil {
		row = make(map[string][]byte, len(values))
	}

	for k, v := range values {
		row[k] = v
	}

	valBytes := a.serializer.Serialize(row)
	return a.db.Put(fullKey, valBytes, a.wo)
}

func (a *LevelDBAdapter) Delete(ctx context.Context, table string, key string) error {
	fullKey := []byte(buildKey(table, key))
	return a.db.Delete(fullKey, a.wo)
}

func buildKey(table, key string) string {
	return table + ":" + key
}

func filterFields(row map[string][]byte, fields []string) map[string][]byte {
	if len(fields) == 0 {
		return row
	}
	filtered := make(map[string][]byte, len(fields))
	for _, f := range fields {
		if v, ok := row[f]; ok {
			filtered[f] = v
		}
	}
	return filtered
}
