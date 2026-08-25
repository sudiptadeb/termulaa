package main

import (
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsConn adapts a WebSocket to net.Conn so smux can ride on it. Kept
// byte-identical with the memd side of the tunnel contract.
type wsConn struct {
	ws       *websocket.Conn
	rbuf     []byte
	wmu      sync.Mutex
	closer   sync.Once
	closeErr error
}

func newWSConn(ws *websocket.Conn) *wsConn { return &wsConn{ws: ws} }

func (c *wsConn) Read(p []byte) (int, error) {
	for len(c.rbuf) == 0 {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		c.rbuf = data
	}
	n := copy(p, c.rbuf)
	c.rbuf = c.rbuf[n:]
	return n, nil
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error {
	c.closer.Do(func() { c.closeErr = c.ws.Close() })
	return c.closeErr
}

func (c *wsConn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }

// No WS read deadline by design: smux's keepalive timeout is the liveness
// detector (see the tunnel contract).
func (c *wsConn) SetDeadline(t time.Time) error     { return nil }
func (c *wsConn) SetReadDeadline(t time.Time) error { return nil }

func (c *wsConn) SetWriteDeadline(t time.Time) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.SetWriteDeadline(t)
}
