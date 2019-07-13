// Copyright (c) 2019 Perlin
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of
// this software and associated documentation files (the "Software"), to deal in
// the Software without restriction, including without limitation the rights to
// use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
// the Software, and to permit persons to whom the Software is furnished to do so,
// subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
// FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
// COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
// IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
// CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package avl

import (
	"bytes"
	"encoding/binary"
	"github.com/perlin-network/wavelet/store"
	"github.com/stretchr/testify/assert"
	"math/rand"
	"os"
	"testing"
	"testing/quick"
)

func TestSerialize(t *testing.T) {
	kv, cleanup := GetKV("level", "db")
	defer cleanup()

	tree := New(kv)

	fn := func(key, value []byte) bool {
		node := newLeafNode(tree, key, value)

		var buf bytes.Buffer
		node.serialize(&buf)

		assert.ObjectsAreEqual(node, mustDeserialize(bytes.NewReader(buf.Bytes())))

		return true
	}

	assert.NoError(t, quick.Check(fn, &quick.Config{MaxCount: 10000}))
}

func TestTree_Commit(t *testing.T) {
	kv, cleanup := GetKV("level", "db")
	defer cleanup()

	{
		tree := New(kv)
		tree.Insert([]byte("key"), []byte("value"))
		assert.NoError(t, tree.Commit())
	}

	{
		tree := New(kv)
		val, ok := tree.Lookup([]byte("key"))
		assert.True(t, ok)
		assert.EqualValues(t, val, []byte("value"))
	}
}

func TestTree_Delete(t *testing.T) {
	kv, cleanup := GetKV("level", "db")
	defer cleanup()

	{
		tree := New(kv)
		tree.Insert([]byte("key"), []byte("value"))
		tree.Insert([]byte("foo"), []byte("bar"))
		assert.NoError(t, tree.Commit())
	}

	{
		tree := New(kv)
		assert.True(t, tree.Delete([]byte("foo")))
		assert.NoError(t, tree.Commit())
	}

	{
		tree := New(kv)

		val, ok := tree.Lookup([]byte("key"))
		assert.True(t, ok)
		assert.EqualValues(t, val, []byte("value"))

		_, ok = tree.Lookup([]byte("foo"))
		assert.False(t, ok)
	}
}

func TestTree_DeleteUntilEmpty(t *testing.T) {
	kv, cleanup := GetKV("level", "db")
	defer cleanup()

	values := map[string]string{
		"key":   "value",
		"foo":   "bar",
		"lorem": "ipsum",
	}

	{
		tree := New(kv)
		for k, v := range values {
			tree.Insert([]byte(k), []byte(v))
		}

		assert.NoError(t, tree.Commit())
	}

	{
		tree := New(kv)

		// Delete all the keys
		for k := range values {
			assert.True(t, tree.Delete([]byte(k)))
		}

		// None of the keys should exist
		for k := range values {
			_, ok := tree.Lookup([]byte(k))
			assert.False(t, ok)
		}

		assert.NoError(t, tree.Commit())
	}

	{
		tree := New(kv)

		// Previous change should be commited
		for k := range values {
			_, ok := tree.Lookup([]byte(k))
			assert.False(t, ok)
		}
	}
}

func TestTree_Snapshot(t *testing.T) {
	kv, cleanup := GetKV("level", "db")
	defer cleanup()

	tree := New(kv)
	tree.Insert([]byte("k1"), []byte("1"))
	assert.NoError(t, tree.Commit())

	ss := tree.Snapshot()

	tree.Insert([]byte("k1"), []byte("2"))
	tree.Insert([]byte("k2"), []byte("2"))

	tree.Revert(ss)

	v, ok := tree.Lookup([]byte("k1"))
	assert.True(t, ok)
	assert.EqualValues(t, v, []byte("1"))

	_, ok = tree.Lookup([]byte("k2"))
	assert.False(t, ok)
}

func TestTree_Diff_Randomized(t *testing.T) {
	kv, cleanup := GetKV("level", "db")
	defer cleanup()

	kv2, cleanup2 := GetKV("level", "db2")
	defer cleanup2()

	tree1 := New(kv)
	tree2 := New(kv2)

	i := 1

	fn := func(key, value []byte) bool {
		if len(key) == 0 || len(value) == 0 {
			return true
		}

		defer func() {
			i++
		}()

		var a, b *Tree

		if rand.Int()%2 == 0 {
			a, b = tree1, tree2
		} else {
			a, b = tree2, tree1
		}

		if a.viewID != uint64(i-1) {
			if b.viewID != uint64(i-1) {
				return false
			}

			assert.NoError(t, a.ApplyDiff(b.DumpDiff(a.viewID)))
		}

		a.SetViewID(uint64(i))
		a.Insert(key, value)
		assert.NoError(t, a.Commit())

		return true
	}

	assert.NoError(t, quick.Check(fn, &quick.Config{MaxCount: 10000}))

	assert.NoError(t, tree1.ApplyDiff(tree2.DumpDiff(tree1.viewID)))
	assert.NoError(t, tree1.Commit())
	assert.NoError(t, tree2.ApplyDiff(tree1.DumpDiff(tree2.viewID)))
	assert.NoError(t, tree2.Commit())
	assert.Equal(t, tree1.root.id, tree2.root.id)
}

func TestTree_Diff_UpdateNotifier(t *testing.T) {
	kv, cleanup1 := GetKV("level", "db")
	defer cleanup1()

	kv2, cleanup2 := GetKV("level", "db2")
	defer cleanup2()

	tree1 := New(kv)
	tree2 := New(kv2)

	tree1.Insert([]byte("a"), []byte("b"))
	tree1.Insert([]byte("c"), []byte("d"))
	tree1.Insert([]byte("m"), []byte("n"))
	tree1.Insert([]byte("p"), []byte("q"))
	tree1.Insert([]byte("r"), []byte("s"))

	tree2.Insert([]byte("a"), []byte("b"))
	tree2.Insert([]byte("c"), []byte("d"))
	tree2.Insert([]byte("m"), []byte("n"))
	tree2.Insert([]byte("p"), []byte("q"))
	tree2.Insert([]byte("r"), []byte("s"))

	tree1.SetViewID(tree1.viewID + 1)
	tree1.Insert([]byte("e"), []byte("f"))

	tree2.Commit()

	diffMap := make(map[string]string)
	iterCount := 0
	assert.NoError(t, tree2.ApplyDiffWithUpdateNotifier(tree1.DumpDiff(tree2.viewID), func(k, v []byte) {
		diffMap[string(k)] = string(v)
		iterCount++
	}))

	assert.Equal(t, iterCount, 1)
	assert.Equal(t, len(diffMap), 1)
	assert.Equal(t, diffMap["e"], "f")
}

func TestTree_ApplyEmptyDiff(t *testing.T) {
	kv, cleanup1 := GetKV("level", "db")
	defer cleanup1()

	kv2, cleanup2 := GetKV("level", "db2")
	defer cleanup2()

	tree1 := New(kv)

	for i := uint64(0); i < 50; i++ {
		tree1.Insert([]byte("a"), []byte("b"))
		tree1.viewID++
	}

	tree1.Insert([]byte("b"), []byte("c"))
	tree1.viewID++

	for i := uint64(0); i < 50; i++ {
		tree1.Insert([]byte("a"), []byte("b"))
		tree1.viewID++
	}

	tree2 := New(kv2)
	tree2.Insert([]byte("a"), []byte("b"))
	tree2.viewID++

	tree1.Commit()
	tree2.Commit()

	assert.NoError(t, tree2.ApplyDiff(tree1.DumpDiff(tree2.viewID)))

	assert.Equal(t, tree1.root.id, tree2.root.id)
}

func TestTree_Difference(t *testing.T) {
	kv, cleanup := GetKV("level", "db")
	defer cleanup()

	kv2, cleanup2 := GetKV("level", "db2")
	defer cleanup2()

	tree := New(kv)
	tree.SetViewID(1)
	tree.Insert([]byte("k1"), []byte("1"))

	tree2 := New(kv2)
	tree2.SetViewID(0)

	tree2.Commit()

	assert.NoError(t, tree2.ApplyDiff(tree.DumpDiff(0)))
	assert.Equal(t, tree2.viewID, uint64(1))
	tree2.SetViewID(2)
	tree2.Insert([]byte("k2"), []byte("2"))

	result, _ := tree2.Lookup([]byte("k1"))
	assert.Equal(t, []byte("1"), result)

	result, _ = tree2.Lookup([]byte("k2"))
	assert.Equal(t, []byte("2"), result)

	tree.Commit()
	assert.NoError(t, tree.ApplyDiff(tree2.DumpDiff(1)))
	assert.Equal(t, tree.viewID, uint64(2))

	result, _ = tree.Lookup([]byte("k2"))
	assert.Equal(t, []byte("2"), result)

	len1 := len(tree.DumpDiff(0))
	len2 := len(tree.DumpDiff(1))
	len3 := len(tree.DumpDiff(2))

	assert.Equal(t, len3, 0)
	assert.True(t, len1 > len2)
	assert.True(t, len2 > len3)
}

func TestTree_Iterate(t *testing.T) {
	tree := New(store.NewInmem())
	for i := uint64(0); i < 50; i++ {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], i)
		tree.Insert(buf[:], buf[:])
	}
	assert.NoError(t, tree.Commit())

	var result []uint64

	tree.Iterate(func(key, value []byte) {
		k := binary.BigEndian.Uint64(key)
		v := binary.BigEndian.Uint64(value)
		assert.Equal(t, k, v)
		result = append(result, k)
	})

	var expected []uint64
	for i := uint64(0); i < 50; i++ {
		expected = append(expected, i)
	}

	assert.Equal(t, result, expected)
}

func TestTree_IterateFrom(t *testing.T) {
	tree := New(store.NewInmem())
	for i := uint64(0); i < 50; i++ {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], i)
		tree.Insert(buf[:], buf[:])
	}
	assert.NoError(t, tree.Commit())

	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], 20)
	var result []uint64

	// Early stop
	tree.IterateFrom(buf[:], func(key, value []byte) bool {
		k := binary.BigEndian.Uint64(key)
		v := binary.BigEndian.Uint64(value)
		assert.Equal(t, k, v)
		result = append(result, k)
		if k == 42 {
			return false
		} else {
			return true
		}
	})
	var expected []uint64
	for i := uint64(20); i <= 42; i++ {
		expected = append(expected, i)
	}
	assert.Equal(t, result, expected)

	// Full iteration
	result = nil
	tree.IterateFrom(buf[:], func(key, value []byte) bool {
		k := binary.BigEndian.Uint64(key)
		v := binary.BigEndian.Uint64(value)
		assert.Equal(t, k, v)
		result = append(result, k)
		return true
	})
	expected = nil
	for i := uint64(20); i <= 49; i++ {
		expected = append(expected, i)
	}
	assert.Equal(t, result, expected)
}

func TestTree_IteratePrefix(t *testing.T) {
	tree := New(store.NewInmem())

	values := []string{
		"1", "foo:2", "foo:3", "4",
		"foo:5", "6", "foo:7", "8", "9",
		"10", "foo:11",
	}

	for _, val := range values {
		tree.Insert([]byte(val), []byte(val))
	}
	assert.NoError(t, tree.Commit())

	var result []string
	tree.IteratePrefix([]byte("foo:"), func(key, value []byte) {
		result = append(result, string(key))
	})

	assert.Equal(t, []string{"foo:11", "foo:2", "foo:3", "foo:5", "foo:7"}, result)
}

func BenchmarkAVL(b *testing.B) {
	const InnerLoopCount = 10000
	const KeySize = 16

	shouldDelete := make([]byte, b.N*InnerLoopCount*2)

	_, err := rand.Read(shouldDelete)
	assert.NoError(b, err)

	b.ResetTimer()

	for i := 0; i < b.N/InnerLoopCount; i++ {
		//kv := store.NewInmem()
		kv, cleanup := GetKV("level", "db")
		tree := New(kv)

		refMap := make(map[string][]byte)
		refDeletes := make(map[string]struct{})

		keys := make([]byte, InnerLoopCount*KeySize)

		_, err := rand.Read(keys)
		assert.NoError(b, err)

		for j := 0; j < InnerLoopCount; j++ {
			shouldDelete := shouldDelete[i*InnerLoopCount+j:]

			if (shouldDelete[0]&1 == 1) && (shouldDelete[1]&1 == 1) {
				if len(refMap) == 0 {
					continue
				}

				var delKey string

				for k := range refMap {
					delKey = k
					break
				}

				lookupResult, ok := tree.Lookup([]byte(delKey))
				assert.True(b, ok)
				assert.EqualValues(b, refMap[delKey], lookupResult)

				delete(refMap, delKey)
				refDeletes[delKey] = struct{}{}
				tree.Delete([]byte(delKey))

				_, ok = tree.Lookup([]byte(delKey))
				assert.False(b, ok)
			} else {
				key := keys[j*KeySize : (j+1)*KeySize]
				value := make([]byte, KeySize)

				for k := 0; k < len(key); k++ {
					value[k] = ^key[k]
				}

				delete(refDeletes, string(key))

				refMap[string(key)] = value
				tree.Insert(key, value)

				lookupResult, ok := tree.Lookup(key)
				assert.True(b, ok)
				assert.EqualValues(b, refMap[string(key)], lookupResult)
			}
		}

		assert.NoError(b, tree.Commit())

		cleanup()
	}
}

func GetKV(kv string, path string) (store.KV, func()) {
	if kv == "inmem" {
		inmemdb := store.NewInmem()
		return inmemdb, func() {
			_ = inmemdb.Close()
		}
	}
	if kv == "level" {
		// Remove existing db
		_ = os.RemoveAll(path)

		leveldb, err := store.NewLevelDB(path)
		if err != nil {
			panic("failed to create LevelDB: " + err.Error())
		}

		return leveldb, func() {
			_ = leveldb.Close()
			_ = os.RemoveAll(path)
		}
	}

	panic("unknown kv " + kv)
}
