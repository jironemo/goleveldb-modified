package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/learned"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
	"github.com/syndtr/goleveldb/ycsb"
)

type LatencyStatsJSON struct {
	Count   int64   `json:"count"`
	Errors  int64   `json:"errors"`
	MinUs   float64 `json:"min_us"`
	AvgUs   float64 `json:"avg_us"`
	P50Us   float64 `json:"p50_us"`
	P90Us   float64 `json:"p90_us"`
	P95Us   float64 `json:"p95_us"`
	P99Us   float64 `json:"p99_us"`
	P999Us  float64 `json:"p999_us"`
	MaxUs   float64 `json:"max_us"`
}

type EngineResultJSON struct {
	Engine      string                      `json:"engine"`
	QPS         float64                     `json:"qps"`
	ElapsedMs   float64                     `json:"elapsed_ms"`
	TotalOps    int64                       `json:"total_ops"`
	TotalErrors int64                       `json:"total_errors"`
	OpLatencies map[string]LatencyStatsJSON `json:"op_latencies"`
}

type WorkloadResultJSON struct {
	Workload     string                      `json:"workload"`
	Description  string                      `json:"description"`
	Distribution string                      `json:"distribution"`
	Records      int64                       `json:"records"`
	Operations   int64                       `json:"operations"`
	Threads      int                         `json:"threads"`
	Engines      map[string]EngineResultJSON `json:"engines"`
}

type FilterStatsJSON struct {
	TotalKeys          int64   `json:"total_keys"`
	MLPFastPathKeys    int64   `json:"mlp_fast_path_keys"`
	OverflowBloomKeys  int64   `json:"overflow_bloom_keys"`
	FastPathPct        float64 `json:"fast_path_pct"`
	OverflowPct        float64 `json:"overflow_pct"`
	StandardBloomBytes int     `json:"standard_bloom_bytes"`
	OverflowBloomBytes int     `json:"overflow_bloom_bytes"`
	SpaceSavedPct      float64 `json:"space_saved_pct"`
}

type BenchmarkRunJSON struct {
	RunIndex  int                  `json:"run_index"`
	Timestamp string               `json:"timestamp"`
	Workloads []WorkloadResultJSON `json:"workloads"`
}

type AggregatedEngineStatsJSON struct {
	Engine       string                      `json:"engine"`
	MeanQPS      float64                     `json:"mean_qps"`
	StdDevQPS    float64                     `json:"stddev_qps"`
	MinQPS       float64                     `json:"min_qps"`
	MaxQPS       float64                     `json:"max_qps"`
	MeanElapsed  float64                     `json:"mean_elapsed_ms"`
	IndividualQPS []float64                  `json:"individual_qps"`
	OpLatencies  map[string]LatencyStatsJSON `json:"op_latencies"`
}

type AggregatedWorkloadJSON struct {
	Workload     string                               `json:"workload"`
	Description  string                               `json:"description"`
	Distribution string                               `json:"distribution"`
	Engines      map[string]AggregatedEngineStatsJSON `json:"engines"`
}

type MultiRunSuiteJSON struct {
	Timestamp      string                   `json:"timestamp"`
	Platform       string                   `json:"platform"`
	RecordCount    int64                    `json:"record_count"`
	OperationCount int64                    `json:"operation_count"`
	ThreadCount    int                      `json:"thread_count"`
	TotalRuns      int                      `json:"total_runs"`
	FilterStats    FilterStatsJSON          `json:"filter_stats"`
	Summary        []AggregatedWorkloadJSON `json:"summary"`
	Runs           []BenchmarkRunJSON       `json:"runs"`
	Workloads      []WorkloadResultJSON     `json:"workloads"` // for backwards compatibility with single-run visualizers
}

func extractStats(m *ycsb.Metrics, engineName string) EngineResultJSON {
	res := EngineResultJSON{
		Engine:      engineName,
		QPS:         m.QPS(),
		ElapsedMs:   float64(m.Elapsed().Nanoseconds()) / 1e6,
		TotalOps:    m.TotalOps(),
		TotalErrors: m.TotalErrors(),
		OpLatencies: make(map[string]LatencyStatsJSON),
	}

	for _, op := range []ycsb.OpType{ycsb.OpRead, ycsb.OpUpdate, ycsb.OpInsert, ycsb.OpScan, ycsb.OpRMW} {
		stat := m.Stats[op]
		cnt := atomic.LoadInt64(&stat.Count)
		if cnt == 0 {
			continue
		}
		res.OpLatencies[string(op)] = LatencyStatsJSON{
			Count:  cnt,
			Errors: atomic.LoadInt64(&stat.Errors),
			MinUs:  float64(stat.MinLatency.Nanoseconds()) / 1000.0,
			AvgUs:  float64(stat.AvgLatency().Nanoseconds()) / 1000.0,
			P50Us:  float64(stat.Percentile(0.50).Nanoseconds()) / 1000.0,
			P90Us:  float64(stat.Percentile(0.90).Nanoseconds()) / 1000.0,
			P95Us:  float64(stat.Percentile(0.95).Nanoseconds()) / 1000.0,
			P99Us:  float64(stat.Percentile(0.99).Nanoseconds()) / 1000.0,
			P999Us: float64(stat.Percentile(0.999).Nanoseconds()) / 1000.0,
			MaxUs:  float64(stat.MaxLatency.Nanoseconds()) / 1000.0,
		}
	}
	return res
}

func main() {
	var numRuns int
	var records int64
	var ops int64
	var threads int
	var outPath string
	var targetWorkload string
	var negativeRatio float64
	var origNoCompression bool

	flag.IntVar(&numRuns, "runs", 10, "Number of benchmark iterations")
	flag.Int64Var(&records, "records", 100000, "Number of preloaded records")
	flag.Int64Var(&ops, "ops", 100000, "Number of operations per workload")
	flag.IntVar(&threads, "threads", 8, "Number of worker threads")
	flag.StringVar(&outPath, "out", "ycsb_10runs_results.json", "Output JSON path")
	flag.StringVar(&targetWorkload, "workload", "ALL", "Workload to run: A, B, C, D, E, F, or ALL")
	flag.Float64Var(&negativeRatio, "negative-ratio", 0.0, "Fraction of read operations that are negative lookups (e.g. 0.80)")
	flag.BoolVar(&origNoCompression, "orig-no-compression", false, "Disable compression on original LevelDB")
	flag.Parse()

	fmt.Printf("========================================================================================\n")
	fmt.Printf("        MULTI-RUN YCSB BENCHMARK SUITE: %d ITERATIONS (N=%d RECORDS)                   \n", numRuns, records)
	fmt.Printf("========================================================================================\n")

	// Compute Filter Statistics
	learnedHits := int64(0)
	overflowHits := int64(0)
	bfGen := filter.NewBloomFilter(10).NewGenerator()
	ofGen := filter.NewBloomFilter(10).NewGenerator()

	for i := int64(0); i < records; i++ {
		keyStr := fmt.Sprintf("usertable:%s", ycsb.FormatKey(i))
		k := []byte(keyStr)
		bfGen.Add(k)

		prob := learned.PredictExistence(k)
		if prob >= learned.Tau {
			learnedHits++
		} else {
			overflowHits++
			ofGen.Add(k)
		}
	}

	buf1 := &util.Buffer{}
	bfGen.Generate(buf1)
	buf2 := &util.Buffer{}
	ofGen.Generate(buf2)

	spaceSavedPct := (1.0 - float64(buf2.Len())/float64(buf1.Len())) * 100.0

	filterStats := FilterStatsJSON{
		TotalKeys:          records,
		MLPFastPathKeys:    learnedHits,
		OverflowBloomKeys:  overflowHits,
		FastPathPct:        float64(learnedHits) / float64(records) * 100.0,
		OverflowPct:        float64(overflowHits) / float64(records) * 100.0,
		StandardBloomBytes: buf1.Len(),
		OverflowBloomBytes: buf2.Len(),
		SpaceSavedPct:      spaceSavedPct,
	}

	fmt.Printf("  • Total Preloaded Keys             : %d\n", records)
	fmt.Printf("  • MLP Fast-Path Keys (Prob >= 0.60): %d (%.4f%%)\n", learnedHits, filterStats.FastPathPct)
	fmt.Printf("  • Overflow Bloom Keys (Prob < 0.60): %d (%.4f%%)\n", overflowHits, filterStats.OverflowPct)
	fmt.Printf("  • On-Disk Filter Space Reduction   : %.2f%% Space Saved!\n\n", spaceSavedPct)

	workloads := []struct {
		name ycsb.WorkloadName
		desc string
		dist string
	}{
		{ycsb.WorkloadA, "50% Read, 50% Update (Session store)", "zipfian"},
		{ycsb.WorkloadB, "95% Read, 5% Update (Photo tagging / WORE)", "zipfian"},
		{ycsb.WorkloadC, "100% Read-Only (User authentication)", "zipfian"},
		{ycsb.WorkloadD, "95% Read, 5% Insert (Latest activity)", "latest"},
		{ycsb.WorkloadE, "95% Range Scan, 5% Insert (Time-series / Threads)", "zipfian"},
		{ycsb.WorkloadF, "50% Read, 50% Read-Modify-Write (Bank transactions)", "zipfian"},
	}

	if strings.ToUpper(targetWorkload) != "ALL" && targetWorkload != "" {
		var filtered []struct {
			name ycsb.WorkloadName
			desc string
			dist string
		}
		for _, wl := range workloads {
			if strings.EqualFold(string(wl.name), targetWorkload) {
				filtered = append(filtered, wl)
			}
		}
		if len(filtered) == 0 {
			log.Fatalf("Unknown workload: %s. Supported: A, B, C, D, E, F, ALL", targetWorkload)
		}
		workloads = filtered
	}

	allRuns := make([]BenchmarkRunJSON, 0, numRuns)

	for r := 1; r <= numRuns; r++ {
		fmt.Printf("\n>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>> STARTING RUN %d OF %d <<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<\n", r, numRuns)
		runStart := time.Now()
		runResult := BenchmarkRunJSON{
			RunIndex:  r,
			Timestamp: time.Now().Format(time.RFC3339),
			Workloads: make([]WorkloadResultJSON, 0, len(workloads)),
		}

		for idx, wl := range workloads {
			fmt.Printf("  [Run %d/%d] [%d/6] Workload %s (%s)...\n", r, numRuns, idx+1, wl.name, wl.desc)

			config := ycsb.WorkloadConfig{
				Workload:       wl.name,
				RecordCount:    records,
				OperationCount: ops,
				ThreadCount:    threads,
				FieldCount:     10,
				FieldLength:    100,
				Distribution:   wl.dist,
				ScanMaxCount:   100,
				TableName:      "usertable",
				NegativeRatio:  negativeRatio,
			}
			ctx := context.Background()
			wlRes := WorkloadResultJSON{
				Workload:     string(wl.name),
				Description:  wl.desc,
				Distribution: wl.dist,
				Records:      records,
				Operations:   ops,
				Threads:      threads,
				Engines:      make(map[string]EngineResultJSON),
			}

			// 1. BBolt
			tmpDirBbolt, _ := ioutil.TempDir("", "ycsb-bbolt")
			bboltAdapter, err := ycsb.NewBBoltAdapter(ycsb.BBoltOptions{
				Path:   filepath.Join(tmpDirBbolt, "bbolt.db"),
				NoSync: true,
			})
			if err != nil {
				log.Fatalf("failed bbolt: %v", err)
			}
			runnerBbolt := ycsb.NewWorkloadRunner(bboltAdapter, config)
			_ = runnerBbolt.Load(ctx)
			mBbolt, _ := runnerBbolt.Run(ctx)
			bboltAdapter.Close()
			os.RemoveAll(tmpDirBbolt)
			wlRes.Engines["bbolt"] = extractStats(mBbolt, "bbolt")
			runtime.GC()
			debug.FreeOSMemory()

			// 2. Original LSM
			tmpDirOrig, _ := ioutil.TempDir("", "ycsb-original")
			origComp := opt.SnappyCompression
			if origNoCompression {
				origComp = opt.NoCompression
			}
			origAdapter, err := ycsb.NewLevelDBAdapter(ycsb.LevelDBOptions{
				Path:        tmpDirOrig,
				Engine:      ycsb.EngineOriginal,
				Compression: origComp,
				BloomBits:   10,
				ScanMode:    ycsb.ScanModeIterator,
			})
			if err != nil {
				log.Fatalf("failed orig: %v", err)
			}
			compStr := "Snappy"
			if origNoCompression {
				compStr = "NoCompression"
			}
			fmt.Printf(" [Original LSM (%s)] Starting Load (%d records)...\n", compStr, config.RecordCount)
			runnerOrig := ycsb.NewWorkloadRunner(origAdapter, config)
			_ = runnerOrig.Load(ctx)
			mOrig, _ := runnerOrig.Run(ctx)
			origAdapter.Close()
			os.RemoveAll(tmpDirOrig)
			wlRes.Engines["original"] = extractStats(mOrig, "original")
			runtime.GC()
			debug.FreeOSMemory()

			// 3. Modified Hybrid
			tmpDirMod, _ := ioutil.TempDir("", "ycsb-modified")
			modAdapter, err := ycsb.NewLevelDBAdapter(ycsb.LevelDBOptions{
				Path:        tmpDirMod,
				Engine:      ycsb.EngineModified,
				Compression: opt.NoCompression,
				BloomBits:   10,
				ScanMode:    ycsb.ScanModeIterator,
			})
			if err != nil {
				log.Fatalf("failed mod: %v", err)
			}
			runnerMod := ycsb.NewWorkloadRunner(modAdapter, config)
			_ = runnerMod.Load(ctx)
			mMod, _ := runnerMod.Run(ctx)
			modAdapter.Close()
			os.RemoveAll(tmpDirMod)
			wlRes.Engines["modified"] = extractStats(mMod, "modified")
			runtime.GC()
			debug.FreeOSMemory()

			speedup := mMod.QPS() / mOrig.QPS()
			fmt.Printf("    -> Done: BBolt=%.0f QPS | Original=%.0f QPS | Modified=%.0f QPS (Speedup: %.2fx)\n",
				mBbolt.QPS(), mOrig.QPS(), mMod.QPS(), speedup)

			runResult.Workloads = append(runResult.Workloads, wlRes)
		}

		allRuns = append(allRuns, runResult)
		fmt.Printf(">>> Completed Run %d in %.2fs\n", r, time.Since(runStart).Seconds())
	}

	// Compute Averages & Statistics across all runs
	fmt.Printf("\n========================================================================================\n")
	fmt.Printf("                   COMPUTING STATISTICAL AVERAGES ACROSS ALL %d RUNS                    \n", numRuns)
	fmt.Printf("========================================================================================\n")

	aggregatedSummary := make([]AggregatedWorkloadJSON, 0, len(workloads))
	compatWorkloads := make([]WorkloadResultJSON, 0, len(workloads))

	for wIdx, wl := range workloads {
		wName := string(wl.name)
		aggWl := AggregatedWorkloadJSON{
			Workload:     wName,
			Description:  wl.desc,
			Distribution: wl.dist,
			Engines:      make(map[string]AggregatedEngineStatsJSON),
		}

		compatWl := WorkloadResultJSON{
			Workload:     wName,
			Description:  wl.desc,
			Distribution: wl.dist,
			Records:      records,
			Operations:   ops,
			Threads:      threads,
			Engines:      make(map[string]EngineResultJSON),
		}

		for _, eng := range []string{"bbolt", "original", "modified"} {
			var qpsList []float64
			var elapsedList []float64
			opLatenciesAcc := make(map[string][]LatencyStatsJSON)

			for r := 0; r < numRuns; r++ {
				eRes := allRuns[r].Workloads[wIdx].Engines[eng]
				qpsList = append(qpsList, eRes.QPS)
				elapsedList = append(elapsedList, eRes.ElapsedMs)

				for opName, latStat := range eRes.OpLatencies {
					opLatenciesAcc[opName] = append(opLatenciesAcc[opName], latStat)
				}
			}

			// Compute Mean, StdDev, Min, Max QPS
			sumQPS := 0.0
			minQPS := qpsList[0]
			maxQPS := qpsList[0]
			for _, q := range qpsList {
				sumQPS += q
				if q < minQPS {
					minQPS = q
				}
				if q > maxQPS {
					maxQPS = q
				}
			}
			meanQPS := sumQPS / float64(numRuns)

			varSum := 0.0
			for _, q := range qpsList {
				diff := q - meanQPS
				varSum += diff * diff
			}
			stdDevQPS := math.Sqrt(varSum / float64(numRuns))

			sumElapsed := 0.0
			for _, el := range elapsedList {
				sumElapsed += el
			}
			meanElapsed := sumElapsed / float64(numRuns)

			// Compute Mean Op Latencies
			meanOpLatencies := make(map[string]LatencyStatsJSON)
			for opName, statsList := range opLatenciesAcc {
				nStats := float64(len(statsList))
				var sumAvg, sumP50, sumP90, sumP95, sumP99, sumP999, sumMax, sumMin float64
				var totCnt, totErr int64

				for _, s := range statsList {
					totCnt += s.Count
					totErr += s.Errors
					sumMin += s.MinUs
					sumAvg += s.AvgUs
					sumP50 += s.P50Us
					sumP90 += s.P90Us
					sumP95 += s.P95Us
					sumP99 += s.P99Us
					sumP999 += s.P999Us
					sumMax += s.MaxUs
				}

				meanOpLatencies[opName] = LatencyStatsJSON{
					Count:  totCnt / int64(numRuns),
					Errors: totErr / int64(numRuns),
					MinUs:  sumMin / nStats,
					AvgUs:  sumAvg / nStats,
					P50Us:  sumP50 / nStats,
					P90Us:  sumP90 / nStats,
					P95Us:  sumP95 / nStats,
					P99Us:  sumP99 / nStats,
					P999Us: sumP999 / nStats,
					MaxUs:  sumMax / nStats,
				}
			}

			aggWl.Engines[eng] = AggregatedEngineStatsJSON{
				Engine:        eng,
				MeanQPS:       meanQPS,
				StdDevQPS:     stdDevQPS,
				MinQPS:        minQPS,
				MaxQPS:        maxQPS,
				MeanElapsed:   meanElapsed,
				IndividualQPS: qpsList,
				OpLatencies:   meanOpLatencies,
			}

			compatWl.Engines[eng] = EngineResultJSON{
				Engine:      eng,
				QPS:         meanQPS,
				ElapsedMs:   meanElapsed,
				TotalOps:    ops,
				TotalErrors: 0,
				OpLatencies: meanOpLatencies,
			}
		}

		origMean := aggWl.Engines["original"].MeanQPS
		modMean := aggWl.Engines["modified"].MeanQPS
		speedup := modMean / origMean
		fmt.Printf("Workload %s Summary (%d Runs):\n", wName, numRuns)
		fmt.Printf("  • Original LSM Mean QPS: %.1f ± %.1f\n", origMean, aggWl.Engines["original"].StdDevQPS)
		fmt.Printf("  • Modified Hybrid Mean QPS: %.1f ± %.1f (Speedup vs LSM: %.2fx)\n", modMean, aggWl.Engines["modified"].StdDevQPS, speedup)
		fmt.Printf("  • BBolt Mean QPS: %.1f ± %.1f\n\n", aggWl.Engines["bbolt"].MeanQPS, aggWl.Engines["bbolt"].StdDevQPS)

		aggregatedSummary = append(aggregatedSummary, aggWl)
		compatWorkloads = append(compatWorkloads, compatWl)
	}

	multiSuite := MultiRunSuiteJSON{
		Timestamp:      time.Now().Format(time.RFC3339),
		Platform:       "Apple Silicon (M-Series, 10-Core)",
		RecordCount:    records,
		OperationCount: ops,
		ThreadCount:    threads,
		TotalRuns:      numRuns,
		FilterStats:    filterStats,
		Summary:        aggregatedSummary,
		Runs:           allRuns,
		Workloads:      compatWorkloads,
	}

	data, err := json.MarshalIndent(multiSuite, "", "  ")
	if err != nil {
		log.Fatalf("failed to marshal json: %v", err)
	}
	if err := ioutil.WriteFile(outPath, data, 0644); err != nil {
		log.Fatalf("failed to write %s: %v", outPath, err)
	}
	// Also update ycsb_results_100k.json with the 10-run averaged results if running 100k
	if records == 100000 {
		_ = ioutil.WriteFile("ycsb_results_100k.json", data, 0644)
	}

	fmt.Printf("========================================================================================\n")
	fmt.Printf("  ALL %d BENCHMARK RUNS FINISHED SUCCESSFULLY! Data saved to %s\n", numRuns, outPath)
	fmt.Printf("========================================================================================\n")
}
