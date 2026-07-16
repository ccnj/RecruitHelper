package session

import (
	"encoding/json"
	"sync"

	"recruithelper/contract/gen/go/protocol"
)

// FrameEvent:一条协议帧的观测摘要(测试页协议观测台用;不含 body 大字段)。
type FrameEvent struct {
	Seq    int64  `json:"seq"`
	Dir    string `json:"dir"` // in(手→脑) / out(脑→手)
	Kind   string `json:"kind"`
	HandID string `json:"handId"`
	MsgID  string `json:"msgId"`
	Ref    string `json:"ref,omitempty"` // ack/result/cancel 等指向的命令
	Ts     int64  `json:"ts"`
}

// FrameBus:帧观测广播。订阅者各持一个有缓冲 channel(满则丢,观测容忍丢);
// 另留一个环形缓冲供迟到订阅者拿最近历史。纯观测,不参与协议正确性。
type FrameBus struct {
	mu     sync.Mutex
	subs   map[int]chan FrameEvent
	nextID int
	seq    int64
	ring   []FrameEvent
	ringN  int
}

func NewFrameBus() *FrameBus {
	return &FrameBus{subs: map[int]chan FrameEvent{}, ringN: 200}
}

// publish:发布一条帧事件。给 seq、写环、非阻塞投递给订阅者。
func (b *FrameBus) publish(e FrameEvent) {
	b.mu.Lock()
	b.seq++
	e.Seq = b.seq
	b.ring = append(b.ring, e)
	if len(b.ring) > b.ringN {
		b.ring = b.ring[len(b.ring)-b.ringN:]
	}
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // 订阅者慢,丢弃(观测容忍丢)
		}
	}
	b.mu.Unlock()
}

// Subscribe:注册订阅者,返回 id、事件 channel、最近历史快照。
func (b *FrameBus) Subscribe() (int, <-chan FrameEvent, []FrameEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan FrameEvent, 64)
	b.subs[id] = ch
	backlog := append([]FrameEvent(nil), b.ring...)
	return id, ch, backlog
}

func (b *FrameBus) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		close(ch)
		delete(b.subs, id)
	}
}

// observe:把一条已解析信封发布为帧事件(dir=in/out)。ack/result 类抽取 ref 便于关联。
func (b *FrameBus) observe(dir, handID string, env *protocol.Envelope) {
	ev := FrameEvent{Dir: dir, Kind: string(env.Kind), HandID: handID, MsgID: env.MsgID, Ts: env.Ts}
	switch env.Kind {
	case protocol.KindAck, protocol.KindResult, protocol.KindCancel, protocol.KindProgress, protocol.KindQuery, protocol.KindReport:
		var r struct {
			Ref string `json:"ref"`
		}
		if json.Unmarshal(env.Body, &r) == nil {
			ev.Ref = r.Ref
		}
	}
	b.publish(ev)
}
