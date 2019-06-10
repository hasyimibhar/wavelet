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

package api

import (
	"context"
	"github.com/fasthttp/websocket"
	"github.com/perlin-network/wavelet/debouncer"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fastjson"
	"strconv"
	"time"
)

const (
	writeWait          = 10 * time.Second
	pongWait           = 60 * time.Second
	pingPeriod         = (pongWait * 9) / 10
	maxMessageSize     = 512
	maxPaginationLimit = 5000
)

var upgrader = websocket.FastHTTPUpgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(ctx *fasthttp.RequestCtx) bool {
		return true
	},
}

type client struct {
	sink      *sink
	debouncer debouncer.IDebouncer
	conn      *websocket.Conn

	filters map[string]string
	sendC   chan []byte
}

func (c *client) readWorker() {
	defer func() {
		c.sink.leave <- c
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error { _ = c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *client) writeWorker() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.sendC:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if len(msg) == 0 {
				continue
			}

			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))

			err := c.conn.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))

			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *client) send(data []interface{}) {
	for _, msg := range data {
		t, ok := msg.([]byte)
		if !ok {
			continue
		}

		select {
		case c.sendC <- t:
		default:
			close(c.sendC)
			delete(c.sink.clients, c)
			return
		}
	}
}

func (s *sink) serve(ctx *fasthttp.RequestCtx) error {
	filters := make(map[string]string)
	values := ctx.QueryArgs()
	for queryKey, key := range s.filters {
		if queryValue := values.Peek(queryKey); len(queryValue) > 0 {
			filters[key] = string(queryValue)
		}
	}

	return upgrader.Upgrade(ctx, func(conn *websocket.Conn) {
		client := &client{
			filters: filters,
			sink:    s,
			conn:    conn,
			sendC:   make(chan []byte, 256),
		}

		ctx := context.TODO()
		if s.groupKey != "" {
			client.debouncer = debouncer.NewGroupDebouncer(ctx, client.send, 100*time.Millisecond)
		} else {
			client.debouncer = debouncer.NewBatchDebouncer(ctx, client.send, 100*time.Millisecond, 16384)
		}

		s.join <- client

		go client.readWorker()

		// Block here because we need to keep the FastHTTPHandler active because of the way it works
		// Refer to https://github.com/fasthttp/websocket/issues/6
		client.writeWorker()
	})
}

type broadcastItem struct {
	buf   []byte
	value *fastjson.Value
}

type sink struct {
	groupKey string
	clients  map[*client]struct{}
	filters  map[string]string

	broadcast   chan broadcastItem
	join, leave chan *client
}

func (s *sink) run() {
	for {
		select {
		case client := <-s.join:
			s.clients[client] = struct{}{}
		case client := <-s.leave:
			if _, ok := s.clients[client]; ok {
				delete(s.clients, client)
				close(client.sendC)
			}
		case msg := <-s.broadcast:
		L:
			for client := range s.clients {
				for key, condition := range client.filters {
					o := msg.value.Get(key)

					if o != nil && !valueEqual(o, condition) {
						continue L
					}
				}

				key := ""
				if s.groupKey != "" {
					k := msg.value.GetStringBytes(s.groupKey)
					if k != nil {
						key = string(k)
					}
				}

				client.debouncer.Add(msg.buf, len(msg.buf), key)
			}
		}
	}
}

func valueEqual(v *fastjson.Value, filter string) bool {
	switch v.Type() {
	case fastjson.TypeArray:
		fallthrough
	case fastjson.TypeNumber:
		fallthrough
	case fastjson.TypeObject:
		return string(v.MarshalTo(nil)) == filter
	case fastjson.TypeString:
		b, _ := v.StringBytes()
		return string(b) == filter
	case fastjson.TypeTrue, fastjson.TypeFalse:
		b, err := v.Bool()
		if err != nil {
			return false
		}
		return strconv.FormatBool(b) == filter
	default:
		return false
	}
}
