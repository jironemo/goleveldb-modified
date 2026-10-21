package ycsb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// Generator is the interface for generating numeric values.
type Generator interface {
	Next() int64
	Last() int64
}

// UniformGenerator generates numbers uniformly in range [min, max].
type UniformGenerator struct {
	min int64
	max int64
	rng *rand.Rand
	mu  sync.Mutex
}

func NewUniformGenerator(min, max int64) *UniformGenerator {
	return &UniformGenerator{
		min: min,
		max: max,
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (g *UniformGenerator) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.max <= g.min {
		return g.min
	}
	return g.min + g.rng.Int63n(g.max-g.min+1)
}

func (g *UniformGenerator) Last() int64 {
	return g.min
}

// ZipfianGenerator generates numbers following a Zipfian distribution (skewed).
type ZipfianGenerator struct {
	items            int64
	base             int64
	zipfianConstant  float64
	alpha            float64
	zetan            float64
	eta              float64
	theta            float64
	zeta2theta       float64
	countforzeta     int64
	lastVal          int64
	rng              *rand.Rand
	mu               sync.Mutex
}

const (
	ZipfianConstant = 0.99
)

func NewZipfianGenerator(min, max int64) *ZipfianGenerator {
	items := max - min + 1
	zg := &ZipfianGenerator{
		items:           items,
		base:            min,
		zipfianConstant: ZipfianConstant,
		theta:           ZipfianConstant,
		rng:             rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	zg.zeta2theta = zg.zeta(0, 2, zg.theta, 0)
	zg.alpha = 1.0 / (1.0 - zg.theta)
	zg.zetan = zg.zetastatic(0, items, zg.theta, 0)
	zg.countforzeta = items
	zg.eta = (1 - math.Pow(2.0/float64(items), 1-zg.theta)) / (1 - zg.zeta2theta/zg.zetan)

	zg.Next()
	return zg
}

func (g *ZipfianGenerator) zeta(st, n int64, thetaVal, initialSum float64) float64 {
	g.countforzeta = n
	return g.zetastatic(st, n, thetaVal, initialSum)
}

func (g *ZipfianGenerator) zetastatic(st, n int64, thetaVal, initialSum float64) float64 {
	sum := initialSum
	for i := st; i < n; i++ {
		sum += 1.0 / math.Pow(float64(i+1), thetaVal)
	}
	return sum
}

func (g *ZipfianGenerator) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	u := g.rng.Float64()
	uz := u * g.zetan

	var val int64
	if uz < 1.0 {
		val = g.base
	} else if uz < 1.0+math.Pow(0.5, g.theta) {
		val = g.base + 1
	} else {
		val = g.base + int64(float64(g.items)*math.Pow(g.eta*u-g.eta+1, g.alpha))
	}
	g.lastVal = val
	return val
}

func (g *ZipfianGenerator) Last() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastVal
}

// ScrambledZipfianGenerator distributes the hot keys uniformly across the key space.
type ScrambledZipfianGenerator struct {
	zg  *ZipfianGenerator
	min int64
	max int64
}

func NewScrambledZipfianGenerator(min, max int64) *ScrambledZipfianGenerator {
	return &ScrambledZipfianGenerator{
		zg:  NewZipfianGenerator(0, max-min),
		min: min,
		max: max,
	}
}

func (g *ScrambledZipfianGenerator) Next() int64 {
	k := g.zg.Next()
	// FNV hash scramble
	h := fnvHash64(k)
	return g.min + int64(math.Abs(float64(h%(g.max-g.min+1))))
}

func (g *ScrambledZipfianGenerator) Last() int64 {
	return g.zg.Last()
}

func fnvHash64(val int64) int64 {
	var h uint64 = 14695981039346656037
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(val))
	for _, byteVal := range b {
		h ^= uint64(byteVal)
		h *= 1099511628211
	}
	return int64(h)
}

// LatestGenerator generates numbers biased towards the most recently inserted items.
type LatestGenerator struct {
	zg      *ZipfianGenerator
	counter *CounterGenerator
	max     int64
}

func NewLatestGenerator(counter *CounterGenerator) *LatestGenerator {
	lg := &LatestGenerator{
		counter: counter,
		max:     counter.Last(),
	}
	lg.zg = NewZipfianGenerator(0, lg.max)
	return lg
}

func (g *LatestGenerator) Next() int64 {
	maxVal := g.counter.Last()
	if maxVal > g.max {
		g.zg = NewZipfianGenerator(0, maxVal)
		g.max = maxVal
	}
	offset := g.zg.Next()
	return maxVal - offset
}

func (g *LatestGenerator) Last() int64 {
	return g.counter.Last()
}

// CounterGenerator sequentially increments numbers thread-safely.
type CounterGenerator struct {
	count int64
}

func NewCounterGenerator(start int64) *CounterGenerator {
	return &CounterGenerator{count: start}
}

func (g *CounterGenerator) Next() int64 {
	return atomic.AddInt64(&g.count, 1) - 1
}

func (g *CounterGenerator) Last() int64 {
	return atomic.LoadInt64(&g.count) - 1
}

// FormatKey formats an integer key to the standard YCSB "user<zero-padded-number>" string.
func FormatKey(keyNum int64) string {
	return fmt.Sprintf("user%016d", keyNum)
}

// RowSerializer serializes and deserializes YCSB row fields (map[string][]byte)
// into a compact binary format: [numFields:uint32] {[lenName:uint16][name][lenVal:uint32][val]}...
type RowSerializer struct{}

func (s *RowSerializer) Serialize(fields map[string][]byte) []byte {
	var buf bytes.Buffer
	numFields := uint32(len(fields))
	_ = binary.Write(&buf, binary.LittleEndian, numFields)

	for k, v := range fields {
		nameBytes := []byte(k)
		_ = binary.Write(&buf, binary.LittleEndian, uint16(len(nameBytes)))
		buf.Write(nameBytes)
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(v)))
		buf.Write(v)
	}
	return buf.Bytes()
}

func (s *RowSerializer) Deserialize(data []byte) (map[string][]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("invalid serialized data length: %d", len(data))
	}
	numFields := binary.LittleEndian.Uint32(data[:4])
	result := make(map[string][]byte, numFields)
	offset := 4

	for i := uint32(0); i < numFields; i++ {
		if offset+2 > len(data) {
			return nil, fmt.Errorf("truncated field name length at offset %d", offset)
		}
		nameLen := int(binary.LittleEndian.Uint16(data[offset : offset+2]))
		offset += 2

		if offset+nameLen > len(data) {
			return nil, fmt.Errorf("truncated field name at offset %d", offset)
		}
		name := string(data[offset : offset+nameLen])
		offset += nameLen

		if offset+4 > len(data) {
			return nil, fmt.Errorf("truncated field value length at offset %d", offset)
		}
		valLen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		offset += 4

		if offset+valLen > len(data) {
			return nil, fmt.Errorf("truncated field value at offset %d", offset)
		}
		val := make([]byte, valLen)
		copy(val, data[offset:offset+valLen])
		offset += valLen

		result[name] = val
	}

	return result, nil
}
