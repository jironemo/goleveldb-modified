package ycsb

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type OpType string

const (
	OpRead   OpType = "READ"
	OpUpdate OpType = "UPDATE"
	OpInsert OpType = "INSERT"
	OpScan   OpType = "SCAN"
	OpRMW    OpType = "RMW"
	OpDelete OpType = "DELETE"
)

// OpStats stores latency measurements for a specific operation type.
type OpStats struct {
	Count       int64
	Errors      int64
	latencies   []time.Duration
	mu          sync.Mutex
	MinLatency  time.Duration
	MaxLatency  time.Duration
	TotalNano   int64
}

func newOpStats() *OpStats {
	return &OpStats{
		MinLatency: time.Duration(math.MaxInt64),
		latencies:  make([]time.Duration, 0, 10000),
	}
}

func (s *OpStats) Record(d time.Duration, err error) {
	atomic.AddInt64(&s.Count, 1)
	if err != nil {
		atomic.AddInt64(&s.Errors, 1)
	}

	nano := d.Nanoseconds()
	atomic.AddInt64(&s.TotalNano, nano)

	s.mu.Lock()
	if d < s.MinLatency {
		s.MinLatency = d
	}
	if d > s.MaxLatency {
		s.MaxLatency = d
	}
	s.latencies = append(s.latencies, d)
	s.mu.Unlock()
}

// Percentile returns the p-th percentile latency (e.g. 0.50, 0.95, 0.99, 0.999).
func (s *OpStats) Percentile(p float64) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.latencies)
	if n == 0 {
		return 0
	}

	sort.Slice(s.latencies, func(i, j int) bool {
		return s.latencies[i] < s.latencies[j]
	})

	idx := int(float64(n) * p)
	if idx >= n {
		idx = n - 1
	}
	return s.latencies[idx]
}

func (s *OpStats) AvgLatency() time.Duration {
	c := atomic.LoadInt64(&s.Count)
	if c <= 0 {
		return 0
	}
	tot := atomic.LoadInt64(&s.TotalNano)
	return time.Duration(tot / c)
}

// Metrics tracks benchmark-wide performance statistics.
type Metrics struct {
	StartTime time.Time
	EndTime   time.Time
	Stats     map[OpType]*OpStats
	mu        sync.RWMutex
}

func NewMetrics() *Metrics {
	m := &Metrics{
		Stats: make(map[OpType]*OpStats),
	}
	for _, op := range []OpType{OpRead, OpUpdate, OpInsert, OpScan, OpRMW, OpDelete} {
		m.Stats[op] = newOpStats()
	}
	return m
}

func (m *Metrics) Start() {
	m.StartTime = time.Now()
}

func (m *Metrics) Stop() {
	m.EndTime = time.Now()
}

func (m *Metrics) Record(op OpType, d time.Duration, err error) {
	m.mu.RLock()
	stat, ok := m.Stats[op]
	m.mu.RUnlock()
	if ok {
		stat.Record(d, err)
	}
}

func (m *Metrics) TotalOps() int64 {
	var total int64
	for _, s := range m.Stats {
		total += atomic.LoadInt64(&s.Count)
	}
	return total
}

func (m *Metrics) TotalErrors() int64 {
	var total int64
	for _, s := range m.Stats {
		total += atomic.LoadInt64(&s.Errors)
	}
	return total
}

func (m *Metrics) Elapsed() time.Duration {
	if m.EndTime.IsZero() {
		return time.Since(m.StartTime)
	}
	return m.EndTime.Sub(m.StartTime)
}

func (m *Metrics) QPS() float64 {
	el := m.Elapsed().Seconds()
	if el <= 0 {
		return 0
	}
	return float64(m.TotalOps()) / el
}

func (m *Metrics) SummaryString() string {
	var res string
	res += fmt.Sprintf("\n=================== YCSB Benchmark Summary ===================\n")
	res += fmt.Sprintf("Elapsed Time : %v\n", m.Elapsed().Round(time.Millisecond))
	res += fmt.Sprintf("Total Ops    : %d\n", m.TotalOps())
	res += fmt.Sprintf("Total Errors : %d\n", m.TotalErrors())
	res += fmt.Sprintf("Throughput   : %.2f ops/sec\n", m.QPS())
	res += fmt.Sprintf("----------------------------------------------------------------------------------------------------\n")
	res += fmt.Sprintf("%-10s | %-8s | %-8s | %-10s | %-10s | %-10s | %-10s | %-10s\n",
		"Operation", "Count", "Errors", "Avg (us)", "p50 (us)", "p95 (us)", "p99 (us)", "p99.9 (us)")
	res += fmt.Sprintf("----------------------------------------------------------------------------------------------------\n")

	for _, op := range []OpType{OpRead, OpUpdate, OpInsert, OpScan, OpRMW, OpDelete} {
		stat := m.Stats[op]
		cnt := atomic.LoadInt64(&stat.Count)
		if cnt == 0 {
			continue
		}
		errs := atomic.LoadInt64(&stat.Errors)
		avgUs := float64(stat.AvgLatency().Nanoseconds()) / 1000.0
		p50Us := float64(stat.Percentile(0.50).Nanoseconds()) / 1000.0
		p95Us := float64(stat.Percentile(0.95).Nanoseconds()) / 1000.0
		p99Us := float64(stat.Percentile(0.99).Nanoseconds()) / 1000.0
		p999Us := float64(stat.Percentile(0.999).Nanoseconds()) / 1000.0

		res += fmt.Sprintf("%-10s | %-8d | %-8d | %-10.2f | %-10.2f | %-10.2f | %-10.2f | %-10.2f\n",
			op, cnt, errs, avgUs, p50Us, p95Us, p99Us, p999Us)
	}
	res += fmt.Sprintf("====================================================================================================\n")
	return res
}
