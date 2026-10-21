package ycsb

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/syndtr/goleveldb/leveldb/opt"
)

func TestYCSB_Generators(t *testing.T) {
	t.Run("Uniform", func(t *testing.T) {
		gen := NewUniformGenerator(10, 20)
		for i := 0; i < 100; i++ {
			v := gen.Next()
			if v < 10 || v > 20 {
				t.Fatalf("expected uniform value in [10, 20], got %d", v)
			}
		}
	})

	t.Run("Zipfian", func(t *testing.T) {
		gen := NewZipfianGenerator(0, 1000)
		counts := make(map[int64]int)
		for i := 0; i < 5000; i++ {
			v := gen.Next()
			if v < 0 || v > 1000 {
				t.Fatalf("expected zipfian value in [0, 1000], got %d", v)
			}
			counts[v]++
		}
		// In Zipfian, lower values should be much more frequent
		if counts[0]+counts[1] < counts[999]+counts[998] {
			t.Fatalf("zipfian skew failed: lower items should have higher frequency")
		}
	})

	t.Run("Latest", func(t *testing.T) {
		counter := NewCounterGenerator(100)
		gen := NewLatestGenerator(counter)
		for i := 0; i < 50; i++ {
			counter.Next()
			v := gen.Next()
			if v < 0 || v > counter.Last() {
				t.Fatalf("latest value out of bounds: %d (max: %d)", v, counter.Last())
			}
		}
	})
}

func TestYCSB_Serializer(t *testing.T) {
	ser := &RowSerializer{}
	fields := map[string][]byte{
		"field0": []byte("value_zero"),
		"field1": []byte("value_one"),
		"field2": []byte("value_two"),
	}

	data := ser.Serialize(fields)
	deserialized, err := ser.Deserialize(data)
	if err != nil {
		t.Fatalf("failed to deserialize: %v", err)
	}

	if len(deserialized) != len(fields) {
		t.Fatalf("expected %d fields, got %d", len(fields), len(deserialized))
	}

	for k, v := range fields {
		if !bytes.Equal(deserialized[k], v) {
			t.Fatalf("field mismatch for %s: expected %s, got %s", k, string(v), string(deserialized[k]))
		}
	}
}

func TestYCSB_ModifiedEngine_WorkloadsAThroughF(t *testing.T) {
	workloads := []WorkloadName{
		WorkloadA,
		WorkloadB,
		WorkloadC,
		WorkloadD,
		WorkloadE,
		WorkloadF,
	}

	for _, wl := range workloads {
		t.Run(fmt.Sprintf("Workload_%s", wl), func(t *testing.T) {
			tmpDir, err := ioutil.TempDir("", fmt.Sprintf("ycsb-test-mod-%s", wl))
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)

			adapter, err := NewLevelDBAdapter(LevelDBOptions{
				Path:        tmpDir,
				Engine:      EngineModified,
				Compression: opt.NoCompression,
				BloomBits:   10,
				ScanMode:    ScanModeRangeQuery,
			})
			if err != nil {
				t.Fatalf("failed to create adapter: %v", err)
			}
			defer adapter.Close()

			config := WorkloadConfig{
				Workload:       wl,
				RecordCount:    200,
				OperationCount: 500,
				ThreadCount:    2,
				FieldCount:     5,
				FieldLength:    32,
				Distribution:   "zipfian",
				ScanMaxCount:   20,
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

			if metrics.TotalOps() < config.OperationCount {
				t.Fatalf("expected at least %d ops, got %d", config.OperationCount, metrics.TotalOps())
			}

			if metrics.TotalErrors() > 0 {
				t.Fatalf("encountered %d unexpected errors during workload %s", metrics.TotalErrors(), wl)
			}
		})
	}
}

func TestYCSB_OriginalEngine_WorkloadsAThroughF(t *testing.T) {
	workloads := []WorkloadName{
		WorkloadA,
		WorkloadB,
		WorkloadC,
		WorkloadD,
		WorkloadE,
		WorkloadF,
	}

	for _, wl := range workloads {
		t.Run(fmt.Sprintf("Original_Workload_%s", wl), func(t *testing.T) {
			tmpDir, err := ioutil.TempDir("", fmt.Sprintf("ycsb-test-orig-%s", wl))
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)

			adapter, err := NewLevelDBAdapter(LevelDBOptions{
				Path:        tmpDir,
				Engine:      EngineOriginal,
				Compression: opt.SnappyCompression,
				BloomBits:   10,
				ScanMode:    ScanModeIterator,
			})
			if err != nil {
				t.Fatalf("failed to create adapter: %v", err)
			}
			defer adapter.Close()

			config := WorkloadConfig{
				Workload:       wl,
				RecordCount:    200,
				OperationCount: 500,
				ThreadCount:    2,
				FieldCount:     5,
				FieldLength:    32,
				Distribution:   "zipfian",
				ScanMaxCount:   20,
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

			if metrics.TotalOps() < config.OperationCount {
				t.Fatalf("expected at least %d ops, got %d", config.OperationCount, metrics.TotalOps())
			}

			if metrics.TotalErrors() > 0 {
				t.Fatalf("encountered %d unexpected errors during workload %s", metrics.TotalErrors(), wl)
			}
		})
	}
}

func runEngineBenchmark(b *testing.B, engine EngineType, wl WorkloadName) {
	tmpDir, err := ioutil.TempDir("", fmt.Sprintf("ycsb-bench-%s-%s", engine, wl))
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	compression := opt.NoCompression
	scanMode := ScanModeRangeQuery
	if engine == EngineOriginal {
		compression = opt.SnappyCompression
		scanMode = ScanModeIterator
	}

	adapter, err := NewLevelDBAdapter(LevelDBOptions{
		Path:        tmpDir,
		Engine:      engine,
		Compression: compression,
		BloomBits:   10,
		ScanMode:    scanMode,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer adapter.Close()

	config := WorkloadConfig{
		Workload:       wl,
		RecordCount:    5000,
		OperationCount: int64(b.N),
		ThreadCount:    4,
		FieldCount:     10,
		FieldLength:    100,
		Distribution:   "zipfian",
		ScanMaxCount:   50,
		TableName:      "usertable",
	}
	if wl == WorkloadD {
		config.Distribution = "latest"
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

func BenchmarkCompare_WorkloadA_Original(b *testing.B) { runEngineBenchmark(b, EngineOriginal, WorkloadA) }
func BenchmarkCompare_WorkloadA_Modified(b *testing.B) { runEngineBenchmark(b, EngineModified, WorkloadA) }

func BenchmarkCompare_WorkloadB_Original(b *testing.B) { runEngineBenchmark(b, EngineOriginal, WorkloadB) }
func BenchmarkCompare_WorkloadB_Modified(b *testing.B) { runEngineBenchmark(b, EngineModified, WorkloadB) }

func BenchmarkCompare_WorkloadC_Original(b *testing.B) { runEngineBenchmark(b, EngineOriginal, WorkloadC) }
func BenchmarkCompare_WorkloadC_Modified(b *testing.B) { runEngineBenchmark(b, EngineModified, WorkloadC) }

func BenchmarkCompare_WorkloadE_Original(b *testing.B) { runEngineBenchmark(b, EngineOriginal, WorkloadE) }
func BenchmarkCompare_WorkloadE_Modified(b *testing.B) { runEngineBenchmark(b, EngineModified, WorkloadE) }
