package ycsb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/syndtr/goleveldb/leveldb/opt"
)

func TestBBolt_WorkloadsAThroughF(t *testing.T) {
	workloads := []WorkloadName{
		WorkloadA,
		WorkloadB,
		WorkloadC,
		WorkloadD,
		WorkloadE,
		WorkloadF,
	}

	for _, wl := range workloads {
		t.Run(fmt.Sprintf("BBolt_Workload_%s", wl), func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", fmt.Sprintf("bbolt-test-%s", wl))
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)

			adapter, err := NewBBoltAdapter(BBoltOptions{
				Path:   filepath.Join(tmpDir, "test.db"),
				NoSync: true, // NoSync for fair benchmark comparison with LevelDB asynchronous WAL
			})
			if err != nil {
				t.Fatalf("failed to create bbolt adapter: %v", err)
			}
			defer adapter.Close()

			config := WorkloadConfig{
				Workload:       wl,
				RecordCount:    100,
				OperationCount: 200,
				ThreadCount:    2,
				FieldCount:     5,
				FieldLength:    32,
				Distribution:   "zipfian",
				ScanMaxCount:   10,
				TableName:      "usertable",
			}
			if wl == WorkloadD {
				config.Distribution = "latest"
			}

			runner := NewWorkloadRunner(adapter, config)
			ctx := context.Background()

			if err := runner.Load(ctx); err != nil {
				t.Fatalf("load failed: %v", err)
			}

			metrics, err := runner.Run(ctx)
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}

			if metrics.TotalErrors() > 0 {
				t.Fatalf("encountered %d unexpected errors during bbolt workload %s", metrics.TotalErrors(), wl)
			}
		})
	}
}

func run3WayBenchmark(b *testing.B, engine string, wl WorkloadName) {
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("ycsb-bench3-%s-%s", engine, wl))
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	var adapter DB
	switch engine {
	case "bbolt":
		adapter, err = NewBBoltAdapter(BBoltOptions{
			Path:   filepath.Join(tmpDir, "bbolt.db"),
			NoSync: true,
		})
	case "original":
		adapter, err = NewLevelDBAdapter(LevelDBOptions{
			Path:        tmpDir,
			Engine:      EngineOriginal,
			Compression: opt.SnappyCompression,
			BloomBits:   10,
			ScanMode:    ScanModeIterator,
		})
	case "modified":
		adapter, err = NewLevelDBAdapter(LevelDBOptions{
			Path:        tmpDir,
			Engine:      EngineModified,
			Compression: opt.NoCompression,
			BloomBits:   10,
			ScanMode:    ScanModeRangeQuery,
		})
	}

	if err != nil {
		b.Fatal(err)
	}
	defer adapter.Close()

	config := WorkloadConfig{
		Workload:       wl,
		RecordCount:    2000,
		OperationCount: int64(b.N),
		ThreadCount:    4,
		FieldCount:     10,
		FieldLength:    100,
		Distribution:   "zipfian",
		ScanMaxCount:   50,
		TableName:      "usertable",
	}

	runner := NewWorkloadRunner(adapter, config)
	ctx := context.Background()

	if err := runner.Load(ctx); err != nil {
		b.Fatalf("load failed: %v", err)
	}

	b.ResetTimer()
	metrics, err := runner.Run(ctx)
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}

	b.ReportMetric(metrics.QPS(), "ops/sec")
}

func BenchmarkCompare3_WorkloadA_BBolt(b *testing.B)     { run3WayBenchmark(b, "bbolt", WorkloadA) }
func BenchmarkCompare3_WorkloadA_Original(b *testing.B)  { run3WayBenchmark(b, "original", WorkloadA) }
func BenchmarkCompare3_WorkloadA_Modified(b *testing.B)  { run3WayBenchmark(b, "modified", WorkloadA) }

func BenchmarkCompare3_WorkloadC_BBolt(b *testing.B)     { run3WayBenchmark(b, "bbolt", WorkloadC) }
func BenchmarkCompare3_WorkloadC_Original(b *testing.B)  { run3WayBenchmark(b, "original", WorkloadC) }
func BenchmarkCompare3_WorkloadC_Modified(b *testing.B)  { run3WayBenchmark(b, "modified", WorkloadC) }
