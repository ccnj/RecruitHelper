package protocol

import (
	"reflect"
	"strings"
	"testing"
)

// 手写 body 结构的 json tag 必须 ⊆ 契约生成的 KindBodyFields[kind]。
// 这是"零手写字段漂移"的构造性保证:写错字段名、用了别名,测试即红。
func TestBodyFieldsSubsetOfContract(t *testing.T) {
	samples := map[Kind]any{
		KindHello:   HelloBody{},
		KindWelcome: WelcomeBody{},
		KindBye:     ByeBody{},
		KindPing:    PingBody{},
		KindPong:    PongBody{},
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
		}
	}
}
