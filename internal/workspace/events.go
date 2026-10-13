package workspace

import (
	"github.com/fredricw/prt/internal/proto"
)

// Events implements daemon.Client: every member's events on one channel,
// rewritten to workspace ids (outEvent). Forwarding never blocks: an event
// is dropped when the channel's buffer is full, as InProcess does. The
// channel closes after Close, or once every member's stream has closed.
func (c *Client) Events() <-chan proto.Event { return c.events }

// startEvents starts one forwarder per member. With at least one member,
// the merged channel also closes when every member's stream has ended.
func (c *Client) startEvents() {
	for _, m := range c.members {
		c.fwd.Add(1)
		go c.forward(m.Info.Key, m.Client.Events())
	}
	if len(c.members) > 0 {
		go func() {
			c.fwd.Wait()
			c.closeEvents()
		}()
	}
}

// forward copies one member's events until its stream closes or Close.
func (c *Client) forward(key string, ch <-chan proto.Event) {
	defer c.fwd.Done()
	for {
		select {
		case <-c.done:
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			select {
			case c.events <- outEvent(key, e):
			default: // full: drop, as InProcess.send does
			}
		}
	}
}

// closeEvents closes the merged channel once; callers make sure no
// forwarder is still running.
func (c *Client) closeEvents() {
	c.eventsOnce.Do(func() { close(c.events) })
}
