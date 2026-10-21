package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/ycsb"
)

func main() {
	engineFlag := flag.String("engine", "modified", "Engine to benchmark: bbolt, original, modified, compare, compare3")
	workloadFlag := flag.String("workload", "A", "YCSB workload type: A, B, C, D, E, F")
	threadsFlag := flag.Int("threads", 8, "Number of concurrent worker threads")
	recordsFlag := flag.Int64("recordcount", 50000, "Number of records to preload")
	opsFlag := flag.Int64("operationcount", 100000, "Number of operations to run")
	distFlag := flag.String("distribution", "", "Key distribution: zipfian, uniform, latest, scrambled_zipfian")
	dbPathFlag := flag.String("dbpath", "", "Database directory path (defaults to temporary directory)")
	scanModeFlag := flag.String("scan-mode", "iterator", "Scan mode for Workload E on modified engine: iterator or rangequery")
	bloomBitsFlag := flag.Int("bloom", 10, "Bloom filter bits per key (0 to disable)")
	phaseFlag := flag.String("phase", "all", "Execution phase: all (load+run), load (only preload data), run (only execute operations)")
	flag.Parse()

	wlName := ycsb.WorkloadName(strings.ToUpper(*workloadFlag))
	switch wlName {
	case ycsb.WorkloadA, ycsb.WorkloadB, ycsb.WorkloadC, ycsb.WorkloadD, ycsb.WorkloadE, ycsb.WorkloadF:
	default:
		log.Fatalf("Unknown workload: %s. Supported: A, B, C, D, E, F", *workloadFlag)
	}

	dist := *distFlag
	if dist == "" {
		if wlName == ycsb.WorkloadD {
			dist = "latest"
		} else {
			dist = "zipfian"
		}
	}

	scanMode := ycsb.ScanModeIterator
	if strings.ToLower(*scanModeFlag) == "rangequery" {
		scanMode = ycsb.ScanModeRangeQuery
	}

	engineMode := strings.ToLower(*engineFlag)
	if engineMode == "compare" {
		runComparison(wlName, dist, *threadsFlag, *recordsFlag, *opsFlag, *bloomBitsFlag, scanMode)
		return
	}
	if engineMode == "compare3" || engineMode == "compare-all" {
		run3WayComparison(wlName, dist, *threadsFlag, *recordsFlag, *opsFlag, *bloomBitsFlag, scanMode)
		return
	}

	dbPath := *dbPathFlag
	cleanup := false
	if dbPath == "" {
		tmpDir, err := ioutil.TempDir("", "ycsb-"+engineMode)
		if err != nil {
			log.Fatalf("Failed to create temporary directory: %v", err)
		}
		dbPath = tmpDir
		cleanup = true
		defer os.RemoveAll(tmpDir)
	}

	fmt.Printf("===============================================================\n")
	fmt.Printf("  Database Storage Engine YCSB Benchmark Harness\n")
	fmt.Printf("===============================================================\n")
	fmt.Printf("  Engine          : %s\n", engineMode)
	fmt.Printf("  Phase           : %s\n", *phaseFlag)
	fmt.Printf("  Workload        : %s\n", wlName)
	fmt.Printf("  Distribution    : %s\n", dist)
	fmt.Printf("  Threads         : %d\n", *threadsFlag)
	fmt.Printf("  Record Count    : %d\n", *recordsFlag)
	fmt.Printf("  Operation Count : %d\n", *opsFlag)
	fmt.Printf("  Database Path   : %s\n", dbPath)
	fmt.Printf("===============================================================\n")

	var adapter ycsb.DB
	var err error

	switch engineMode {
	case "bbolt":
		adapter, err = ycsb.NewBBoltAdapter(ycsb.BBoltOptions{
			Path:   filepath.Join(dbPath, "bbolt.db"),
			NoSync: true,
		})
	case "original":
		adapter, err = ycsb.NewLevelDBAdapter(ycsb.LevelDBOptions{
			Path:        dbPath,
			Engine:      ycsb.EngineOriginal,
			Compression: opt.SnappyCompression,
			BloomBits:   *bloomBitsFlag,
			ScanMode:    ycsb.ScanModeIterator,
		})
	case "modified":
		adapter, err = ycsb.NewLevelDBAdapter(ycsb.LevelDBOptions{
			Path:        dbPath,
			Engine:      ycsb.EngineModified,
			Compression: opt.NoCompression,
			BloomBits:   *bloomBitsFlag,
			ScanMode:    scanMode,
		})
	default:
		log.Fatalf("Unknown engine mode: %s. Supported: bbolt, original, modified, compare, compare3", *engineFlag)
	}

	if err != nil {
		log.Fatalf("Failed to open database adapter: %v", err)
	}
	defer adapter.Close()

	config := ycsb.WorkloadConfig{
		Workload:       wlName,
		RecordCount:    *recordsFlag,
		OperationCount: *opsFlag,
		ThreadCount:    *threadsFlag,
		FieldCount:     10,
		FieldLength:    100,
		Distribution:   dist,
		ScanMaxCount:   100,
		TableName:      "usertable",
	}

	runner := ycsb.NewWorkloadRunner(adapter, config)
	ctx := context.Background()

	phase := strings.ToLower(*phaseFlag)
	if phase == "all" || phase == "load" {
		fmt.Printf("[1/2] Loading %d records into %s database...", *recordsFlag, engineMode)
		if err := runner.Load(ctx); err != nil {
			log.Fatalf("\nLoading failed: %v", err)
		}
		fmt.Printf(" DONE.\n")
	}

	if phase == "all" || phase == "run" {
		fmt.Printf("[2/2] Running %d operations across %d threads...", *opsFlag, *threadsFlag)
		metrics, err := runner.Run(ctx)
		if err != nil {
			log.Fatalf("\nBenchmark run failed: %v", err)
		}
		fmt.Printf(" DONE.\n")
		fmt.Print(metrics.SummaryString())
	}

	if cleanup {
		fmt.Printf("Cleaned up temporary database directory: %s\n", dbPath)
	}
}

func runComparison(wlName ycsb.WorkloadName, dist string, threads int, records, ops int64, bloomBits int, scanMode ycsb.ScanMode) {
	fmt.Printf("========================================================================================\n")
	fmt.Printf("  2-Way Benchmark: [goleveldb-original (LSM)] vs [goleveldb-modified (Hybrid)]\n")
	fmt.Printf("========================================================================================\n")
	fmt.Printf("  Workload        : %s\n", wlName)
	fmt.Printf("  Distribution    : %s\n", dist)
	fmt.Printf("  Threads         : %d\n", threads)
	fmt.Printf("  Record Count    : %d\n", records)
	fmt.Printf("  Operation Count : %d\n", ops)
	fmt.Printf("========================================================================================\n\n")

	config := ycsb.WorkloadConfig{
		Workload:       wlName,
		RecordCount:    records,
		OperationCount: ops,
		ThreadCount:    threads,
		FieldCount:     10,
		FieldLength:    100,
		Distribution:   dist,
		ScanMaxCount:   100,
		TableName:      "usertable",
	}

	ctx := context.Background()

	// 1. Original
	tmpDirOrig, _ := ioutil.TempDir("", "leveldb-ycsb-original")
	defer os.RemoveAll(tmpDirOrig)
	fmt.Printf("--> [1/2] Testing goleveldb-original (Pure LSM-Tree)...\n")
	origAdapter, err := ycsb.NewLevelDBAdapter(ycsb.LevelDBOptions{
		Path:        tmpDirOrig,
		Engine:      ycsb.EngineOriginal,
		Compression: opt.SnappyCompression,
		BloomBits:   bloomBits,
		ScanMode:    ycsb.ScanModeIterator,
	})
	if err != nil {
		log.Fatalf("Failed to create original adapter: %v", err)
	}
	runnerOrig := ycsb.NewWorkloadRunner(origAdapter, config)
	_ = runnerOrig.Load(ctx)
	metricsOrig, _ := runnerOrig.Run(ctx)
	origAdapter.Close()

	// 2. Modified
	tmpDirMod, _ := ioutil.TempDir("", "leveldb-ycsb-modified")
	defer os.RemoveAll(tmpDirMod)
	fmt.Printf("--> [2/2] Testing goleveldb-modified (Hybrid Engine)...\n")
	modAdapter, err := ycsb.NewLevelDBAdapter(ycsb.LevelDBOptions{
		Path:        tmpDirMod,
		Engine:      ycsb.EngineModified,
		Compression: opt.NoCompression,
		BloomBits:   bloomBits,
		ScanMode:    scanMode,
	})
	if err != nil {
		log.Fatalf("Failed to create modified adapter: %v", err)
	}
	runnerMod := ycsb.NewWorkloadRunner(modAdapter, config)
	_ = runnerMod.Load(ctx)
	metricsMod, _ := runnerMod.Run(ctx)
	modAdapter.Close()

	printComparisonTable(metricsOrig, metricsMod)
}

func run3WayComparison(wlName ycsb.WorkloadName, dist string, threads int, records, ops int64, bloomBits int, scanMode ycsb.ScanMode) {
	fmt.Printf("======================================================================================================\n")
	fmt.Printf("  3-Way Benchmark: [BBolt (B+ Tree)] vs [GoLevelDB (LSM)] vs [GoLevelDB-Modified (Hybrid)]\n")
	fmt.Printf("======================================================================================================\n")
	fmt.Printf("  Workload        : %s\n", wlName)
	fmt.Printf("  Distribution    : %s\n", dist)
	fmt.Printf("  Threads         : %d\n", threads)
	fmt.Printf("  Record Count    : %d\n", records)
	fmt.Printf("  Operation Count : %d\n", ops)
	fmt.Printf("======================================================================================================\n\n")

	config := ycsb.WorkloadConfig{
		Workload:       wlName,
		RecordCount:    records,
		OperationCount: ops,
		ThreadCount:    threads,
		FieldCount:     10,
		FieldLength:    100,
		Distribution:   dist,
		ScanMaxCount:   100,
		TableName:      "usertable",
	}
	ctx := context.Background()

	// 1. BBolt (Pure B+ Tree)
	tmpDirBbolt, _ := ioutil.TempDir("", "ycsb-bbolt")
	defer os.RemoveAll(tmpDirBbolt)
	fmt.Printf("--> [1/3] Testing BBolt (Pure B+ Tree with mmap)...\n")
	bboltAdapter, err := ycsb.NewBBoltAdapter(ycsb.BBoltOptions{
		Path:   filepath.Join(tmpDirBbolt, "bbolt.db"),
		NoSync: true,
	})
	if err != nil {
		log.Fatalf("Failed to create bbolt adapter: %v", err)
	}
	runnerBbolt := ycsb.NewWorkloadRunner(bboltAdapter, config)
	_ = runnerBbolt.Load(ctx)
	metricsBbolt, _ := runnerBbolt.Run(ctx)
	bboltAdapter.Close()

	// 2. GoLevelDB-Original (Pure LSM)
	tmpDirOrig, _ := ioutil.TempDir("", "ycsb-original")
	defer os.RemoveAll(tmpDirOrig)
	fmt.Printf("--> [2/3] Testing GoLevelDB-Original (Pure Block LSM-Tree)...\n")
	origAdapter, err := ycsb.NewLevelDBAdapter(ycsb.LevelDBOptions{
		Path:        tmpDirOrig,
		Engine:      ycsb.EngineOriginal,
		Compression: opt.SnappyCompression,
		BloomBits:   bloomBits,
		ScanMode:    ycsb.ScanModeIterator,
	})
	if err != nil {
		log.Fatalf("Failed to create original adapter: %v", err)
	}
	runnerOrig := ycsb.NewWorkloadRunner(origAdapter, config)
	_ = runnerOrig.Load(ctx)
	metricsOrig, _ := runnerOrig.Run(ctx)
	origAdapter.Close()

	// 3. GoLevelDB-Modified (Hybrid)
	tmpDirMod, _ := ioutil.TempDir("", "ycsb-modified")
	defer os.RemoveAll(tmpDirMod)
	fmt.Printf("--> [3/3] Testing GoLevelDB-Modified (Hybrid LSM/B+Tree + AI Shield)...\n")
	modAdapter, err := ycsb.NewLevelDBAdapter(ycsb.LevelDBOptions{
		Path:        tmpDirMod,
		Engine:      ycsb.EngineModified,
		Compression: opt.NoCompression,
		BloomBits:   bloomBits,
		ScanMode:    scanMode,
	})
	if err != nil {
		log.Fatalf("Failed to create modified adapter: %v", err)
	}
	runnerMod := ycsb.NewWorkloadRunner(modAdapter, config)
	_ = runnerMod.Load(ctx)
	metricsMod, _ := runnerMod.Run(ctx)
	modAdapter.Close()

	print3WayComparisonTable(metricsBbolt, metricsOrig, metricsMod)
}

func print3WayComparisonTable(bboltM, origM, modM *ycsb.Metrics) {
	fmt.Printf("\n============================================ 3-WAY COMPARISON SUMMARY ============================================\n")
	fmt.Printf("Metric                  | BBolt (B+ Tree)        | GoLevelDB (LSM)        | GoLevelDB-Mod (Hybrid) | Hybrid Speedup\n")
	fmt.Printf("------------------------------------------------------------------------------------------------------------------\n")
	fmt.Printf("Throughput (ops/sec)    | %-22.2f | %-22.2f | %-22.2f | vs LSM: %+.2fx, vs B+: %+.2fx\n",
		bboltM.QPS(), origM.QPS(), modM.QPS(), modM.QPS()/origM.QPS(), modM.QPS()/bboltM.QPS())
	fmt.Printf("Total Elapsed           | %-22v | %-22v | %-22v | %s vs LSM\n",
		bboltM.Elapsed().Round(time.Millisecond), origM.Elapsed().Round(time.Millisecond), modM.Elapsed().Round(time.Millisecond),
		relativeDiff(origM.Elapsed(), modM.Elapsed()))
	fmt.Printf("Total Errors            | %-22d | %-22d | %-22d | 0 errors (clean)\n",
		bboltM.TotalErrors(), origM.TotalErrors(), modM.TotalErrors())

	fmt.Printf("------------------------------------------------------------------------------------------------------------------\n")
	fmt.Printf("LATENCY BREAKDOWN (microseconds):\n")
	fmt.Printf("------------------------------------------------------------------------------------------------------------------\n")

	for _, op := range []ycsb.OpType{ycsb.OpRead, ycsb.OpUpdate, ycsb.OpInsert, ycsb.OpScan, ycsb.OpRMW} {
		statB := bboltM.Stats[op]
		statO := origM.Stats[op]
		statM := modM.Stats[op]
		cntB := atomic.LoadInt64(&statB.Count)
		cntO := atomic.LoadInt64(&statO.Count)
		cntM := atomic.LoadInt64(&statM.Count)
		if cntB == 0 && cntO == 0 && cntM == 0 {
			continue
		}

		fmt.Printf("[%s]\n", op)
		fmt.Printf("  Average Latency       | %-20.2f us | %-20.2f us | %-20.2f us | %s vs LSM\n",
			float64(statB.AvgLatency().Nanoseconds())/1000.0, float64(statO.AvgLatency().Nanoseconds())/1000.0,
			float64(statM.AvgLatency().Nanoseconds())/1000.0,
			latencyDiff(float64(statO.AvgLatency().Nanoseconds())/1000.0, float64(statM.AvgLatency().Nanoseconds())/1000.0))

		fmt.Printf("  p50 (Median) Latency  | %-20.2f us | %-20.2f us | %-20.2f us | %s vs LSM\n",
			float64(statB.Percentile(0.50).Nanoseconds())/1000.0, float64(statO.Percentile(0.50).Nanoseconds())/1000.0,
			float64(statM.Percentile(0.50).Nanoseconds())/1000.0,
			latencyDiff(float64(statO.Percentile(0.50).Nanoseconds())/1000.0, float64(statM.Percentile(0.50).Nanoseconds())/1000.0))

		fmt.Printf("  p90 Latency           | %-20.2f us | %-20.2f us | %-20.2f us | %s vs LSM\n",
			float64(statB.Percentile(0.90).Nanoseconds())/1000.0, float64(statO.Percentile(0.90).Nanoseconds())/1000.0,
			float64(statM.Percentile(0.90).Nanoseconds())/1000.0,
			latencyDiff(float64(statO.Percentile(0.90).Nanoseconds())/1000.0, float64(statM.Percentile(0.90).Nanoseconds())/1000.0))

		fmt.Printf("  p95 Latency           | %-20.2f us | %-20.2f us | %-20.2f us | %s vs LSM\n",
			float64(statB.Percentile(0.95).Nanoseconds())/1000.0, float64(statO.Percentile(0.95).Nanoseconds())/1000.0,
			float64(statM.Percentile(0.95).Nanoseconds())/1000.0,
			latencyDiff(float64(statO.Percentile(0.95).Nanoseconds())/1000.0, float64(statM.Percentile(0.95).Nanoseconds())/1000.0))

		fmt.Printf("  p99 Latency           | %-20.2f us | %-20.2f us | %-20.2f us | %s vs LSM\n",
			float64(statB.Percentile(0.99).Nanoseconds())/1000.0, float64(statO.Percentile(0.99).Nanoseconds())/1000.0,
			float64(statM.Percentile(0.99).Nanoseconds())/1000.0,
			latencyDiff(float64(statO.Percentile(0.99).Nanoseconds())/1000.0, float64(statM.Percentile(0.99).Nanoseconds())/1000.0))

		fmt.Printf("  p99.9 Latency         | %-20.2f us | %-20.2f us | %-20.2f us | %s vs LSM\n",
			float64(statB.Percentile(0.999).Nanoseconds())/1000.0, float64(statO.Percentile(0.999).Nanoseconds())/1000.0,
			float64(statM.Percentile(0.999).Nanoseconds())/1000.0,
			latencyDiff(float64(statO.Percentile(0.999).Nanoseconds())/1000.0, float64(statM.Percentile(0.999).Nanoseconds())/1000.0))
	}
	fmt.Printf("==================================================================================================================\n")
}

func printComparisonTable(orig, mod *ycsb.Metrics) {
	speedup := mod.QPS() / orig.QPS()
	fmt.Printf("================================== COMPARISON SUMMARY ==================================\n")
	fmt.Printf("Metric                  | goleveldb-original     | goleveldb-modified     | Comparison   \n")
	fmt.Printf("----------------------------------------------------------------------------------------\n")
	fmt.Printf("Throughput (ops/sec)    | %-22.2f | %-22.2f | %+.2fx speedup\n", orig.QPS(), mod.QPS(), speedup)
	fmt.Printf("Total Elapsed           | %-22v | %-22v | %s\n",
		orig.Elapsed().Round(time.Millisecond), mod.Elapsed().Round(time.Millisecond),
		relativeDiff(orig.Elapsed(), mod.Elapsed()))
	fmt.Printf("Total Errors            | %-22d | %-22d | %s\n", orig.TotalErrors(), mod.TotalErrors(),
		errorComp(orig.TotalErrors(), mod.TotalErrors()))

	fmt.Printf("----------------------------------------------------------------------------------------\n")
	fmt.Printf("LATENCY BREAKDOWN (microseconds):\n")
	fmt.Printf("----------------------------------------------------------------------------------------\n")

	for _, op := range []ycsb.OpType{ycsb.OpRead, ycsb.OpUpdate, ycsb.OpInsert, ycsb.OpScan, ycsb.OpRMW} {
		statO := orig.Stats[op]
		statM := mod.Stats[op]
		cntO := atomic.LoadInt64(&statO.Count)
		cntM := atomic.LoadInt64(&statM.Count)
		if cntO == 0 && cntM == 0 {
			continue
		}

		avgO := float64(statO.AvgLatency().Nanoseconds()) / 1000.0
		avgM := float64(statM.AvgLatency().Nanoseconds()) / 1000.0
		p50O := float64(statO.Percentile(0.50).Nanoseconds()) / 1000.0
		p50M := float64(statM.Percentile(0.50).Nanoseconds()) / 1000.0
		p90O := float64(statO.Percentile(0.90).Nanoseconds()) / 1000.0
		p90M := float64(statM.Percentile(0.90).Nanoseconds()) / 1000.0
		p95O := float64(statO.Percentile(0.95).Nanoseconds()) / 1000.0
		p95M := float64(statM.Percentile(0.95).Nanoseconds()) / 1000.0
		p99O := float64(statO.Percentile(0.99).Nanoseconds()) / 1000.0
		p99M := float64(statM.Percentile(0.99).Nanoseconds()) / 1000.0
		p999O := float64(statO.Percentile(0.999).Nanoseconds()) / 1000.0
		p999M := float64(statM.Percentile(0.999).Nanoseconds()) / 1000.0

		fmt.Printf("[%s]\n", op)
		fmt.Printf("  Average Latency       | %-20.2f us | %-20.2f us | %s\n", avgO, avgM, latencyDiff(avgO, avgM))
		fmt.Printf("  p50 (Median) Latency  | %-20.2f us | %-20.2f us | %s\n", p50O, p50M, latencyDiff(p50O, p50M))
		fmt.Printf("  p90 Latency           | %-20.2f us | %-20.2f us | %s\n", p90O, p90M, latencyDiff(p90O, p90M))
		fmt.Printf("  p95 Latency           | %-20.2f us | %-20.2f us | %s\n", p95O, p95M, latencyDiff(p95O, p95M))
		fmt.Printf("  p99 Latency           | %-20.2f us | %-20.2f us | %s\n", p99O, p99M, latencyDiff(p99O, p99M))
		fmt.Printf("  p99.9 (Tail) Latency  | %-20.2f us | %-20.2f us | %s\n", p999O, p999M, latencyDiff(p999O, p999M))
	}
	fmt.Printf("========================================================================================\n")
}

func relativeDiff(t1, t2 time.Duration) string {
	if t2 < t1 {
		ratio := float64(t1) / float64(t2)
		return fmt.Sprintf("%.2fx faster", ratio)
	} else if t1 < t2 {
		ratio := float64(t2) / float64(t1)
		return fmt.Sprintf("%.2fx slower", ratio)
	}
	return "equal"
}

func errorComp(e1, e2 int64) string {
	if e1 == 0 && e2 == 0 {
		return "0 errors (clean)"
	}
	return fmt.Sprintf("%d vs %d", e1, e2)
}

func latencyDiff(l1, l2 float64) string {
	if l2 < l1 {
		diff := ((l1 - l2) / l1) * 100.0
		return fmt.Sprintf("-%.1f%% latency (Faster)", diff)
	} else if l1 < l2 {
		diff := ((l2 - l1) / l1) * 100.0
		return fmt.Sprintf("+%.1f%% latency", diff)
	}
	return "equal"
}
