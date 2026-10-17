package main

import (
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"sync"
	"time"
)

func (c *serviceClient) terminal(instance, name, id string) (*tuiTerminal, error) {
	u := c.endpoint("/api/terminal", instance)
	q := u.Query()
	q.Set("sandbox", name)
	q.Set("session", id)
	u.RawQuery = q.Encode()
	u.Scheme = "ws"
	if c.base.Scheme == "https" {
		u.Scheme = "wss"
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("Terminal connection failed: %w", err)
	}
	events := make(chan terminalEvent, 64)
	send := make(chan string, 64)
	done := make(chan struct{})
	var once sync.Once
	closeStream := func() { once.Do(func() { close(done); _ = conn.Close() }) }
	t := &tuiTerminal{events: events, name: name, id: id, send: func(data string) error {
		select {
		case <-done:
			return fmt.Errorf("Terminal connection closed")
		default:
		}
		select {
		case send <- data:
			return nil
		default:
			return fmt.Errorf("Terminal input queue full; retry")
		}
	}, closeStream: closeStream}
	go func() {
		defer closeStream()
		for {
			select {
			case data := <-send:
				_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
				if err := conn.WriteJSON(map[string]string{"type": "input", "data": data}); err != nil {
					return
				}
			case <-done:
				return
			case <-c.ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer close(events)
		defer closeStream()
		for {
			var event terminalEvent
			if err := conn.ReadJSON(&event); err != nil {
				select {
				case events <- terminalEvent{"disconnected", "Terminal connection lost; reattach to continue"}:
				case <-done:
				}
				return
			}
			select {
			case events <- event:
			case <-done:
				return
			}
			if event.Type == "ended" {
				return
			}
		}
	}()
	return t, nil
}
