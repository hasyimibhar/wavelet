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
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestLRU_Read(t *testing.T) {
	{
		lru := newLRU(2)

		lru.put([MerkleHashSize]byte{'a'}, 1)
		lru.put([MerkleHashSize]byte{'b'}, 2)

		// Make 'b' least recently used
		assert.Equal(t, 2, lruValue(t, lru, [MerkleHashSize]byte{'b'}))
		assert.Equal(t, 1, lruValue(t, lru, [MerkleHashSize]byte{'a'}))

		lru.put([MerkleHashSize]byte{'c'}, 3)

		assert.Equal(t, 1, lruValue(t, lru, [MerkleHashSize]byte{'a'}))
		assert.False(t, lruCheck(lru, [MerkleHashSize]byte{'b'})) // 'b' should be removed
		assert.Equal(t, 3, lruValue(t, lru, [MerkleHashSize]byte{'c'}))
	}

	{
		lru := newLRU(2)

		lru.put([MerkleHashSize]byte{'a'}, 1)
		lru.put([MerkleHashSize]byte{'b'}, 2)

		// Make 'a' least recently used
		assert.Equal(t, 1, lruValue(t, lru, [MerkleHashSize]byte{'a'}))
		assert.Equal(t, 2, lruValue(t, lru, [MerkleHashSize]byte{'b'}))

		lru.put([MerkleHashSize]byte{'c'}, 3)

		assert.False(t, lruCheck(lru, [MerkleHashSize]byte{'a'})) // 'a' should be removed
		assert.Equal(t, 2, lruValue(t, lru, [MerkleHashSize]byte{'b'}))
		assert.Equal(t, 3, lruValue(t, lru, [MerkleHashSize]byte{'c'}))
	}
}

func TestLRU_Write(t *testing.T) {
	{
		lru := newLRU(2)

		lru.put([MerkleHashSize]byte{'a'}, 1)
		lru.put([MerkleHashSize]byte{'b'}, 2)

		assert.Equal(t, 1, lruValue(t, lru, [MerkleHashSize]byte{'a'}))
		assert.Equal(t, 2, lruValue(t, lru, [MerkleHashSize]byte{'b'}))

		// Make 'b' least recently used
		lru.put([MerkleHashSize]byte{'a'}, 5)

		lru.put([MerkleHashSize]byte{'c'}, 3)

		assert.Equal(t, 5, lruValue(t, lru, [MerkleHashSize]byte{'a'}))
		assert.False(t, lruCheck(lru, [MerkleHashSize]byte{'b'})) // 'b' should be removed
		assert.Equal(t, 3, lruValue(t, lru, [MerkleHashSize]byte{'c'}))
	}

	{
		lru := newLRU(2)

		lru.put([MerkleHashSize]byte{'a'}, 1)
		lru.put([MerkleHashSize]byte{'b'}, 2)

		assert.Equal(t, 1, lruValue(t, lru, [MerkleHashSize]byte{'a'}))
		assert.Equal(t, 2, lruValue(t, lru, [MerkleHashSize]byte{'b'}))

		// Make 'a' least recently used
		lru.put([MerkleHashSize]byte{'b'}, 8)

		lru.put([MerkleHashSize]byte{'c'}, 3)

		assert.False(t, lruCheck(lru, [MerkleHashSize]byte{'a'})) // 'a' should be removed
		assert.Equal(t, 8, lruValue(t, lru, [MerkleHashSize]byte{'b'}))
		assert.Equal(t, 3, lruValue(t, lru, [MerkleHashSize]byte{'c'}))
	}
}

func TestLRU_Remove(t *testing.T) {
	lru := newLRU(2)

	lru.put([MerkleHashSize]byte{'a'}, 1)
	lru.put([MerkleHashSize]byte{'b'}, 2)

	assert.Equal(t, 1, lruValue(t, lru, [MerkleHashSize]byte{'a'}))
	assert.Equal(t, 2, lruValue(t, lru, [MerkleHashSize]byte{'b'}))

	lru.remove([MerkleHashSize]byte{'a'})

	assert.False(t, lruCheck(lru, [MerkleHashSize]byte{'a'})) // 'a' should be removed
	assert.Equal(t, 2, lruValue(t, lru, [MerkleHashSize]byte{'b'}))
}

func lruCheck(lru *lru, key [MerkleHashSize]byte) bool {
	_, ok := lru.load(key)
	return ok
}

func lruValue(t *testing.T, lru *lru, key [MerkleHashSize]byte) interface{} {
	val, ok := lru.load(key)
	assert.True(t, ok)
	return val
}
