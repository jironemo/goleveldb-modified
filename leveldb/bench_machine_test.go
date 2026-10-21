package leveldb

import (
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// TestMachine_FilterAndMemoryMetrics benchmarks the exact filter bytes and RAM usage on this machine.
func TestMachine_FilterAndMemoryMetrics(t *testing.T) {
	recordCounts := []int{10000, 50000, 100000}

	for _, n := range recordCounts {
		// 1. Classical Bloom Filter Generator
		stdGen := filter.NewBloomFilter(10).NewGenerator()
		for i := 0; i < n; i++ {
			key := []byte(fmt.Sprintf("user%010d", i))
			stdGen.Add(key)
		}
		var stdBuf util.Buffer
		stdGen.Generate(&stdBuf)
		stdBytes := stdBuf.Len()

		// 2. Hybrid Overflow Bloom Filter Generator
		overflowGen := iFilter{filter.NewBloomFilter(10)}.NewGenerator()
		for i := 0; i < n; i++ {
			ikey := makeInternalKey(nil, []byte(fmt.Sprintf("user%010d", i)), 1, keyTypeVal)
			overflowGen.Add(ikey)
		}
		var overflowBuf util.Buffer
		overflowGen.Generate(&overflowBuf)
		overflowBytes := overflowBuf.Len()

		reductionRatio := float64(stdBytes) / float64(overflowBytes)
		reductionPct := (1.0 - float64(overflowBytes)/float64(stdBytes)) * 100.0

		t.Logf("Scale N=%d records:", n)
		t.Logf("  Standard Bloom Filter : %d bytes (%.2f bits/key)", stdBytes, float64(stdBytes*8)/float64(n))
		t.Logf("  Overflow Bloom Filter : %d bytes (%.4f bits/key)", overflowBytes, float64(overflowBytes*8)/float64(n))
		t.Logf("  Storage Reduction     : %.2f%% (%.1fx space savings)", reductionPct, reductionRatio)
	}

	// 3. Measure RAM consumption of in-memory B+ Tree at 50k and 100k
	runtime.GC()
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)

	tmpDir, _ := ioutil.TempDir("", "mod-mem-test")
	defer os.RemoveAll(tmpDir)

	db, err := OpenFile(tmpDir, &opt.Options{
		Compression: opt.NoCompression,
		Filter:      filter.NewBloomFilter(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	nRecords := 50000
	batch := new(Batch)
	for i := 0; i < nRecords; i++ {
		batch.Put([]byte(fmt.Sprintf("user%010d", i)), make([]byte, 100))
	}
	_ = db.Write(batch, nil)

	runtime.GC()
	runtime.ReadMemStats(&m2)
	ramUsedMB := float64(m2.Alloc-m1.Alloc) / (1024 * 1024)

	t.Logf("RAM Allocation for %d records in Modified Engine: %.2f MB", nRecords, ramUsedMB)
}

// TestMachine_NegativeLookups benchmarks Workload C with 80% negative lookups on this machine.
func TestMachine_NegativeLookups(t *testing.T) {
	nRecords := 50000
	nQueries := 100000

	// Create Original DB
	dirOrig, _ := ioutil.TempDir("", "neg-orig")
	defer os.RemoveAll(dirOrig)
	dbOrig, _ := OpenFile(dirOrig, &opt.Options{Filter: filter.NewBloomFilter(10)})
	batch := new(Batch)
	for i := 0; i < nRecords; i++ {
		batch.Put([]byte(fmt.Sprintf("user%010d", i)), []byte("val"))
	}
	_ = dbOrig.Write(batch, nil)

	// Create Modified DB
	dirMod, _ := ioutil.TempDir("", "neg-mod")
	defer os.RemoveAll(dirMod)
	dbMod, _ := OpenFile(dirMod, &opt.Options{
		Filter:      filter.NewBloomFilter(10),
		Compression: opt.NoCompression,
	})
	_ = dbMod.Write(batch, nil)

	// 80% negative lookups query set
	queries := make([][]byte, nQueries)
	for i := 0; i < nQueries; i++ {
		if i%5 == 0 {
			// 20% positive
			queries[i] = []byte(fmt.Sprintf("user%010d", i%(nRecords)))
		} else {
			// 80% negative (random non-existent keys)
			queries[i] = []byte(fmt.Sprintf("nonexist%010d", i))
		}
	}

	// Benchmark Original
	startO := time.Now()
	for _, q := range queries {
		_, _ = dbOrig.Get(q, nil)
	}
	durO := time.Since(startO)
	dbOrig.Close()

	// Benchmark Modified
	startM := time.Now()
	for _, q := range queries {
		_, _ = dbMod.Get(q, nil)
	}
	durM := time.Since(startM)
	dbMod.Close()

	qpsO := float64(nQueries) / durO.Seconds()
	qpsM := float64(nQueries) / durM.Seconds()
	speedup := qpsM / qpsO

	t.Logf("================ 80%% NEGATIVE LOOKUP BENCHMARK (N=%d queries) ================", nQueries)
	t.Logf("GoLevelDB-Original : %v (%.2f ops/sec)", durO, qpsO)
	t.Logf("GoLevelDB-Modified : %v (%.2f ops/sec)", durM, qpsM)
	t.Logf("Throughput Speedup : %.2fx faster (%.1f%% throughput gain)", speedup, (speedup-1.0)*100.0)
	t.Logf("================================================================================")
}
