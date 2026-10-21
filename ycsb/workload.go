package ycsb

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

type WorkloadName string

const (
	WorkloadA WorkloadName = "A" // 50% Read, 50% Update
	WorkloadB WorkloadName = "B" // 95% Read, 5% Update
	WorkloadC WorkloadName = "C" // 100% Read
	WorkloadD WorkloadName = "D" // 95% Read, 5% Insert (Latest)
	WorkloadE WorkloadName = "E" // 95% Scan, 5% Insert
	WorkloadF WorkloadName = "F" // 50% Read, 50% Read-Modify-Write (RMW)
)

type WorkloadConfig struct {
	Workload       WorkloadName
	RecordCount    int64
	OperationCount int64
	ThreadCount    int
	FieldCount     int
	FieldLength    int
	Distribution   string // "zipfian", "uniform", "latest"
	ScanMaxCount   int
	TableName      string
	NegativeRatio  float64 // fraction of reads that are negative lookups (e.g. 0.80)
}

func DefaultWorkloadConfig(name WorkloadName) WorkloadConfig {
	dist := "zipfian"
	if name == WorkloadD {
		dist = "latest"
	}
	return WorkloadConfig{
		Workload:       name,
		RecordCount:    10000,
		OperationCount: 10000,
		ThreadCount:    4,
		FieldCount:     10,
		FieldLength:    100,
		Distribution:   dist,
		ScanMaxCount:   100,
		TableName:      "usertable",
	}
}

// WorkloadRunner manages loading and executing YCSB workloads.
type WorkloadRunner struct {
	config       WorkloadConfig
	db           DB
	keyGen       Generator
	insertGen    *CounterGenerator
	committedGen *CounterGenerator
	scanLenGen   Generator
	fieldValue   []byte
	metrics      *Metrics
}

func NewWorkloadRunner(db DB, config WorkloadConfig) *WorkloadRunner {
	if config.FieldCount <= 0 {
		config.FieldCount = 10
	}
	if config.FieldLength <= 0 {
		config.FieldLength = 100
	}
	if config.ScanMaxCount <= 0 {
		config.ScanMaxCount = 100
	}
	if config.TableName == "" {
		config.TableName = "usertable"
	}

	insertGen := NewCounterGenerator(config.RecordCount)
	committedGen := NewCounterGenerator(config.RecordCount)

	var keyGen Generator
	switch config.Distribution {
	case "uniform":
		keyGen = NewUniformGenerator(0, config.RecordCount-1)
	case "latest":
		keyGen = NewLatestGenerator(committedGen)
	case "scrambled_zipfian":
		keyGen = NewScrambledZipfianGenerator(0, config.RecordCount-1)
	default: // zipfian
		keyGen = NewZipfianGenerator(0, config.RecordCount-1)
	}

	fieldVal := make([]byte, config.FieldLength)
	for i := range fieldVal {
		fieldVal[i] = byte('a' + (i % 26))
	}

	return &WorkloadRunner{
		config:       config,
		db:           db,
		keyGen:       keyGen,
		insertGen:    insertGen,
		committedGen: committedGen,
		scanLenGen:   NewUniformGenerator(1, int64(config.ScanMaxCount)),
		fieldValue:   fieldVal,
		metrics:      NewMetrics(),
	}
}

func (r *WorkloadRunner) generateRow(fieldPrefix string) map[string][]byte {
	row := make(map[string][]byte, r.config.FieldCount)
	for i := 0; i < r.config.FieldCount; i++ {
		fieldName := fmt.Sprintf("field%d", i)
		val := make([]byte, r.config.FieldLength)
		copy(val, r.fieldValue)
		row[fieldName] = val
	}
	return row
}

// Load loads the initial dataset into the database.
func (r *WorkloadRunner) Load(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, r.config.ThreadCount)

	totalRecords := r.config.RecordCount
	recordsPerThread := totalRecords / int64(r.config.ThreadCount)

	for t := 0; t < r.config.ThreadCount; t++ {
		start := int64(t) * recordsPerThread
		end := start + recordsPerThread
		if t == r.config.ThreadCount-1 {
			end = totalRecords
		}

		wg.Add(1)
		go func(threadID int, startKey, endKey int64) {
			defer wg.Done()
			tCtx := r.db.InitThread(ctx, threadID, r.config.ThreadCount)
			defer r.db.CleanupThread(tCtx)

			for k := startKey; k < endKey; k++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				keyStr := FormatKey(k)
				row := r.generateRow("load")
				if err := r.db.Insert(tCtx, r.config.TableName, keyStr, row); err != nil {
					errCh <- fmt.Errorf("load insert failed for key %s: %w", keyStr, err)
					return
				}
			}
		}(t, start, end)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// Run executes the workload operations concurrently and records metrics.
func (r *WorkloadRunner) Run(ctx context.Context) (*Metrics, error) {
	r.metrics.Start()
	defer r.metrics.Stop()

	var wg sync.WaitGroup
	var completedOps int64
	totalOps := r.config.OperationCount

	for t := 0; t < r.config.ThreadCount; t++ {
		wg.Add(1)
		go func(threadID int) {
			defer wg.Done()
			tCtx := r.db.InitThread(ctx, threadID, r.config.ThreadCount)
			defer r.db.CleanupThread(tCtx)

			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(threadID*1000)))

			for {
				if atomic.AddInt64(&completedOps, 1) > totalOps {
					break
				}
				select {
				case <-ctx.Done():
					return
				default:
				}

				r.doOneOp(tCtx, rng)
			}
		}(t)
	}

	wg.Wait()
	return r.metrics, nil
}

func (r *WorkloadRunner) doOneOp(ctx context.Context, rng *rand.Rand) {
	p := rng.Float64()

	switch r.config.Workload {
	case WorkloadA: // 50% Read, 50% Update
		if p < 0.50 {
			r.doRead(ctx, rng)
		} else {
			r.doUpdate(ctx)
		}
	case WorkloadB: // 95% Read, 5% Update
		if p < 0.95 {
			r.doRead(ctx, rng)
		} else {
			r.doUpdate(ctx)
		}
	case WorkloadC: // 100% Read
		r.doRead(ctx, rng)
	case WorkloadD: // 95% Read, 5% Insert (Latest)
		if p < 0.95 {
			r.doRead(ctx, rng)
		} else {
			r.doInsert(ctx)
		}
	case WorkloadE: // 95% Scan, 5% Insert
		if p < 0.95 {
			r.doScan(ctx)
		} else {
			r.doInsert(ctx)
		}
	case WorkloadF: // 50% Read, 50% RMW
		if p < 0.50 {
			r.doRead(ctx, rng)
		} else {
			r.doRMW(ctx)
		}
	default:
		r.doRead(ctx, rng)
	}
}

func (r *WorkloadRunner) doRead(ctx context.Context, rng *rand.Rand) {
	var keyNum int64
	if r.config.NegativeRatio > 0 && rng != nil && rng.Float64() < r.config.NegativeRatio {
		// Negative lookup: key guaranteed not to exist in [0, RecordCount-1]
		keyNum = r.config.RecordCount + r.keyGen.Next()
	} else {
		// Positive lookup: key in [0, RecordCount-1]
		keyNum = r.keyGen.Next()
	}
	keyStr := FormatKey(keyNum)

	start := time.Now()
	_, err := r.db.Read(ctx, r.config.TableName, keyStr, nil)
	d := time.Since(start)

	r.metrics.Record(OpRead, d, err)
}

func (r *WorkloadRunner) doUpdate(ctx context.Context) {
	keyNum := r.keyGen.Next()
	keyStr := FormatKey(keyNum)
	updateValues := map[string][]byte{
		"field0": []byte(fmt.Sprintf("updated_%d", time.Now().UnixNano())),
	}

	start := time.Now()
	err := r.db.Update(ctx, r.config.TableName, keyStr, updateValues)
	d := time.Since(start)

	r.metrics.Record(OpUpdate, d, err)
}

func (r *WorkloadRunner) doInsert(ctx context.Context) {
	keyNum := r.insertGen.Next()
	keyStr := FormatKey(keyNum)
	row := r.generateRow("insert")

	start := time.Now()
	err := r.db.Insert(ctx, r.config.TableName, keyStr, row)
	d := time.Since(start)

	if err == nil && r.committedGen != nil {
		r.committedGen.Next()
	}

	r.metrics.Record(OpInsert, d, err)
}

func (r *WorkloadRunner) doScan(ctx context.Context) {
	keyNum := r.keyGen.Next()
	keyStr := FormatKey(keyNum)
	scanLen := int(r.scanLenGen.Next())

	start := time.Now()
	_, err := r.db.Scan(ctx, r.config.TableName, keyStr, scanLen, nil)
	d := time.Since(start)

	r.metrics.Record(OpScan, d, err)
}

func (r *WorkloadRunner) doRMW(ctx context.Context) {
	keyNum := r.keyGen.Next()
	keyStr := FormatKey(keyNum)

	start := time.Now()
	row, err := r.db.Read(ctx, r.config.TableName, keyStr, nil)
	if err == nil {
		if row == nil {
			row = make(map[string][]byte)
		}
		row["field0"] = []byte(fmt.Sprintf("rmw_%d", time.Now().UnixNano()))
		err = r.db.Update(ctx, r.config.TableName, keyStr, row)
	}
	d := time.Since(start)

	r.metrics.Record(OpRMW, d, err)
}
