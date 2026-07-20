package appbridge

import (
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

func threadMessage(direction protocol.MessageDirection, hash string) protocol.ThreadMessage {
	text := hash
	return protocol.ThreadMessage{
		Direction: direction, Kind: protocol.MessageKindText, ContentHash: hash, Text: &text,
	}
}

func TestClassifyVerifiedSendRequiresUniqueAnchorAndUniqueTargetAfterTail(t *testing.T) {
	anchors := []protocol.MessageAnchor{
		{Direction: protocol.MessageDirectionIn, ContentHash: "a"},
		{Direction: protocol.MessageDirectionOut, ContentHash: "target"},
	}
	base := []protocol.ThreadMessage{
		threadMessage(protocol.MessageDirectionIn, "a"),
		threadMessage(protocol.MessageDirectionOut, "target"), // tail 内相同指纹不算新发
		threadMessage(protocol.MessageDirectionIn, "other"),
		threadMessage(protocol.MessageDirectionOut, "target"),
	}
	starts := matchingAnchorStarts(base, anchors)
	observation, err := classifyVerifiedSend(base, starts, len(anchors), "target")
	if err != nil || !observation.Confirmed || observation.ContentHash != "target" {
		t.Fatalf("唯一 tail 后命中应确认: observation=%+v err=%v", observation, err)
	}

	duplicate := append(append([]protocol.ThreadMessage(nil), base...),
		threadMessage(protocol.MessageDirectionOut, "target"))
	observation, err = classifyVerifiedSend(duplicate, matchingAnchorStarts(duplicate, anchors), len(anchors), "target")
	if err != nil || observation.Confirmed || observation.Reason == "" {
		t.Fatalf("两条相同指纹必须歧义: observation=%+v err=%v", observation, err)
	}

	missing := []protocol.ThreadMessage{threadMessage(protocol.MessageDirectionOut, "target")}
	observation, err = classifyVerifiedSend(missing, matchingAnchorStarts(missing, anchors), len(anchors), "target")
	if err != nil || observation.Confirmed || observation.Reason == "" {
		t.Fatalf("没有 expectedTail 不得确认: observation=%+v err=%v", observation, err)
	}
}

func TestMatchingAnchorStartsTreatsRepeatedTailAsAmbiguous(t *testing.T) {
	anchors := []protocol.MessageAnchor{{Direction: protocol.MessageDirectionIn, ContentHash: "a"}}
	messages := []protocol.ThreadMessage{
		threadMessage(protocol.MessageDirectionIn, "a"),
		threadMessage(protocol.MessageDirectionOut, "target"),
		threadMessage(protocol.MessageDirectionIn, "a"),
		threadMessage(protocol.MessageDirectionOut, "target"),
	}
	starts := matchingAnchorStarts(messages, anchors)
	if len(starts) != 2 {
		t.Fatalf("预置应找到两次 tail: %v", starts)
	}
	observation, err := classifyVerifiedSend(messages, starts, len(anchors), "target")
	if err != nil || observation.Confirmed || observation.Reason == "" {
		t.Fatalf("重复 tail 必须歧义: observation=%+v err=%v", observation, err)
	}
}
