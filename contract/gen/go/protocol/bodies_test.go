package protocol

import (
	"reflect"
	"strings"
	"testing"
)

// 生成 body 结构的 json tag 必须与同一 schema 生成的 KindBodyFields[kind] 完全一致。
func TestGeneratedBodyFieldsEqualContract(t *testing.T) {
	samples := map[Kind]any{
		KindHello:    HelloBody{},
		KindWelcome:  WelcomeBody{},
		KindBye:      ByeBody{},
		KindCmd:      CmdBody{},
		KindAck:      AckBody{},
		KindResult:   ResultBody{},
		KindPing:     PingBody{},
		KindPong:     PongBody{},
		KindProgress: ProgressBody{},
		KindEvent:    EventBody{},
		KindQuery:    QueryBody{},
		KindReport:   ReportBody{},
		KindCancel:   CancelBody{},
	}
	for kind, sample := range samples {
		allowed := map[string]bool{}
		for _, f := range KindBodyFields[kind] {
			allowed[f] = true
		}
		if len(allowed) == 0 {
			t.Errorf("KindBodyFields 缺少 %s 的字段清单", kind)
			continue
		}
		typ := reflect.TypeOf(sample)
		seen := map[string]bool{}
		for i := range typ.NumField() {
			tag := typ.Field(i).Tag.Get("json")
			name, _, _ := strings.Cut(tag, ",")
			if name == "" || name == "-" {
				t.Errorf("%s.%s 缺 json tag", kind, typ.Field(i).Name)
				continue
			}
			if !allowed[name] {
				t.Errorf("%s 的 body 字段 %q 不在契约 KindBodyFields[%s]=%v 内(疑似别名漂移)",
					kind, name, kind, KindBodyFields[kind])
			}
			seen[name] = true
		}
		for name := range allowed {
			if !seen[name] {
				t.Errorf("%s 的生成结构缺少契约字段 %q", kind, name)
			}
		}
	}
}
