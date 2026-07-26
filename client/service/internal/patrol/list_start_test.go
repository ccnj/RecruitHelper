package patrol

import (
	"context"
	"reflect"
	"testing"

	"recruithelper/contract/gen/go/protocol"
)

func TestConversationListCursorContinuationOmitsFreshStart(t *testing.T) {
	h := newHarness(t)
	var got []protocol.ChatReadListArgs
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			got = append(got, args)
			if args.Cursor == "" {
				next := "page-two"
				return protocol.ChatReadListData{
					Sessions:   []protocol.ConversationSummary{},
					Complete:   false,
					NextCursor: &next,
				}, nil
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{},
				Complete: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	if len(got) != 2 ||
		got[0].Cursor != "" ||
		got[0].StartAt != protocol.ListStartTop ||
		got[1].Cursor != "page-two" ||
		got[1].StartAt != "" {
		t.Fatalf("游标续页不得混入 fresh 起点: %+v", got)
	}
}

func TestConversationListFilterSwitchesRestartFromTop(t *testing.T) {
	h := newHarness(t)
	setUnreadHintForTest(h, ptr(0))
	allReads := 0
	var got []protocol.ChatReadListArgs
	h.runner.handler = func(request RunRequest) (any, error) {
		switch request.Name {
		case protocol.PrimChatReadList:
			args := decodeArgs[protocol.ChatReadListArgs](t, request)
			got = append(got, args)
			switch args.Filter {
			case protocol.ListFilterAll:
				allReads++
				if allReads == 1 {
					setUnreadHintForTest(h, ptr(1))
					return protocol.ChatReadListData{
						Sessions: []protocol.ConversationSummary{
							summary("ordinary-before-unread", "peer-before-unread", "普通列表", 0),
						},
						Complete: true,
					}, nil
				}
			case protocol.ListFilterUnread:
				setUnreadHintForTest(h, ptr(0))
			}
			return protocol.ChatReadListData{
				Sessions: []protocol.ConversationSummary{},
				Complete: true,
			}, nil
		default:
			return defaultHandler(request)
		}
	}

	result, err := h.manager.Tick(context.Background())
	if err != nil || len(result.Rounds) != 1 || result.Rounds[0].Err != nil {
		t.Fatalf("Tick = %+v, %v", result, err)
	}
	gotFilters := make([]protocol.ListFilter, 0, len(got))
	gotStarts := make([]protocol.ListStart, 0, len(got))
	for _, args := range got {
		gotFilters = append(gotFilters, args.Filter)
		gotStarts = append(gotStarts, args.StartAt)
		if args.Cursor != "" {
			t.Fatalf("筛选切换必须建立 fresh 快照: %+v", got)
		}
	}
	if want := []protocol.ListFilter{
		protocol.ListFilterAll,
		protocol.ListFilterUnread,
		protocol.ListFilterAll,
	}; !reflect.DeepEqual(gotFilters, want) {
		t.Fatalf("筛选切换顺序错误: got=%v want=%v", gotFilters, want)
	}
	if want := []protocol.ListStart{
		protocol.ListStartTop,
		protocol.ListStartTop,
		protocol.ListStartTop,
	}; !reflect.DeepEqual(gotStarts, want) {
		t.Fatalf("筛选切换必须从列表顶部建立 fresh 快照: got=%v want=%v", gotStarts, want)
	}
}
