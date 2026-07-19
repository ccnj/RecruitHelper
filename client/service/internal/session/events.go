package session

import (
	"encoding/json"

	"recruithelper/contract/gen/go/protocol"
)

// SensorEvent 是已通过 generated schema 校验并完成持久 msgId 去重的事件。
type SensorEvent struct {
	HandID string
	MsgID  string
	Body   protocol.EventBody
}

type EventSink interface {
	OnEvent(SensorEvent)
}

type EventSinkFunc func(SensorEvent)

func (f EventSinkFunc) OnEvent(event SensorEvent) { f(event) }

func (c *Conn) handleEvent(env *protocol.Envelope) {
	if err := protocol.ValidateKindBody(protocol.KindEvent, env.Body); err != nil {
		c.hub.st.Audit("event_invalid", c.handID, env.MsgID, err.Error())
		return
	}
	var body protocol.EventBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		c.hub.st.Audit("event_invalid", c.handID, env.MsgID, err.Error())
		return
	}
	already, err := c.hub.st.MarkProcessed(env.MsgID, string(protocol.KindEvent), c.handID)
	if err != nil {
		c.hub.st.Audit("event_dedup_failed", c.handID, env.MsgID, err.Error())
		return
	}
	if already {
		return
	}
	if sink := c.hub.eventSink; sink != nil {
		sink.OnEvent(SensorEvent{HandID: c.handID, MsgID: env.MsgID, Body: body})
	}
}
