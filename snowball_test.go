package wavelet

import (
	"github.com/perlin-network/noise/skademlia"
	"github.com/perlin-network/wavelet/sys"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestNewSnowball(t *testing.T) {
	t.Parallel()

	snowball := NewSnowball(WithBeta(10))

	keys, err := skademlia.NewKeys(1, 1)
	assert.NoError(t, err)

	start := AttachSenderToTransaction(keys, NewTransaction(keys, sys.TagTransfer, nil))

	endA := AttachSenderToTransaction(keys, NewTransaction(keys, sys.TagStake, nil))
	endB := AttachSenderToTransaction(keys, NewTransaction(keys, sys.TagContract, nil))

	a := NewRound(1, ZeroMerkleNodeID, 1337, start, endA)
	b := NewRound(1, ZeroMerkleNodeID, 1010, start, endB)

	// Check that Snowball terminates properly given unanimous sampling of Round A.

	assert.Nil(t, snowball.Preferred())

	for i := 0; i < 12; i++ {
		assert.False(t, snowball.Decided())
		snowball.Tick(&a)
		assert.Equal(t, *snowball.Preferred(), a)
	}

	assert.True(t, snowball.Decided())
	assert.Equal(t, *snowball.Preferred(), a)

	assert.Equal(t, snowball.count, 11)
	assert.Len(t, snowball.counts, 1)
	assert.Len(t, snowball.candidates, 1)

	// Try tick once more. Does absolutely nothing.

	cloned := *snowball
	snowball.Tick(&a)
	assert.Equal(t, cloned, *snowball)

	// Reset Snowball and assert everything is cleared properly.

	snowball.Reset()

	assert.False(t, snowball.Decided())
	assert.Nil(t, snowball.Preferred())

	assert.Equal(t, snowball.count, 0)
	assert.Len(t, snowball.counts, 0)
	assert.Len(t, snowball.candidates, 0)

	// Check that Snowball terminates properly given unanimous sampling of Round A, with preference
	// first initially to check for off-by-one errors.

	snowball.Prefer(&a)
	assert.Equal(t, *snowball.Preferred(), a)

	for i := 0; i < 12; i++ {
		assert.False(t, snowball.Decided())
		snowball.Tick(&a)
		assert.Equal(t, *snowball.Preferred(), a)
	}

	assert.True(t, snowball.Decided())
	assert.Equal(t, *snowball.Preferred(), a)

	assert.Equal(t, snowball.count, 11)
	assert.Len(t, snowball.counts, 1)
	assert.Len(t, snowball.candidates, 1)

	// Reset Snowball and assert everything is cleared properly.

	snowball.Reset()

	assert.False(t, snowball.Decided())
	assert.Nil(t, snowball.Preferred())

	assert.Equal(t, snowball.count, 0)
	assert.Len(t, snowball.counts, 0)
	assert.Len(t, snowball.candidates, 0)

	// Check that Snowball terminates if we sample 11 times Round A, then sample 12 times Round B.
	// This demonstrates the that we need a large amount of samplings to overthrow our preferred
	// round, originally being A, such that it is B.

	for i := 0; i < 11; i++ {
		assert.False(t, snowball.Decided())
		snowball.Tick(&a)
		assert.Equal(t, *snowball.Preferred(), a)
	}

	assert.False(t, snowball.Decided())

	for i := 0; i < 12; i++ {
		assert.False(t, snowball.Decided())
		snowball.Tick(&b)

		if i == 11 {
			assert.Equal(t, *snowball.Preferred(), b)
		} else {
			assert.Equal(t, *snowball.Preferred(), a)
		}
	}

	assert.Equal(t, snowball.counts[a.ID], 11)
	assert.Equal(t, snowball.counts[b.ID], 12)

	assert.True(t, snowball.Decided())
	assert.Equal(t, *snowball.Preferred(), b)
	assert.Equal(t, snowball.count, 11)
	assert.Len(t, snowball.counts, 2)
	assert.Len(t, snowball.candidates, 2)

	// Try cause a panic by ticking with nil, or with an empty round.

	empty := &Round{}

	snowball.Tick(nil)
	snowball.Tick(empty)

	assert.Equal(t, snowball.counts[a.ID], 11)
	assert.Equal(t, snowball.counts[b.ID], 12)

	assert.True(t, snowball.Decided())
	assert.Equal(t, b, *snowball.Preferred())
	assert.Equal(t, 11, snowball.count)
	assert.Len(t, snowball.counts, 2)
	assert.Len(t, snowball.candidates, 2)

	// Try tick with nil if Snowball has not decided yet.

	snowball.Reset()

	snowball.Tick(&a)
	snowball.Tick(&a)

	assert.Equal(t, a.ID, snowball.lastID)
	assert.Equal(t, 1, snowball.Progress())
	assert.Len(t, snowball.counts, 1)

	snowball.Tick(nil)

	assert.Equal(t, ZeroRoundID, snowball.lastID)
	assert.Equal(t, 0, snowball.Progress())
	assert.Len(t, snowball.counts, 1)
}
