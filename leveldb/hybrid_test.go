package leveldb

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/hbtree"
	"github.com/syndtr/goleveldb/leveldb/learned"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func TestHybrid_PredictExistence(t *testing.T) {
	key := []byte("test-key-12345")
	prob := learned.PredictExistence(key)
	t.Logf("PredictExistence prob: %v", prob)

	// Since we hash inputs, probability should be in [0, 1] range.
	if prob < 0.0 || prob > 1.0 {
		t.Errorf("expected probability in [0, 1], got %v", prob)
	}

	emptyProb := learned.PredictExistence(nil)
	if emptyProb != 0.0 {
		t.Errorf("expected nil key probability to be 0.0, got %v", emptyProb)
	}
}

func TestHybrid_BTree(t *testing.T) {
	btree := hbtree.NewBTree(2, nil) // small K to force splits

	// Insert items.
	items := []struct {
		key []byte
		p   hbtree.BTreePayload
	}{
		{[]byte("key-b"), hbtree.BTreePayload{FileID: 1, Offset: 10, Length: 5, Seq: 10, Type: uint8(keyTypeVal)}},
		{[]byte("key-d"), hbtree.BTreePayload{FileID: 1, Offset: 20, Length: 5, Seq: 20, Type: uint8(keyTypeVal)}},
		{[]byte("key-a"), hbtree.BTreePayload{FileID: 1, Offset: 30, Length: 5, Seq: 30, Type: uint8(keyTypeVal)}},
		{[]byte("key-c"), hbtree.BTreePayload{FileID: 1, Offset: 40, Length: 5, Seq: 40, Type: uint8(keyTypeVal)}},
	}

	for _, it := range items {
		btree.Insert(it.key, it.p)
	}

	// Search items.
	for _, it := range items {
		res, found := btree.Search(it.key)
		if !found {
			t.Errorf("expected to find key %s", string(it.key))
		}
		if res.Offset != it.p.Offset {
			t.Errorf("expected offset %d, got %d", it.p.Offset, res.Offset)
		}
	}

	// Search non-existent.
	if _, found := btree.Search([]byte("key-e")); found {
		t.Errorf("did not expect to find non-existent key")
	}

	// Range search.
	results := btree.RangeSearch([]byte("key-b"), []byte("key-c"))
	if len(results) != 2 {
		t.Errorf("expected range query to return 2 results, got %d", len(results))
	}
	if !bytes.Equal(results[0].Key, []byte("key-b")) || !bytes.Equal(results[1].Key, []byte("key-c")) {
		t.Errorf("range search keys out of order or incorrect")
	}
}

func TestHybrid_EndToEnd(t *testing.T) {
	tmpDir1, err := ioutil.TempDir("", "hybrid-test-db-1")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir1)

	tmpDir2, err := ioutil.TempDir("", "hybrid-test-db-2")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir2)

	// Force NoCompression as required by the direct physical offset reads.
	opts := &opt.Options{
		Compression: opt.NoCompression,
		Filter:      filter.NewBloomFilter(10),
	}

	// 1. Open Database instances (relational per-table instances).
	dbA, err := OpenFile(tmpDir1, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()

	dbB, err := OpenFile(tmpDir2, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()

	// 2. Insert records to Table A (outer table: small table).
	// 2. Insert records to Table A (outer table: small table).
	// Table A: user_id -> country
	err = dbA.Put([]byte("user-1"), []byte("USA"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = dbA.Put([]byte("user-2"), []byte("France"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = dbA.Put([]byte("user-3"), []byte("Japan"), nil)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Insert records to Table B (inner table: large table).
	// Table B: country -> tax_rate
	err = dbB.Put([]byte("USA"), []byte("0.30"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = dbB.Put([]byte("Japan"), []byte("0.25"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = dbB.Put([]byte("Germany"), []byte("0.40"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = dbB.Put([]byte("Canada"), []byte("0.15"), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Trigger compaction to write them to SSTables, generating the B-tree indices and Overflow Bloom filters.
	err = dbA.CompactRange(util.Range{})
	if err != nil {
		t.Fatal(err)
	}
	err = dbB.CompactRange(util.Range{})
	if err != nil {
		t.Fatal(err)
	}

	// Verify point reads go through the new B-tree + direct disk access.
	val, err := dbA.Get([]byte("user-1"), nil)
	if err != nil {
		t.Fatalf("expected to get user-1 value, got error: %v", err)
	}
	if !bytes.Equal(val, []byte("USA")) {
		t.Fatalf("expected value USA, got %s", string(val))
	}

	// Test point query for non-existent key.
	_, err = dbB.Get([]byte("France"), nil)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}

	// 4. Test Range Queries.
	keys, values, err := dbA.RangeQuery([]byte("user-1"), []byte("user-2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if !bytes.Equal(keys[0], []byte("user-1")) || !bytes.Equal(keys[1], []byte("user-2")) {
		t.Fatalf("incorrect keys returned in range query: %s, %s", keys[0], keys[1])
	}
	if !bytes.Equal(values[0], []byte("USA")) || !bytes.Equal(values[1], []byte("France")) {
		t.Fatalf("incorrect values returned in range query: %s, %s", values[0], values[1])
	}

	// 5. Test Indexed Nested-Loop Joins (INLJ).
	ast, err := ParseJoin("db.join('table_a', 'table_b')")
	if err != nil {
		t.Fatal(err)
	}

	dbs := map[string]*DB{
		"table_a": dbA,
		"table_b": dbB,
	}

	joined, err := ast.Eval(dbs)
	if err != nil {
		t.Fatal(err)
	}

	// Joins should produce:
	// - user-1 (USA) -> USA (0.30)
	// - user-3 (Japan) -> Japan (0.25)
	// Note: user-2 (France) should be instantly dropped by the AI Shield of table_b since France does not exist in table_b!
	if len(joined) != 2 {
		t.Fatalf("expected 2 joined tuples, got %d", len(joined))
	}

	fmt.Println("Join Results:")
	for _, tuple := range joined {
		fmt.Printf("Outer: (%s => %s) Joined with Inner: (%s => %s)\n",
			string(tuple.OuterKey), string(tuple.OuterValue),
			string(tuple.InnerKey), string(tuple.InnerValue))
	}

	// Check tuple values.
	foundUSA := false
	foundJapan := false
	for _, tpl := range joined {
		if bytes.Equal(tpl.OuterKey, []byte("user-1")) && bytes.Equal(tpl.InnerValue, []byte("0.30")) {
			foundUSA = true
		}
		if bytes.Equal(tpl.OuterKey, []byte("user-3")) && bytes.Equal(tpl.InnerValue, []byte("0.25")) {
			foundJapan = true
		}
	}

	if !foundUSA || !foundJapan {
		t.Fatalf("missing expected USA or Japan join records")
	}
}
