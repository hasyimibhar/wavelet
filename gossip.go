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

package wavelet

import (
	"context"
	"github.com/perlin-network/noise/skademlia"
	"github.com/perlin-network/wavelet/debouncer"
	"github.com/perlin-network/wavelet/log"
	"sync"
	"time"
)

type Gossiper struct {
	client  *skademlia.Client
	metrics *Metrics

	streams     map[string]Wavelet_GossipClient
	streamsLock sync.Mutex

	debouncer *debouncer.BatchDebouncer
}

func NewGossiper(ctx context.Context, client *skademlia.Client, metrics *Metrics) *Gossiper {
	g := &Gossiper{
		client:  client,
		metrics: metrics,

		streams: make(map[string]Wavelet_GossipClient),
	}

	g.debouncer = debouncer.NewBatchDebouncer(ctx, g.Gossip, 50*time.Millisecond, 16384)

	return g
}

func (g *Gossiper) Push(tx Transaction) {
	data := tx.Marshal()
	g.debouncer.Add(data, len(data), "")

	if g.metrics != nil {
		g.metrics.gossipedTX.Mark(int64(tx.LogicalUnits()))
	}
}

func (g *Gossiper) Gossip(transactions []interface{}) {
	var err error

	ttx := make([][]byte, len(transactions))
	for i, tx := range transactions {
		t, ok := tx.([]byte)
		if !ok {
			continue
		}
		ttx[i] = t
	}

	batch := &Transactions{Transactions: ttx}

	conns := g.client.ClosestPeers()

	var wg sync.WaitGroup
	wg.Add(len(conns))

	for _, conn := range conns {
		target := conn.Target()

		g.streamsLock.Lock()
		stream, exists := g.streams[conn.Target()]

		if !exists {
			client := NewWaveletClient(conn)

			if stream, err = client.Gossip(context.Background()); err != nil {
				g.streamsLock.Unlock()
				continue
			}

			g.streams[target] = stream
		}
		g.streamsLock.Unlock()

		go func() {
			if err := stream.Send(batch); err != nil {
				logger := log.TX("gossip")
				logger.Err(err).Msg("Failed to send batch")

				g.streamsLock.Lock()
				delete(g.streams, target)
				g.streamsLock.Unlock()
			}

			wg.Done()
		}()
	}

	wg.Wait()
}
