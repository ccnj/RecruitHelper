package communication

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"recruithelper/client/service/internal/m5ai"
)

func fixedPhrasePackage(content string) m5ai.JobConfigDocumentPackage {
	return m5ai.JobConfigDocumentPackage{Documents: []m5ai.JobConfigDocument{
		{DocType: "职位筛选", Content: "不相关文档"},
		{DocType: v4FixedPhraseDocType, Content: content},
	}}
}

func TestBuildV4FixedPhraseViewMapsOnlyApprovedScenesAndPreservesOrder(t *testing.T) {
	view, err := BuildV4FixedPhraseView(fixedPhrasePackage(`{
  "candidateAskWechat": {"message":"暂不启用","messages":["暂不启用"],"actions":[],"enabled":true},
  "meetingAccepted": {"message":"确认一","messages":["确认一","确认二"],"actions":["legacy-action"],"enabled":true},
  "meetingInvitePending": {"message":"不能当三档","messages":["不能当三档"],"actions":[],"enabled":true},
  "rejectWechat": {"message":" 挽留一 ","messages":[" 挽留一 ","挽留二","  "],"actions":["invite-wechat"],"enabled":true},
  "silence48Wechat": {"message":"冷催二","messages":["冷催二"],"actions":[],"enabled":true},
  "wechatAccepted": {"message":"收到","messages":["收到"],"actions":[],"enabled":true}
}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[V4FixedPhraseKind]string{
		V4PhraseRejectionRetention: "挽留一\n挽留二",
		V4PhraseRejectionClosing:   v4LocalRejectionClosingText,
		V4PhraseColdWechat:         "冷催二",
		V4PhraseWechatReceipt:      "收到",
		V4PhraseInterviewAccepted:  "确认一\n确认二",
	}
	wantMessages := map[V4FixedPhraseKind][]string{
		V4PhraseRejectionRetention: {"挽留一", "挽留二"},
		V4PhraseRejectionClosing:   {v4LocalRejectionClosingText},
		V4PhraseColdWechat:         {"冷催二"},
		V4PhraseWechatReceipt:      {"收到"},
		V4PhraseInterviewAccepted:  {"确认一", "确认二"},
	}
	for kind, text := range want {
		phrase := view.Phrase(kind)
		if phrase.State != V4PhraseAvailable ||
			phrase.Text != text ||
			!reflect.DeepEqual(phrase.Messages, wantMessages[kind]) {
			t.Fatalf("场景映射错误 kind=%s phrase=%+v", kind, phrase)
		}
	}
	if len(view.Phrases) != 5 {
		t.Fatalf("未批准场景不应进入可执行视图: %+v", view.Phrases)
	}
}

func TestBuildV4FixedPhraseViewDiscardsLegacyActionsAsAuthority(t *testing.T) {
	base := `{"rejectWechat":{"message":"挽留","messages":["挽留"],"actions":[%s],"enabled":true}}`
	first, err := BuildV4FixedPhraseView(fixedPhrasePackage(strings.Replace(base, "%s", `"unknown-a"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildV4FixedPhraseView(fixedPhrasePackage(strings.Replace(base, "%s", `"unknown-b","unknown-c"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Phrase(V4PhraseRejectionRetention), second.Phrase(V4PhraseRejectionRetention)) {
		t.Fatalf("旧 actions 不得改变新系统动作授权: first=%+v second=%+v", first, second)
	}
}

func TestBuildV4FixedPhraseViewDegradesOnlyBrokenScene(t *testing.T) {
	view, err := BuildV4FixedPhraseView(fixedPhrasePackage(`{
  "meetingAccepted":{"message":"确认","messages":["确认"],"actions":[],"enabled":true},
  "rejectWechat":{"message":"镜像不同","messages":["实际正文"],"actions":[],"enabled":true},
  "silence48Wechat":{"message":"禁用","messages":["禁用"],"actions":[],"enabled":false},
  "wechatAccepted":{"message":" ","messages":[" ",""],"actions":[]}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := view.Phrase(V4PhraseInterviewAccepted); got.State != V4PhraseAvailable || got.Text != "确认" {
		t.Fatalf("一个坏场景不应阻断其他场景: %+v", got)
	}
	if got := view.Phrase(V4PhraseRejectionRetention); got.State != V4PhraseInvalid ||
		len(got.Messages) != 0 ||
		got.Text != "" {
		t.Fatalf("兼容镜像不一致必须只禁用本场景: %+v", got)
	}
	if got := view.Phrase(V4PhraseColdWechat); got.State != V4PhraseDisabled {
		t.Fatalf("显式禁用未保留: %+v", got)
	}
	if got := view.Phrase(V4PhraseWechatReceipt); got.State != V4PhraseEmpty {
		t.Fatalf("空消息未保守降级: %+v", got)
	}
}

func TestBuildV4FixedPhraseViewSupportsLegacyMessageFallback(t *testing.T) {
	view, err := BuildV4FixedPhraseView(fixedPhrasePackage(`{
  "wechatAccepted":{"message":"兼容单消息","actions":[]}
}`))
	if err != nil || view.Phrase(V4PhraseWechatReceipt).State != V4PhraseAvailable ||
		!reflect.DeepEqual(view.Phrase(V4PhraseWechatReceipt).Messages, []string{"兼容单消息"}) ||
		view.Phrase(V4PhraseWechatReceipt).Text != "兼容单消息" {
		t.Fatalf("messages 缺失时没有使用旧 message 镜像: view=%+v err=%v", view, err)
	}
}

func TestBuildV4FixedPhraseViewDoesNotSplitInsideMessageItem(t *testing.T) {
	view, err := BuildV4FixedPhraseView(fixedPhrasePackage(`{
  "rejectWechat":{
    "message":"第一项。包含两句话。仍是一项。",
    "messages":[
      "第一项。包含两句话。仍是一项。",
      "第二项第一行。\n第二项第二行。"
    ],
    "actions":[]
  }
}`))
	phrase := view.Phrase(V4PhraseRejectionRetention)
	want := []string{
		"第一项。包含两句话。仍是一项。",
		"第二项第一行。\n第二项第二行。",
	}
	if err != nil ||
		phrase.State != V4PhraseAvailable ||
		!reflect.DeepEqual(phrase.Messages, want) ||
		phrase.Text != strings.Join(want, "\n") {
		t.Fatalf("固定话术数组项边界被标点或项内换行改写: phrase=%+v err=%v", phrase, err)
	}
}

func TestBuildV4FixedPhraseViewKeepsMissingDocumentAndScenesLocal(t *testing.T) {
	view, err := BuildV4FixedPhraseView(m5ai.JobConfigDocumentPackage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, mapping := range v4FixedPhraseScenes {
		if got := view.Phrase(mapping.kind); got.State != V4PhraseMissing || got.SourceScene != mapping.scene {
			t.Fatalf("缺文档时每个分支应独立标缺: %+v", got)
		}
	}
	if got := view.Phrase(V4PhraseRejectionClosing); got.State != V4PhraseAvailable ||
		got.SourceScene != v4LocalRejectionClosingScene ||
		!reflect.DeepEqual(got.Messages, []string{v4LocalRejectionClosingText}) ||
		got.Text != v4LocalRejectionClosingText {
		t.Fatalf("本地拒绝收场默认不应依赖旧后台文档: %+v", got)
	}

	view, err = BuildV4FixedPhraseView(fixedPhrasePackage(`{"rejectWechat":{"message":"挽留","messages":["挽留"],"actions":[]}}`))
	if err != nil || view.Phrase(V4PhraseRejectionRetention).State != V4PhraseAvailable ||
		view.Phrase(V4PhraseWechatReceipt).State != V4PhraseMissing {
		t.Fatalf("缺场景不应阻断已有场景: view=%+v err=%v", view, err)
	}
}

func TestBuildV4FixedPhraseViewRejectsBrokenPackageBoundaries(t *testing.T) {
	duplicate := fixedPhrasePackage(`{}`)
	duplicate.Documents = append(duplicate.Documents, m5ai.JobConfigDocument{DocType: v4FixedPhraseDocType, Content: `{}`})
	cases := []m5ai.JobConfigDocumentPackage{
		fixedPhrasePackage(``),
		fixedPhrasePackage(`[]`),
		fixedPhrasePackage(`{"rejectWechat":`),
		duplicate,
	}
	for index, source := range cases {
		if _, err := BuildV4FixedPhraseView(source); !errors.Is(err, ErrInvalidV4FixedPhrasePackage) {
			t.Fatalf("非法原包[%d]没有响亮失败: %v", index, err)
		}
	}
}

func TestBuildV4FixedPhraseViewMarksIllegalKnownSceneShapeInvalid(t *testing.T) {
	cases := []string{
		`{"rejectWechat":[]}`,
		`{"rejectWechat":{"enabled":null,"messages":["x"]}}`,
		`{"rejectWechat":{"enabled":"true","messages":["x"]}}`,
		`{"rejectWechat":{"message":1,"messages":["x"]}}`,
		`{"rejectWechat":{"messages":null}}`,
		`{"rejectWechat":{"messages":[1]}}`,
		`{"rejectWechat":{"messages":["x"],"actions":null}}`,
		`{"rejectWechat":{"messages":["x"],"actions":[1]}}`,
	}
	for index, content := range cases {
		view, err := BuildV4FixedPhraseView(fixedPhrasePackage(content))
		if err != nil || view.Phrase(V4PhraseRejectionRetention).State != V4PhraseInvalid {
			t.Fatalf("非法场景[%d]没有局部降级: view=%+v err=%v", index, view, err)
		}
	}
}

func TestRenderV4FixedPhraseUsesOnlyApprovedSalutationPlaceholder(t *testing.T) {
	rendered, err := RenderV4FixedPhrase(
		" {称呼}您好，稍后联系{称呼}。 ",
		V4FixedPhraseRenderInput{Salutation: "候选人女士"},
	)
	if err != nil || rendered != "候选人女士您好，稍后联系候选人女士。" {
		t.Fatalf("固定话术没有稳定渲染称呼: rendered=%q err=%v", rendered, err)
	}

	plain, err := RenderV4FixedPhrase(
		"好的，稍后联系。",
		V4FixedPhraseRenderInput{},
	)
	if err != nil || plain != "好的，稍后联系。" {
		t.Fatalf("无占位固定话术不应依赖称呼: rendered=%q err=%v", plain, err)
	}
}

func TestRenderV4FixedPhraseRejectsMissingUnknownAndResidualPlaceholders(t *testing.T) {
	tests := []struct {
		name       string
		template   string
		salutation string
	}{
		{name: "missing salutation", template: "{称呼}您好"},
		{name: "unknown", template: "{姓名}您好", salutation: "候选人女士"},
		{name: "unclosed", template: "{称呼您好", salutation: "候选人女士"},
		{name: "stray close", template: "称呼}您好", salutation: "候选人女士"},
		{name: "placeholder in value", template: "{称呼}您好", salutation: "{姓名}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if rendered, err := RenderV4FixedPhrase(
				test.template,
				V4FixedPhraseRenderInput{Salutation: test.salutation},
			); !errors.Is(err, ErrInvalidV4FixedPhraseRender) || rendered != "" {
				t.Fatalf("非法固定话术必须响亮失败: rendered=%q err=%v", rendered, err)
			}
		})
	}
}
