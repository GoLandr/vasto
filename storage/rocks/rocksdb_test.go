package rocks

import (
	"bytes"
	"fmt"
	"github.com/chrislusf/vasto/pb"
	"math/rand"
	"testing"
	"time"
)

func TestPutGet(t *testing.T) {
	db := setupTestDb()
	defer cleanup(db)

	key := make([]byte, 4)
	value := make([]byte, 4)
	rand.Read(key)
	rand.Read(value)

	if err := db.Put(key, value); err != nil {
		t.Errorf("insert should not return any error. err: %v", err)
	}

	returned, err := db.Get(key)
	if err != nil {
		t.Errorf("get should not return any error. err: %v", err)
	}

	if !bytes.Equal(value, returned) {
		t.Errorf("data value is different. is(%d) should be(%d)", returned, value)
	}
}

func TestMerge(t *testing.T) {
	db := setupTestDb()
	defer cleanup(db)

	key := make([]byte, 4)
	rand.Read(key)

	if err := db.Merge(key, []byte("123")); err != nil {
		t.Errorf("insert should not return any error. err: %v", err)
	}

	if err := db.Merge(key, []byte("456")); err != nil {
		t.Errorf("insert should not return any error. err: %v", err)
	}

	returned, err := db.Get(key)
	if err != nil {
		t.Errorf("get should not return any error. err: %v", err)
	}

	if !bytes.Equal([]byte("123456"), returned) {
		t.Errorf("data value is different. is(%x) should be(%x)", returned, []byte("123456"))
	}
}

func TestPut10Million(t *testing.T) {

	db := setupTestDb()
	defer cleanup(db)

	limit := 100000

	value := make([]byte, 4)
	rand.Read(value)

	var buf bytes.Buffer
	now := time.Now()
	for i := 0; i < limit; i++ {
		buf.Reset()
		buf.WriteString(fmt.Sprintf("k%d", i))
		db.Put(buf.Bytes(), value)
	}

	db.Delete([]byte(fmt.Sprintf("k%d", 23445)))

	fmt.Printf("%d messages inserted in: %v\n", limit, time.Now().Sub(now))
	fmt.Printf("db size: %v\n", db.LiveFilesSize())
	for i, meta := range db.GetLiveFilesMetaData() {
		fmt.Printf("%d. file %s size:%d level: %v, [%v,%v]\n", i, meta.Name, meta.Size, meta.Level, meta.SmallestKey, meta.LargestKey)
	}

	acc := 0
	it := db.db.NewIterator(db.ro)
	it.SeekToFirst()
	for ; it.Valid(); it.Next() {
		acc++
	}
	if err := it.Err(); err != nil {
		t.Errorf("iterating should not return any error. err: %v", err)
	}
	it.Close()

	fmt.Printf("total number of elements in rocksdb: %d\n", acc)
}

func TestRangeScan(t *testing.T) {

	db := setupTestDb()
	defer cleanup(db)

	limit := 100000

	for i := 0; i < limit; i++ {
		key := []byte(fmt.Sprintf("k%5d", i))
		value := []byte(fmt.Sprintf("v%5d", i))
		db.Put(key, value)
	}

	prefix := []byte(fmt.Sprintf("k%3d", 123))
	limit = 25
	var counter1 int
	db.PrefixScan(prefix, nil, limit, func(key, value []byte) bool {
		counter1++
		// fmt.Printf("key: %s value: %s\n", string(key), string(value))
		return true
	})
	if counter1 != limit {
		t.Errorf("scanning expecting %d rows, but actual %d rows", limit, counter1)
	}

	prefix = []byte(fmt.Sprintf("k%4d", 1234))
	var counter2 int
	db.PrefixScan(prefix, nil, limit, func(key, value []byte) bool {
		counter2++
		// fmt.Printf("key: %s value: %s\n", string(key), string(value))
		return true
	})
	if counter2 != 10 {
		t.Errorf("scanning expecting %d rows, but actual %d rows", 10, counter2)
	}

	prefix = []byte(fmt.Sprintf("k%3d", 123))
	lastKey := []byte(fmt.Sprintf("k%5d", 12345))
	var counter3 int
	limit = 100
	db.PrefixScan(prefix, lastKey, limit, func(key, value []byte) bool {
		counter3++
		// fmt.Printf("key: %s value: %s\n", string(key), string(value))
		return true
	})
	if counter3 != 54 {
		t.Errorf("scanning expecting %d rows, but actual %d rows", 54, counter3)
	}

	if count(db) != 100000 {
		t.Errorf("scanning expecting %d rows, but actual %d rows", 100000, count(db))
	}

}

func TestFullScan(t *testing.T) {

	db := setupTestDb()
	defer cleanup(db)

	limit := 100000
	batchSize := 100

	value := make([]byte, 4)
	rand.Read(value)

	var buf bytes.Buffer
	for i := 0; i < limit; i++ {
		buf.Reset()
		buf.WriteString(fmt.Sprintf("k%d", i))
		db.Put(buf.Bytes(), value)
	}

	db.Delete([]byte(fmt.Sprintf("k%d", 23445)))

	var counter1 int
	db.FullScan(uint64(batchSize), 0, func(rows []*pb.RawKeyValue) error {
		for range rows {
			counter1++
		}
		if len(rows) > batchSize {
			t.Errorf("full scan batch size %d, but actual %d", batchSize, len(rows))
		}
		return nil
	})
	if counter1 != limit-1 {
		t.Errorf("full scan batches %d, but actual %d", limit-1, counter1)
	}

}

func setupTestDb() *Rocks {
	db := NewDb("/tmp/rocks-test-go", &bytesMergeOperator{})
	return db
}

func cleanup(db *Rocks) {
	db.Close()
	db.Destroy()
}

func count(db *Rocks) (count int) {
	db.PrefixScan(nil, nil, 0, func(key, value []byte) bool {
		count++
		return true
	})
	return count
}

type bytesMergeOperator struct {
}

func (mo bytesMergeOperator) FullMerge(key, existingValue []byte, operands [][]byte) ([]byte, bool) {
	for _, operand := range operands {
		existingValue = append(existingValue, operand...)
	}
	return existingValue, true
}

func (mo bytesMergeOperator) PartialMerge(key, leftOperand, rightOperand []byte) ([]byte, bool) {
	out := append(leftOperand, rightOperand...)
	return out, true
}
func (mo bytesMergeOperator) Name() string { return "bytesMergeOperator" }
