// contract/codegen:从 contract/contract.v1.json 生成 Go/TypeScript 类型、常量与运行时校验。
//
// 用法(仓库根目录执行):
//
//	go run ./contract/codegen          生成(覆盖 contract/gen/ 下产物)
//	go run ./contract/codegen -check   校验产物与契约一致(CI 用;漂移退出码 1)
//
// 生成规则:
//   - schemas.types 是 body/args/data/event 类型与校验的唯一机器源;已知字段按 schema 校验,未知加法字段必须忽略。
//   - 契约中 "$" 开头的键是文档注释,不进产物;各对象里的散文字段(note/redispatch 等)只作说明。
//   - 产物确定性:键一律排序、无时间戳;ContractHash = sha256(契约文件原始字节)。
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var (
	contractPath = flag.String("contract", "contract/contract.v1.json", "契约文件路径")
	goOut        = flag.String("go-out", "contract/gen/go/protocol/protocol.gen.go", "Go 产物路径")
	goTypesOut   = flag.String("go-types-out", "contract/gen/go/protocol/types.gen.go", "Go 类型与校验产物路径")
	tsOut        = flag.String("ts-out", "contract/gen/ts/protocol.gen.ts", "TS 产物路径")
	check        = flag.Bool("check", false, "只校验产物是否与契约一致,不写文件")
)

func main() {
	flag.Parse()

	raw, err := os.ReadFile(*contractPath)
	must(err, "读契约文件")
	var c map[string]any
	must(json.Unmarshal(raw, &c), "解析契约 JSON")

	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	assertContract(c)

	goSrc := genGo(c, hash)
	goTypesSrc := genGoTypes(c)
	tsSrc := genTS(c, hash)

	formatted, err := format.Source(goSrc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成的 Go 源码无法通过 gofmt(生成器 bug):", err)
		fmt.Fprintln(os.Stderr, withLineNumbers(string(goSrc)))
		os.Exit(1)
	}
	formattedTypes, err := format.Source(goTypesSrc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成的 Go 类型源码无法通过 gofmt(生成器 bug):", err)
		fmt.Fprintln(os.Stderr, withLineNumbers(string(goTypesSrc)))
		os.Exit(1)
	}

	outputs := []struct {
		path string
		data []byte
	}{
		{*goOut, formatted},
		{*goTypesOut, formattedTypes},
		{*tsOut, tsSrc},
	}

	if *check {
		drift := false
		for _, o := range outputs {
			existing, err := os.ReadFile(o.path)
			switch {
			case err != nil:
				fmt.Printf("缺失: %s\n", o.path)
				drift = true
			case string(existing) != string(o.data):
				fmt.Printf("漂移: %s(请重跑 go run ./contract/codegen)\n", o.path)
				drift = true
			default:
				fmt.Printf("一致: %s\n", o.path)
			}
		}
		if drift {
			os.Exit(1)
		}
		return
	}

	for _, o := range outputs {
		must(os.MkdirAll(filepath.Dir(o.path), 0o755), "建目录")
		must(os.WriteFile(o.path, o.data, 0o644), "写 "+o.path)
		fmt.Printf("生成: %s(%d 字节)\n", o.path, len(o.data))
	}
}

// ---------- 契约一致性断言(生成前置检查) ----------

func assertContract(c map[string]any) {
	// 信封字段集必须与生成器映射表一致——契约加了信封字段而生成器不知道,必须响亮失败。
	envFields := obj(obj(c, "envelope"), "fields")
	want := map[string]bool{"proto": true, "kind": true, "msgId": true, "session": true, "ts": true, "attempt": true, "body": true}
	for k := range envFields {
		if !want[k] {
			die("契约 envelope.fields 出现生成器不认识的字段 %q:请同步更新 codegen 的信封映射", k)
		}
	}
	for k := range want {
		if _, ok := envFields[k]; !ok {
			die("契约 envelope.fields 缺少字段 %q", k)
		}
	}
	// kinds.report.states 与 enums.reportState 必须一致(同一事实只许一处定义的例外,双写必须相等)。
	rs := strSlice(obj(obj(c, "kinds"), "report")["states"])
	es := strSlice(obj(c, "enums")["reportState"])
	if strings.Join(rs, ",") != strings.Join(es, ",") {
		die("kinds.report.states 与 enums.reportState 不一致:%v vs %v", rs, es)
	}

	compat := obj(c, "compatibility")
	if str(compat["unknownFields"]) != "must-ignore" {
		die("compatibility.unknownFields 必须是 must-ignore")
	}
	if str(compat["contractHash"]) != "warn-only" {
		die("compatibility.contractHash 必须是 warn-only")
	}
	if str(compat["jsonInteger"]) != "safe-int53" {
		die("compatibility.jsonInteger 必须是 safe-int53,保证 Go/TS 数字一致")
	}

	types := obj(obj(c, "schemas"), "types")
	// 首个客户安装前已经冻结为“本地可信 + Origin 边界”的零配对握手。
	// 这些断言防止历史 auth/issued/配对默认值被误抄回唯一契约。
	if err := validateLocalHandshakeContract(c); err != nil {
		die("%v", err)
	}
	for _, name := range sortedKeys(types) {
		assertSchemaNode(c, types, "schemas.types."+name, obj(types, name))
	}
	assertSchemaRef(types, "errorObject.schema", str(obj(c, "errorObject")["schema"]))

	bodies := obj(c, "bodies")
	for _, kind := range sortedKeys(obj(c, "kinds")) {
		body, ok := bodies[kind].(map[string]any)
		if !ok {
			die("bodies 缺少 kind %q", kind)
		}
		assertSchemaRef(types, "bodies."+kind+".schema", str(body["schema"]))
	}
	for kind := range bodies {
		if _, ok := obj(c, "kinds")[kind]; !ok {
			die("bodies 出现未知 kind %q", kind)
		}
	}

	for _, name := range sortedKeys(obj(c, "primitives")) {
		p := obj(obj(c, "primitives"), name)
		if boolval(p["contextOptionalBeforeBinding"]) {
			if name != "probe.platform" || str(p["class"]) != "readonly" || intval(p["ver"]) == 0 {
				die("primitives.%s.contextOptionalBeforeBinding 只允许激活的 readonly probe.platform", name)
			}
		}
		if intval(p["ver"]) == 0 {
			continue
		}
		assertSchemaRef(types, "primitives."+name+".argsSchema", str(p["argsSchema"]))
		assertSchemaRef(types, "primitives."+name+".dataSchema", str(p["dataSchema"]))
	}
	for _, name := range sortedKeys(obj(c, "events")) {
		e := obj(obj(c, "events"), name)
		assertSchemaRef(types, "events."+name+".dataSchema", str(e["dataSchema"]))
	}

	// receipt 阶段错误与 ack(rejected) 白名单必须是一组事实。
	var receipt []string
	for _, code := range sortedKeys(obj(c, "errorCodes")) {
		if str(obj(obj(c, "errorCodes"), code)["phase"]) == "receipt" {
			receipt = append(receipt, code)
		}
	}
	listed := strSlice(obj(c, "commandAcceptance")["ackRejectedErrorCodes"])
	sort.Strings(listed)
	if strings.Join(receipt, ",") != strings.Join(listed, ",") {
		die("receipt 错误码与 commandAcceptance.ackRejectedErrorCodes 不一致:%v vs %v", receipt, listed)
	}
	expired := obj(obj(c, "commandAcceptance"), "expiredAtReceipt")
	if str(expired["ack"]) != "accepted" || str(expired["result"]) != "expired" || boolval(expired["invokeHandler"]) || !boolval(expired["bypassQueueCapacity"]) {
		die("commandAcceptance.expiredAtReceipt 必须冻结为 accepted→expired、零 handler、绕过 FIFO 容量")
	}

	// 重复在 defaults 与 schema 中出现的硬边界必须由断言锁成同一事实。
	defs := obj(c, "defaults")
	payload := obj(defs, "payload")
	lease := obj(defs, "lease")
	pagination := obj(defs, "pagination")
	assertIntEqual("inlineResultDataBytes", intval(payload["inlineResultDataBytes"]), schemaFieldInt(types, "ResultBody", "data", "maxJsonBytes"))
	assertIntEqual("inlineMessageTextBytes", intval(payload["inlineMessageTextBytes"]), schemaFieldInt(types, "ThreadMessage", "text", "maxBytes"))
	assertIntEqual("maxEvidenceTextBytes", intval(payload["maxEvidenceTextBytes"]), schemaFieldInt(types, "Evidence", "text", "maxBytes"))
	assertIntEqual("blobMaxBytes", intval(payload["blobMaxBytes"]), schemaFieldInt(types, "BlobParams", "maxBytes", "maximum"))
	assertIntEqual("lease.minMs", intval(lease["minMs"]), schemaFieldInt(types, "CmdBody", "leaseMs", "minimum"))
	assertIntEqual("lease.maxMs", intval(lease["maxMs"]), schemaFieldInt(types, "CmdBody", "leaseMs", "maximum"))
	assertIntEqual("pagination.readListMaxItems(args)", intval(pagination["readListMaxItems"]), schemaFieldInt(types, "ChatReadListArgs", "maxSessions", "maximum"))
	assertIntEqual("pagination.readListMaxItems(data)", intval(pagination["readListMaxItems"]), schemaFieldInt(types, "ChatReadListData", "sessions", "maxItems"))
	assertIntEqual("pagination.readThreadMaxItems(args)", intval(pagination["readThreadMaxItems"]), schemaFieldInt(types, "ThreadWindow", "maxMessages", "maximum"))
	assertIntEqual("pagination.readThreadMaxItems(data)", intval(pagination["readThreadMaxItems"]), schemaFieldInt(types, "ChatReadThreadData", "messages", "maxItems"))
	assertIntEqual("pagination.cursorMaxBytes(list args)", intval(pagination["cursorMaxBytes"]), schemaFieldInt(types, "ChatReadListArgs", "cursor", "maxBytes"))
	assertIntEqual("pagination.cursorMaxBytes(thread args)", intval(pagination["cursorMaxBytes"]), schemaFieldInt(types, "ChatReadThreadArgs", "cursor", "maxBytes"))
	assertIntEqual("readList data byte budget", intval(payload["inlineResultDataBytes"]), schemaTypeInt(types, "ChatReadListData", "maxJsonBytes"))
	assertIntEqual("readThread data byte budget", intval(payload["inlineResultDataBytes"]), schemaTypeInt(types, "ChatReadThreadData", "maxJsonBytes"))
	assertIntEqual("error data byte budget", intval(payload["inlineResultDataBytes"]), schemaFieldInt(types, "ErrorBody", "data", "maxJsonBytes"))

	// 保守按所有字符串每输入字节/码点最多转成 6 字节 JSON escape 计算。
	// failed result 可同时带 error.evidence 与 result.evidence,但只会有一份 data。
	resultEvidenceItems := schemaFieldInt(types, "ResultBody", "evidence", "maxItems")
	errorEvidenceItems := schemaFieldInt(types, "ErrorBody", "evidence", "maxItems")
	evidenceStringBound := 6 * (intval(payload["maxEvidenceTextBytes"]) + schemaFieldInt(types, "Evidence", "type", "maxLength") + schemaFieldInt(types, "Evidence", "blob", "maxLength"))
	errorMessageBound := 6 * schemaFieldInt(types, "ErrorBody", "message", "maxBytes")
	resultWorstBound := intval(payload["inlineResultDataBytes"]) + (resultEvidenceItems+errorEvidenceItems)*(evidenceStringBound+256) + errorMessageBound + 8192
	if resultWorstBound >= intval(defs["maxMsgBytes"]) {
		die("ResultBody 保守上界 %d 未低于 maxMsgBytes %d", resultWorstBound, intval(defs["maxMsgBytes"]))
	}
}

// validateLocalHandshakeContract 是可单测的回潮门禁。运行时仍遵守 must-ignore
// 未知加法字段；这里拒绝的是“唯一机器契约”重新声明已经退役的字段。
func validateLocalHandshakeContract(c map[string]any) error {
	schemas, ok := c["schemas"].(map[string]any)
	if !ok {
		return fmt.Errorf("契约缺少对象 %q", "schemas")
	}
	types, ok := schemas["types"].(map[string]any)
	if !ok {
		return fmt.Errorf("契约缺少对象 %q", "schemas.types")
	}
	hello, ok := types["HelloBody"].(map[string]any)
	if !ok {
		return fmt.Errorf("契约缺少对象 %q", "schemas.types.HelloBody")
	}
	helloFields, ok := hello["fields"].(map[string]any)
	if !ok {
		return fmt.Errorf("契约缺少对象 %q", "schemas.types.HelloBody.fields")
	}
	if _, ok := helloFields["auth"]; ok {
		return fmt.Errorf("schemas.types.HelloBody.auth 已退役:本地握手不得恢复 token 鉴权")
	}
	handID, ok := helloFields["handId"].(map[string]any)
	if !ok || handID["type"] != "string" || handID["nullable"] == true || handID["optional"] == true {
		return fmt.Errorf("schemas.types.HelloBody.handId 必须是 required non-null string")
	}
	if _, ok := types["IssuedCreds"]; ok {
		return fmt.Errorf("schemas.types.IssuedCreds 已退役:handId 由手本地生成,脑不得签发凭据")
	}
	welcome, ok := types["WelcomeBody"].(map[string]any)
	if !ok {
		return fmt.Errorf("契约缺少对象 %q", "schemas.types.WelcomeBody")
	}
	welcomeFields, ok := welcome["fields"].(map[string]any)
	if !ok {
		return fmt.Errorf("契约缺少对象 %q", "schemas.types.WelcomeBody.fields")
	}
	if _, ok := welcomeFields["issued"]; ok {
		return fmt.Errorf("schemas.types.WelcomeBody.issued 已退役:welcome 不得签发凭据")
	}
	defaults, ok := c["defaults"].(map[string]any)
	if !ok {
		return fmt.Errorf("契约缺少对象 %q", "defaults")
	}
	for _, old := range []string{"pairingWindowMs", "pairingHelloTimeoutMs", "preSessionPingMs"} {
		if _, ok := defaults[old]; ok {
			return fmt.Errorf("defaults.%s 已随配对流程退役", old)
		}
	}
	if defaults["helloTimeoutMs"] != float64(10000) {
		return fmt.Errorf("defaults.helloTimeoutMs 必须保持 10000")
	}
	byeCodes, ok := c["byeCodes"].([]any)
	if !ok {
		return fmt.Errorf("byeCodes 必须是数组")
	}
	for _, old := range []string{"AUTH_FAILED", "PAIRING_TIMEOUT", "PAIRING_REJECTED"} {
		for _, code := range byeCodes {
			if code == old {
				return fmt.Errorf("byeCodes.%s 已随配对/鉴权流程退役", old)
			}
		}
	}
	return nil
}

func assertSchemaRef(types map[string]any, path, name string) {
	if name == "" {
		die("%s 缺少 schema 名", path)
	}
	if _, ok := types[name]; !ok {
		die("%s 引用不存在的 schema %q", path, name)
	}
}

func assertSchemaNode(c, types map[string]any, path string, node map[string]any) {
	if ref := str(node["ref"]); ref != "" {
		assertSchemaRef(types, path+".ref", ref)
		return
	}
	typ := str(node["type"])
	switch typ {
	case "any", "boolean", "string", "int", "int64":
	case "array":
		items, ok := node["items"].(map[string]any)
		if !ok {
			die("%s.items 缺少对象 schema", path)
		}
		assertSchemaNode(c, types, path+".items", items)
	case "object":
		fields, ok := node["fields"].(map[string]any)
		if !ok && !boolval(node["raw"]) {
			die("%s.fields 缺少对象", path)
		}
		for _, field := range sortedKeys(fields) {
			assertSchemaNode(c, types, path+".fields."+field, obj(fields, field))
		}
	default:
		die("%s.type 不支持 %q", path, typ)
	}
	if enumRef := str(node["enumRef"]); enumRef != "" {
		if _, ok := schemaEnumValues(c)[enumRef]; !ok {
			die("%s.enumRef 引用不存在的枚举 %q", path, enumRef)
		}
	}
}

func schemaFieldInt(types map[string]any, typeName, fieldName, key string) int64 {
	t := obj(types, typeName)
	f := obj(obj(t, "fields"), fieldName)
	return intval(f[key])
}

func schemaTypeInt(types map[string]any, typeName, key string) int64 {
	return intval(obj(types, typeName)[key])
}

func assertIntEqual(name string, a, b int64) {
	if a != b {
		die("%s 边界重复定义不一致:%d vs %d", name, a, b)
	}
}

// ---------- Go 产物 ----------

func genGo(c map[string]any, hash string) []byte {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	w("// Code generated by contract/codegen from %s. DO NOT EDIT.", *contractPath)
	w("")
	w("package protocol")
	w("")
	w(`import "encoding/json"`)
	w("")
	w("// 协议主版本与契约指纹")
	w("const (")
	w("\tProtoVersion = %d", intval(c["proto"]))
	w("\tContractHash = %q", hash)
	w("\tUnknownFieldPolicy = %q", str(obj(c, "compatibility")["unknownFields"]))
	w("\tContractHashPolicy = %q", str(obj(c, "compatibility")["contractHash"]))
	w("\tJSONIntegerPolicy = %q", str(obj(c, "compatibility")["jsonInteger"]))
	w(")")
	w("")

	tr := obj(c, "transport")
	w("// 传输常量")
	w("const (")
	w("\tTransportScheme  = %q", str(tr["scheme"]))
	w("\tTransportHost    = %q", str(tr["host"]))
	w("\tDefaultPort      = %d", intval(tr["portDefault"]))
	w("\tTransportPath    = %q", str(tr["path"]))
	w("\tOriginPrefix     = %q", str(tr["originPrefix"]))
	w("\tChromeMinVersion = %d", intval(tr["chromeMin"]))
	w(")")
	w("")

	// 枚举(enums 区,含 Batch/AckStatus/ResultStatus/Retryable/SideEffect/ReportState/NotReadyReason)
	enums := obj(c, "enums")
	for _, name := range sortedKeys(enums) {
		typ := pascal(name)
		vals := strSlice(enums[name])
		w("type %s string", typ)
		w("")
		w("const (")
		for _, v := range vals {
			w("\t%s%s %s = %q", typ, pascal(v), typ, v)
		}
		w(")")
		w("")
		w("var %sValues = []%s{", typ, typ)
		for _, v := range vals {
			w("\t%s%s,", typ, pascal(v))
		}
		w("}")
		w("")
	}

	// Kind
	kinds := obj(c, "kinds")
	w("type Kind string")
	w("")
	w("const (")
	for _, k := range sortedKeys(kinds) {
		w("\tKind%s Kind = %q", pascal(k), k)
	}
	w(")")
	w("")
	w("// KindMeta:方向、应答方、批次、是否可丢(lossy)。")
	w("type KindMeta struct {")
	w("\tDir   string // H2B / B2H / both")
	w("\tAckBy string // 应答该消息的 kind;空=无应答")
	w("\tBatch Batch")
	w("\tLossy bool // true=可丢(无重发、无补投)")
	w("}")
	w("")
	w("var Kinds = map[Kind]KindMeta{")
	for _, k := range sortedKeys(kinds) {
		m := obj(kinds, k)
		w("\tKind%s: {Dir: %q, AckBy: %q, Batch: Batch%s, Lossy: %t},",
			pascal(k), str(m["dir"]), str(m["ackBy"]), pascal(str(m["batch"])), boolval(m["lossy"]))
	}
	w("}")
	w("")

	features := obj(c, "features")
	w("type Feature string")
	w("")
	w("const (")
	for _, name := range sortedKeys(features) {
		w("\tFeature%s Feature = %q", pascal(name), name)
	}
	w(")")
	w("")
	w("var Features = map[Feature]Batch{")
	for _, name := range sortedKeys(features) {
		w("\tFeature%s: Batch%s,", pascal(name), pascal(str(obj(features, name)["batch"])))
	}
	w("}")
	w("")

	// 信封
	w("// Envelope:所有消息的信封(7 字段,冻结;扩展只进 Body)。")
	w("type Envelope struct {")
	w("\tProto   int             `json:\"proto\"`")
	w("\tKind    Kind            `json:\"kind\"`")
	w("\tMsgID   string          `json:\"msgId\"`")
	w("\tSession *string         `json:\"session\"` // hello/welcome/bye 为 null")
	w("\tTs      int64           `json:\"ts\"`")
	w("\tAttempt int             `json:\"attempt\"`")
	w("\tBody    json.RawMessage `json:\"body\"`")
	w("}")
	w("")

	// 命令分类
	classes := obj(c, "classes")
	classKeys := objectKeys(classes)
	w("type CmdClass string")
	w("")
	w("const (")
	for _, k := range classKeys {
		w("\tClass%s CmdClass = %q", pascal(k), k)
	}
	w(")")
	w("")
	w("type ClassMeta struct {")
	w("\tSerialDomain bool   // 是否进串行域(账号域;debug.* 用每手 debug 域)")
	w("\tIdemKey      string // forbidden / required")
	w("\tEvidence     string")
	w("}")
	w("")
	w("var Classes = map[CmdClass]ClassMeta{")
	for _, k := range classKeys {
		m := obj(classes, k)
		w("\tClass%s: {SerialDomain: %t, IdemKey: %q, Evidence: %q},",
			pascal(k), boolval(m["serialDomain"]), str(m["idemKey"]), str(m["evidence"]))
	}
	w("}")
	w("")

	// bye 码
	byeCodes := strSlice(c["byeCodes"])
	w("type ByeCode string")
	w("")
	w("const (")
	for _, v := range byeCodes {
		w("\tByeCode%s ByeCode = %q", pascal(v), v)
	}
	w(")")
	w("")

	// 错误码
	errCodes := obj(c, "errorCodes")
	w("type ErrorCode string")
	w("")
	w("const (")
	for _, k := range sortedKeys(errCodes) {
		w("\tErrCode%s ErrorCode = %q", pascal(k), k)
	}
	w(")")
	w("")
	w("// ErrorCodeMeta:契约默认值。RetryableDefault 可为条件式描述(如 \"afterRecovery|manualOnly(SX)\"),裁决权在脑的矩阵。")
	w("type ErrorCodeMeta struct {")
	w("\tRetryableDefault string")
	w("\tSideEffect       []SideEffect // 允许取值")
	w("\tBatch            Batch")
	w("\tPhase            ErrorPhase // receipt=仅 ack(rejected);execution=accepted 后 result(failed)")
	w("}")
	w("")
	w("var ErrorCodes = map[ErrorCode]ErrorCodeMeta{")
	for _, k := range sortedKeys(errCodes) {
		m := obj(errCodes, k)
		ses := strSlice(m["sideEffect"])
		parts := make([]string, len(ses))
		for i, se := range ses {
			parts[i] = "SideEffect" + pascal(se)
		}
		w("\tErrCode%s: {RetryableDefault: %q, SideEffect: []SideEffect{%s}, Batch: Batch%s, Phase: ErrorPhase%s},",
			pascal(k), str(m["retryable"]), strings.Join(parts, ", "), pascal(str(m["batch"])), pascal(str(m["phase"])))
	}
	w("}")
	w("")
	w("var AckRejectableErrorCodes = map[ErrorCode]struct{}{")
	for _, code := range strSlice(obj(c, "commandAcceptance")["ackRejectedErrorCodes"]) {
		w("\tErrCode%s: {},", pascal(code))
	}
	w("}")
	w("")
	expired := obj(obj(c, "commandAcceptance"), "expiredAtReceipt")
	w("const (")
	w("\tExpiredAtReceiptAckStatus AckStatus = AckStatus%s", pascal(str(expired["ack"])))
	w("\tExpiredAtReceiptResultStatus ResultStatus = ResultStatus%s", pascal(str(expired["result"])))
	w("\tExpiredAtReceiptInvokeHandler = %t", boolval(expired["invokeHandler"]))
	w("\tExpiredAtReceiptBypassQueueCapacity = %t", boolval(expired["bypassQueueCapacity"]))
	w(")")
	w("")

	// 原语
	prims := obj(c, "primitives")
	w("const (")
	for _, k := range sortedKeys(prims) {
		w("\tPrim%s = %q", pascal(k), k)
	}
	w(")")
	w("")
	w("type PrimitiveMeta struct {")
	w("\tVer                int // 0=占位,未定稿")
	w("\tClass              CmdClass")
	w("\tBatch              Batch")
	w("\tPlatformSideEffect string // intrusive 专用声明;空=未声明")
	w("\tExecBudgetMs       int64  // 0=用类默认")
	w("\tDeadlineMs         int64  // 0=派发时计算")
	w("\tLeaseMs            int64  // 0=不启用租约;非零须协商 lease/1")
	w("\tArgsSchema         string")
	w("\tDataSchema         string")
	w("\tContextOptionalBeforeBinding bool // 仅首次真人绑定探测可省略 context")
	w("}")
	w("")
	w("var Primitives = map[string]PrimitiveMeta{")
	for _, k := range sortedKeys(prims) {
		m := obj(prims, k)
		w("\tPrim%s: {Ver: %d, Class: Class%s, Batch: Batch%s, PlatformSideEffect: %q, ExecBudgetMs: %d, DeadlineMs: %d, LeaseMs: %d, ArgsSchema: %q, DataSchema: %q, ContextOptionalBeforeBinding: %t},",
			pascal(k), intval(m["ver"]), pascal(str(m["class"])), pascal(str(m["batch"])),
			str(m["platformSideEffect"]), intval(m["execBudgetMs"]), intval(m["deadlineMs"]), intval(m["leaseMs"]), str(m["argsSchema"]), str(m["dataSchema"]), boolval(m["contextOptionalBeforeBinding"]))
	}
	w("}")
	w("")

	// 事件
	events := obj(c, "events")
	w("type EventName string")
	w("")
	w("const (")
	for _, k := range sortedKeys(events) {
		w("\tEvent%s EventName = %q", pascal(k), k)
	}
	w(")")
	w("")
	w("var Events = map[EventName]Batch{")
	for _, k := range sortedKeys(events) {
		w("\tEvent%s: Batch%s,", pascal(k), pascal(str(obj(events, k)["batch"])))
	}
	w("}")
	w("")

	// 默认参数(展平)
	w("// 默认参数(契约 defaults 展平;嵌套层级用驼峰连接)。welcome 可下发覆盖。")
	var consts, vars []string
	flattenDefaults("Default", obj(c, "defaults"), &consts, &vars)
	w("const (")
	for _, line := range consts {
		w("\t%s", line)
	}
	w(")")
	w("")
	for _, line := range vars {
		w("%s", line)
	}
	w("")

	// 各 kind 的线上字段名清单
	bodies := obj(c, "bodies")
	types := obj(obj(c, "schemas"), "types")
	w("// KindBodyFields:各 kind 的线上字段名清单($ 注释键已剔除)。")
	w("// 字段直接由 schemas.types 生成,未知加法字段由运行时校验忽略。")
	w("var KindBodyFields = map[Kind][]string{")
	for _, k := range sortedKeys(bodies) {
		fields := schemaObjectFields(types, str(obj(bodies, k)["schema"]))
		quoted := make([]string, len(fields))
		for i, f := range fields {
			quoted[i] = strconv.Quote(f)
		}
		w("\tKind%s: {%s},", pascal(k), strings.Join(quoted, ", "))
	}
	w("}")

	return []byte(b.String())
}

// ---------- Go 类型与运行时校验产物 ----------

func genGoTypes(c map[string]any) []byte {
	var b strings.Builder
	w := func(f string, a ...any) {
		if len(a) == 0 {
			b.WriteString(f)
			b.WriteByte('\n')
			return
		}
		fmt.Fprintf(&b, f+"\n", a...)
	}
	types := obj(obj(c, "schemas"), "types")

	w("// Code generated by contract/codegen from %s. DO NOT EDIT.", *contractPath)
	w("")
	w("package protocol")
	w("")
	w("import (")
	w("\t\"bytes\"")
	w("\t\"encoding/json\"")
	w("\t\"fmt\"")
	w("\t\"io\"")
	w("\t\"reflect\"")
	w("\t\"strings\"")
	w("\t\"unicode/utf8\"")
	w(")")
	w("")
	w("// 以下线上类型全部来自 contract.v1.json 的 schemas.types。")
	for _, name := range sortedKeys(types) {
		node := obj(types, name)
		if str(node["type"]) != "object" || boolval(node["raw"]) {
			die("schemas.types.%s 必须是具名 object", name)
		}
		w("type %s struct {", name)
		fields := obj(node, "fields")
		for _, fieldName := range sortedKeys(fields) {
			field := obj(fields, fieldName)
			tag := fieldName
			if boolval(field["optional"]) {
				tag += ",omitempty"
			}
			w("\t%s %s `json:%q`", goFieldName(fieldName), goSchemaType(field), tag)
		}
		w("}")
		w("")
	}

	w("// Encode 把生成 body/args/data/event 类型编码成信封可用的 RawMessage。")
	w("func Encode(v any) (json.RawMessage, error) {")
	w("\tb, err := json.Marshal(v)")
	w("\treturn json.RawMessage(b), err")
	w("}")
	w("")

	typesJSON, err := json.Marshal(types)
	must(err, "序列化 schemas.types")
	enumsJSON, err := json.Marshal(schemaEnumValues(c))
	must(err, "序列化 schema enums")
	bodySchemasJSON, err := json.Marshal(bodySchemaMap(c))
	must(err, "序列化 body schemas")
	primitiveSchemasJSON, err := json.Marshal(primitiveSchemaMap(c))
	must(err, "序列化 primitive schemas")
	eventSchemasJSON, err := json.Marshal(eventSchemaMap(c))
	must(err, "序列化 event schemas")

	w("type schemaRule struct {")
	w("\tKind string `json:\"kind\"`")
	w("\tField string `json:\"field\"`")
	w("\tFields []string `json:\"fields\"`")
	w("\tWhenField string `json:\"whenField\"`")
	w("\tEquals []any `json:\"equals\"`")
	w("}")
	w("")
	w("type schemaSpec struct {")
	w("\tType string `json:\"type\"`")
	w("\tRef string `json:\"ref\"`")
	w("\tEnumRef string `json:\"enumRef\"`")
	w("\tOptional bool `json:\"optional\"`")
	w("\tNullable bool `json:\"nullable\"`")
	w("\tRaw bool `json:\"raw\"`")
	w("\tFields map[string]schemaSpec `json:\"fields\"`")
	w("\tItems *schemaSpec `json:\"items\"`")
	w("\tMinimum *int64 `json:\"minimum\"`")
	w("\tMaximum *int64 `json:\"maximum\"`")
	w("\tMinItems int `json:\"minItems\"`")
	w("\tMaxItems int `json:\"maxItems\"`")
	w("\tMinLength int `json:\"minLength\"`")
	w("\tMaxLength int `json:\"maxLength\"`")
	w("\tMaxBytes int `json:\"maxBytes\"`")
	w("\tMaxJSONBytes int `json:\"maxJsonBytes\"`")
	w("\tConst *bool `json:\"const\"`")
	w("\tRules []schemaRule `json:\"rules\"`")
	w("}")
	w("")
	w("type primitiveSchema struct {")
	w("\tVer int `json:\"ver\"`")
	w("\tArgs string `json:\"args\"`")
	w("\tData string `json:\"data\"`")
	w("}")
	w("")
	w("var schemaTypes = mustDecodeSchemaMap(%q)", string(typesJSON))
	w("var schemaEnums = mustDecodeStringSlices(%q)", string(enumsJSON))
	w("var bodySchemas = mustDecodeKindSchemas(%q)", string(bodySchemasJSON))
	w("var primitiveSchemas = mustDecodePrimitiveSchemas(%q)", string(primitiveSchemasJSON))
	w("var eventSchemas = mustDecodeStringMap(%q)", string(eventSchemasJSON))
	w("")
	w("func mustDecodeSchemaMap(raw string) map[string]schemaSpec {")
	w("\tvar out map[string]schemaSpec")
	w("\tif err := json.Unmarshal([]byte(raw), &out); err != nil { panic(err) }")
	w("\treturn out")
	w("}")
	w("")
	w("func mustDecodeStringSlices(raw string) map[string][]string {")
	w("\tvar out map[string][]string")
	w("\tif err := json.Unmarshal([]byte(raw), &out); err != nil { panic(err) }")
	w("\treturn out")
	w("}")
	w("")
	w("func mustDecodeKindSchemas(raw string) map[Kind]string {")
	w("\tvar out map[Kind]string")
	w("\tif err := json.Unmarshal([]byte(raw), &out); err != nil { panic(err) }")
	w("\treturn out")
	w("}")
	w("")
	w("func mustDecodePrimitiveSchemas(raw string) map[string]primitiveSchema {")
	w("\tvar out map[string]primitiveSchema")
	w("\tif err := json.Unmarshal([]byte(raw), &out); err != nil { panic(err) }")
	w("\treturn out")
	w("}")
	w("")
	w("func mustDecodeStringMap(raw string) map[string]string {")
	w("\tvar out map[string]string")
	w("\tif err := json.Unmarshal([]byte(raw), &out); err != nil { panic(err) }")
	w("\treturn out")
	w("}")
	w("")

	w("// ValidationError 是稳定的结构化校验错误;Path 指向首个失败位置。")
	w("type ValidationError struct {")
	w("\tPath string")
	w("\tRule string")
	w("\tMessage string")
	w("}")
	w("")
	w("func (e *ValidationError) Error() string {")
	w("\treturn fmt.Sprintf(\"%s: %s (%s)\", e.Path, e.Message, e.Rule)")
	w("}")
	w("")
	w("func validationError(path, rule, format string, args ...any) error {")
	w("\treturn &ValidationError{Path: path, Rule: rule, Message: fmt.Sprintf(format, args...)}")
	w("}")
	w("")
	w("// ValidateFrameSize 按完整 UTF-8 帧字节数执行硬上限;maxBytes<=0 时使用契约默认值。")
	w("func ValidateFrameSize(frame []byte, maxBytes int64) error {")
	w("\tif maxBytes <= 0 { maxBytes = DefaultMaxMsgBytes }")
	w("\tif int64(len(frame)) > maxBytes {")
	w("\t\treturn validationError(\"$\", \"maxBytes\", \"帧为 %d 字节,上限 %d\", len(frame), maxBytes)")
	w("\t}")
	w("\treturn nil")
	w("}")
	w("")
	w("// ValidateKindBody 校验已知字段并忽略未知加法字段;cmd/event 还会下钻 args/data schema。")
	w("func ValidateKindBody(kind Kind, raw json.RawMessage) error {")
	w("\tname, ok := bodySchemas[kind]")
	w("\tif !ok { return validationError(\"$\", \"kind\", \"未知 kind %q\", kind) }")
	w("\tif err := validateRaw(name, raw); err != nil { return err }")
	w("\tswitch kind {")
	w("\tcase KindCmd:")
	w("\t\tvar body CmdBody")
	w("\t\tif err := json.Unmarshal(raw, &body); err != nil { return validationError(\"$\", \"json\", \"%v\", err) }")
	w("\t\tif err := validateCommandSemantics(body); err != nil { return err }")
	w("\t\treturn rebaseValidationError(ValidatePrimitiveArgs(body.Name, body.Ver, body.Args), \"$.args\")")
	w("\tcase KindEvent:")
	w("\t\tvar body EventBody")
	w("\t\tif err := json.Unmarshal(raw, &body); err != nil { return validationError(\"$\", \"json\", \"%v\", err) }")
	w("\t\treturn rebaseValidationError(ValidateEventData(string(body.Name), body.Data), \"$.data\")")
	w("\tdefault:")
	w("\t\treturn nil")
	w("\t}")
	w("}")
	w("")
	w("func ValidatePrimitiveArgs(name string, ver int, raw json.RawMessage) error {")
	w("\ts, ok := primitiveSchemas[name]")
	w("\tif !ok { return validationError(\"$\", \"primitive\", \"原语 %q 无激活 schema\", name) }")
	w("\tif ver != s.Ver { return validationError(\"$\", \"version\", \"原语 %q 需要版本 %d,收到 %d\", name, s.Ver, ver) }")
	w("\treturn validateRaw(s.Args, raw)")
	w("}")
	w("")
	w("func ValidatePrimitiveData(name string, ver int, raw json.RawMessage) error {")
	w("\ts, ok := primitiveSchemas[name]")
	w("\tif !ok { return validationError(\"$\", \"primitive\", \"原语 %q 无激活 schema\", name) }")
	w("\tif ver != s.Ver { return validationError(\"$\", \"version\", \"原语 %q 需要版本 %d,收到 %d\", name, s.Ver, ver) }")
	w("\treturn validateRaw(s.Data, raw)")
	w("}")
	w("")
	w("func ValidateEventData(name string, raw json.RawMessage) error {")
	w("\ts, ok := eventSchemas[name]")
	w("\tif !ok { return validationError(\"$\", \"event\", \"未知事件 %q\", name) }")
	w("\treturn validateRaw(s, raw)")
	w("}")
	w("")
	w("func validateCommandSemantics(body CmdBody) error {")
	w("\tmeta, ok := Primitives[body.Name]")
	w("\tif !ok || meta.Ver == 0 { return validationError(\"$.name\", \"primitive\", \"未知或未激活原语 %q\", body.Name) }")
	w("\tif meta.Ver != body.Ver { return validationError(\"$.ver\", \"version\", \"原语 %q 需要版本 %d,收到 %d\", body.Name, meta.Ver, body.Ver) }")
	w("\tisDebug := strings.HasPrefix(body.Name, \"debug.\")")
	w("\tif !isDebug && body.Context == nil && !meta.ContextOptionalBeforeBinding { return validationError(\"$.context\", \"required\", \"除绑定前探测外,非 debug 原语必须携带 context\") }")
	w("\tif meta.Class == ClassEffectful {")
	w("\t\tif body.IdemKey == \"\" { return validationError(\"$.idemKey\", \"required\", \"effectful 原语必须携带 idemKey\") }")
	w("\t} else if body.IdemKey != \"\" {")
	w("\t\treturn validationError(\"$.idemKey\", \"forbidden\", \"readonly/intrusive 原语禁止携带 idemKey\")")
	w("\t}")
	w("\tif meta.LeaseMs > 0 && body.LeaseMs == 0 { return validationError(\"$.leaseMs\", \"required\", \"原语 %q 必须携带租约\", body.Name) }")
	w("\tif meta.LeaseMs == 0 && body.LeaseMs != 0 { return validationError(\"$.leaseMs\", \"forbidden\", \"原语 %q 未启用租约\", body.Name) }")
	w("\tif !isDebug && meta.Class != ClassReadonly {")
	w("\t\tif body.Context == nil || body.Context.ExpectedPrincipalFingerprint == \"\" {")
	w("\t\t\treturn validationError(\"$.context.expectedPrincipalFingerprint\", \"required\", \"intrusive/effectful 原语必须携带期望账号指纹\")")
	w("\t\t}")
	w("\t}")
	w("\treturn nil")
	w("}")
	w("")
	w("func validateRaw(schemaName string, raw json.RawMessage) error {")
	w("\tif len(raw) == 0 { return validationError(\"$\", \"json\", \"空 JSON\") }")
	w("\tdec := json.NewDecoder(bytes.NewReader(raw))")
	w("\tdec.UseNumber()")
	w("\tvar value any")
	w("\tif err := dec.Decode(&value); err != nil { return validationError(\"$\", \"json\", \"%v\", err) }")
	w("\tvar extra any")
	w("\tif err := dec.Decode(&extra); err != io.EOF { return validationError(\"$\", \"json\", \"JSON 后存在额外内容\") }")
	w("\tspec, ok := schemaTypes[schemaName]")
	w("\tif !ok { return validationError(\"$\", \"schema\", \"未知 schema %q\", schemaName) }")
	w("\treturn validateSpec(spec, value, \"$\")")
	w("}")
	w("")
	w("func validateSpec(spec schemaSpec, value any, path string) error {")
	w("\tif value == nil {")
	w("\t\tif spec.Nullable || spec.Type == \"any\" { return nil }")
	w("\t\treturn validationError(path, \"nullable\", \"不允许 null\")")
	w("\t}")
	w("\tif spec.Ref != \"\" {")
	w("\t\ttarget, ok := schemaTypes[spec.Ref]")
	w("\t\tif !ok { return validationError(path, \"schema\", \"未知引用 %q\", spec.Ref) }")
	w("\t\treturn validateSpec(target, value, path)")
	w("\t}")
	w("\tif spec.MaxJSONBytes > 0 {")
	w("\t\tencoded, err := json.Marshal(value)")
	w("\t\tif err != nil { return validationError(path, \"json\", \"%v\", err) }")
	w("\t\tif len(encoded) > spec.MaxJSONBytes { return validationError(path, \"maxJsonBytes\", \"JSON 为 %d 字节,上限 %d\", len(encoded), spec.MaxJSONBytes) }")
	w("\t}")
	w("\tswitch spec.Type {")
	w("\tcase \"any\":")
	w("\t\treturn nil")
	w("\tcase \"boolean\":")
	w("\t\tv, ok := value.(bool)")
	w("\t\tif !ok { return validationError(path, \"type\", \"需要 boolean\") }")
	w("\t\tif spec.Const != nil && v != *spec.Const { return validationError(path, \"const\", \"必须为 %t\", *spec.Const) }")
	w("\t\treturn nil")
	w("\tcase \"string\":")
	w("\t\tv, ok := value.(string)")
	w("\t\tif !ok { return validationError(path, \"type\", \"需要 string\") }")
	w("\t\trunes := utf8.RuneCountInString(v)")
	w("\t\tif runes < spec.MinLength { return validationError(path, \"minLength\", \"长度 %d 小于下限 %d\", runes, spec.MinLength) }")
	w("\t\tif spec.MaxLength > 0 && runes > spec.MaxLength { return validationError(path, \"maxLength\", \"长度 %d 超过上限 %d\", runes, spec.MaxLength) }")
	w("\t\tif spec.MaxBytes > 0 && len([]byte(v)) > spec.MaxBytes { return validationError(path, \"maxBytes\", \"UTF-8 为 %d 字节,上限 %d\", len([]byte(v)), spec.MaxBytes) }")
	w("\t\tif spec.EnumRef != \"\" && !stringAllowed(schemaEnums[spec.EnumRef], v) { return validationError(path, \"enum\", \"%q 不在 %s 内\", v, spec.EnumRef) }")
	w("\t\treturn nil")
	w("\tcase \"int\", \"int64\":")
	w("\t\tn, ok := value.(json.Number)")
	w("\t\tif !ok { return validationError(path, \"type\", \"需要整数\") }")
	w("\t\tv, err := n.Int64()")
	w("\t\tif err != nil { return validationError(path, \"type\", \"需要 int64 范围内整数\") }")
	w("\t\tif v < -9007199254740991 || v > 9007199254740991 { return validationError(path, \"safeInteger\", \"整数超出跨 Go/TS 安全范围\") }")
	w("\t\tif spec.Minimum != nil && v < *spec.Minimum { return validationError(path, \"minimum\", \"%d 小于下限 %d\", v, *spec.Minimum) }")
	w("\t\tif spec.Maximum != nil && v > *spec.Maximum { return validationError(path, \"maximum\", \"%d 超过上限 %d\", v, *spec.Maximum) }")
	w("\t\treturn nil")
	w("\tcase \"array\":")
	w("\t\tv, ok := value.([]any)")
	w("\t\tif !ok { return validationError(path, \"type\", \"需要 array\") }")
	w("\t\tif len(v) < spec.MinItems { return validationError(path, \"minItems\", \"元素数 %d 小于下限 %d\", len(v), spec.MinItems) }")
	w("\t\tif spec.MaxItems > 0 && len(v) > spec.MaxItems { return validationError(path, \"maxItems\", \"元素数 %d 超过上限 %d\", len(v), spec.MaxItems) }")
	w("\t\tif spec.Items == nil { return validationError(path, \"schema\", \"array 缺 items\") }")
	w("\t\tfor i, item := range v { if err := validateSpec(*spec.Items, item, fmt.Sprintf(\"%s[%d]\", path, i)); err != nil { return err } }")
	w("\t\treturn nil")
	w("\tcase \"object\":")
	w("\t\tv, ok := value.(map[string]any)")
	w("\t\tif !ok { return validationError(path, \"type\", \"需要 object\") }")
	w("\t\tfor name, field := range spec.Fields {")
	w("\t\t\tfv, exists := v[name]")
	w("\t\t\tif !exists { if !field.Optional { return validationError(path+\".\"+name, \"required\", \"缺少必填字段\") }; continue }")
	w("\t\t\tif err := validateSpec(field, fv, path+\".\"+name); err != nil { return err }")
	w("\t\t}")
	w("\t\t// 未知字段故意不遍历:主版本内 must-ignore。")
	w("\t\tfor _, rule := range spec.Rules { if err := validateSchemaRule(rule, v, path); err != nil { return err } }")
	w("\t\treturn nil")
	w("\tdefault:")
	w("\t\treturn validationError(path, \"schema\", \"未知 schema type %q\", spec.Type)")
	w("\t}")
	w("}")
	w("")
	w("func validateSchemaRule(rule schemaRule, values map[string]any, path string) error {")
	w("\tactual, ok := values[rule.WhenField]")
	w("\tif !ok || !anyAllowed(rule.Equals, actual) { return nil }")
	w("\tnonNull := func(field string) bool { value, exists := values[field]; return exists && value != nil }")
	w("\tswitch rule.Kind {")
	w("\tcase \"requiredWhen\":")
	w("\t\tif !nonNull(rule.Field) { return validationError(path+\".\"+rule.Field, \"requiredWhen\", \"条件成立时字段必填且非 null\") }")
	w("\tcase \"forbiddenWhen\":")
	w("\t\tif nonNull(rule.Field) { return validationError(path+\".\"+rule.Field, \"forbiddenWhen\", \"条件成立时字段必须缺省或 null\") }")
	w("\tcase \"exactlyOneWhen\":")
	w("\t\tcount := 0")
	w("\t\tfor _, field := range rule.Fields { if nonNull(field) { count++ } }")
	w("\t\tif count != 1 { return validationError(path, \"exactlyOneWhen\", \"条件成立时 %v 必须恰有一个非 null\", rule.Fields) }")
	w("\tcase \"atLeastOneTrueWhen\":")
	w("\t\tmatched := false")
	w("\t\tfor _, field := range rule.Fields { if value, ok := values[field].(bool); ok && value { matched = true } }")
	w("\t\tif !matched { return validationError(path, \"atLeastOneTrueWhen\", \"条件成立时 %v 至少一个必须为 true\", rule.Fields) }")
	w("\tcase \"allFalseWhen\":")
	w("\t\tfor _, field := range rule.Fields { if value, ok := values[field].(bool); !ok || value { return validationError(path+\".\"+field, \"allFalseWhen\", \"条件成立时必须为 false\") } }")
	w("\tdefault:")
	w("\t\treturn validationError(path, \"schema\", \"未知规则 %q\", rule.Kind)")
	w("\t}")
	w("\treturn nil")
	w("}")
	w("")
	w("func stringAllowed(values []string, value string) bool {")
	w("\tfor _, candidate := range values { if candidate == value { return true } }")
	w("\treturn false")
	w("}")
	w("")
	w("func anyAllowed(values []any, value any) bool {")
	w("\tfor _, candidate := range values { if reflect.DeepEqual(candidate, value) { return true } }")
	w("\treturn false")
	w("}")
	w("")
	w("func rebaseValidationError(err error, base string) error {")
	w("\tif err == nil { return nil }")
	w("\tvar validation *ValidationError")
	w("\tif !errorsAsValidation(err, &validation) { return err }")
	w("\tpath := base")
	w("\tif validation.Path != \"$\" { path += strings.TrimPrefix(validation.Path, \"$\") }")
	w("\treturn &ValidationError{Path: path, Rule: validation.Rule, Message: validation.Message}")
	w("}")
	w("")
	w("func errorsAsValidation(err error, target **ValidationError) bool {")
	w("\tvalidation, ok := err.(*ValidationError)")
	w("\tif ok { *target = validation }")
	w("\treturn ok")
	w("}")

	return []byte(b.String())
}

// ---------- TS 产物 ----------

func genTS(c map[string]any, hash string) []byte {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	w("// Code generated by contract/codegen from %s. DO NOT EDIT.", *contractPath)
	w("")
	w("export const PROTO_VERSION = %d;", intval(c["proto"]))
	w("export const CONTRACT_HASH = %q;", hash)
	w("export const UNKNOWN_FIELD_POLICY = %q as const;", str(obj(c, "compatibility")["unknownFields"]))
	w("export const CONTRACT_HASH_POLICY = %q as const;", str(obj(c, "compatibility")["contractHash"]))
	w("export const JSON_INTEGER_POLICY = %q as const;", str(obj(c, "compatibility")["jsonInteger"]))
	w("")

	tr := obj(c, "transport")
	w("export const TRANSPORT = {")
	w("  scheme: %q,", str(tr["scheme"]))
	w("  host: %q,", str(tr["host"]))
	w("  portDefault: %d,", intval(tr["portDefault"]))
	w("  path: %q,", str(tr["path"]))
	w("  originPrefix: %q,", str(tr["originPrefix"]))
	w("  chromeMinVersion: %d,", intval(tr["chromeMin"]))
	w("} as const;")
	w("")

	emitTSEnum := func(typ string, vals []string) {
		w("export const %s = {", typ)
		for _, v := range vals {
			w("  %s: %q,", pascal(v), v)
		}
		w("} as const;")
		w("export type %s = (typeof %s)[keyof typeof %s];", typ, typ, typ)
		w("")
	}

	enums := obj(c, "enums")
	for _, name := range sortedKeys(enums) {
		emitTSEnum(pascal(name), strSlice(enums[name]))
	}

	kinds := obj(c, "kinds")
	emitTSEnum("Kind", sortedKeys(kinds))

	w("export interface Envelope {")
	w("  proto: number;")
	w("  kind: Kind;")
	w("  msgId: string;")
	w("  /** hello/welcome/bye 为 null */")
	w("  session: string | null;")
	w("  ts: number;")
	w("  attempt: number;")
	w("  body: unknown;")
	w("}")
	w("")

	w("export interface KindMetaEntry {")
	w("  dir: \"H2B\" | \"B2H\" | \"both\";")
	w("  /** 应答该消息的 kind;null=无应答 */")
	w("  ackBy: string | null;")
	w("  batch: Batch;")
	w("  /** true=可丢(无重发、无补投) */")
	w("  lossy: boolean;")
	w("}")
	w("export const KIND_META: Record<Kind, KindMetaEntry> = {")
	for _, k := range sortedKeys(kinds) {
		m := obj(kinds, k)
		ackBy := "null"
		if s := str(m["ackBy"]); s != "" {
			ackBy = strconv.Quote(s)
		}
		w("  %s: { dir: %q, ackBy: %s, batch: %q, lossy: %t },", k, str(m["dir"]), ackBy, str(m["batch"]), boolval(m["lossy"]))
	}
	w("};")
	w("")

	features := obj(c, "features")
	emitTSEnum("Feature", sortedKeys(features))
	w("export const FEATURE_META: Record<Feature, { batch: Batch }> = {")
	for _, name := range sortedKeys(features) {
		w("  %q: { batch: %q },", name, str(obj(features, name)["batch"]))
	}
	w("};")
	w("")

	classes := obj(c, "classes")
	classKeys := objectKeys(classes)
	emitTSEnum("CmdClass", classKeys)
	w("export interface ClassMetaEntry {")
	w("  /** 是否进串行域(账号域;debug.* 用每手 debug 域) */")
	w("  serialDomain: boolean;")
	w("  idemKey: \"forbidden\" | \"required\";")
	w("  evidence: string;")
	w("}")
	w("export const CLASS_META: Record<CmdClass, ClassMetaEntry> = {")
	for _, k := range classKeys {
		m := obj(classes, k)
		w("  %s: { serialDomain: %t, idemKey: %q, evidence: %q },", k, boolval(m["serialDomain"]), str(m["idemKey"]), str(m["evidence"]))
	}
	w("};")
	w("")

	emitTSEnum("ByeCode", strSlice(c["byeCodes"]))

	errCodes := obj(c, "errorCodes")
	emitTSEnum("ErrorCode", sortedKeys(errCodes))
	w("export interface ErrorCodeMetaEntry {")
	w("  /** 契约默认建议,可为条件式描述;裁决权在脑的矩阵 */")
	w("  retryableDefault: string;")
	w("  sideEffect: readonly SideEffect[];")
	w("  batch: Batch;")
	w("  phase: ErrorPhase;")
	w("}")
	w("export const ERROR_CODE_META: Record<ErrorCode, ErrorCodeMetaEntry> = {")
	for _, k := range sortedKeys(errCodes) {
		m := obj(errCodes, k)
		ses := strSlice(m["sideEffect"])
		quoted := make([]string, len(ses))
		for i, se := range ses {
			quoted[i] = strconv.Quote(se)
		}
		w("  %s: { retryableDefault: %q, sideEffect: [%s], batch: %q, phase: %q },", k, str(m["retryable"]), strings.Join(quoted, ", "), str(m["batch"]), str(m["phase"]))
	}
	w("};")
	w("")
	w("export const ACK_REJECTABLE_ERROR_CODES: ReadonlySet<ErrorCode> = new Set([")
	for _, code := range strSlice(obj(c, "commandAcceptance")["ackRejectedErrorCodes"]) {
		w("  %q,", code)
	}
	w("]);")
	w("")
	acceptanceJSON, err := json.MarshalIndent(sortValue(obj(c, "commandAcceptance")), "", "  ")
	must(err, "序列化 commandAcceptance")
	w("export const COMMAND_ACCEPTANCE = %s as const;", string(acceptanceJSON))
	w("")

	prims := obj(c, "primitives")
	emitTSEnum("Primitive", sortedKeys(prims))
	w("export interface PrimitiveMetaEntry {")
	w("  /** 0=占位,未定稿 */")
	w("  ver: number;")
	w("  class: CmdClass;")
	w("  batch: Batch;")
	w("  /** intrusive 专用声明;null=未声明 */")
	w("  platformSideEffect: string | null;")
	w("  /** null=用类默认 */")
	w("  execBudgetMs: number | null;")
	w("  /** null=派发时计算 */")
	w("  deadlineMs: number | null;")
	w("  /** null=不启用租约;非空须协商 lease/1 */")
	w("  leaseMs: number | null;")
	w("  argsSchema: string | null;")
	w("  dataSchema: string | null;")
	w("  /** 仅首次真人绑定探测可省略 context */")
	w("  contextOptionalBeforeBinding: boolean;")
	w("}")
	w("export const PRIMITIVE_META: Record<Primitive, PrimitiveMetaEntry> = {")
	for _, k := range sortedKeys(prims) {
		m := obj(prims, k)
		w("  %q: { ver: %d, class: %q, batch: %q, platformSideEffect: %s, execBudgetMs: %s, deadlineMs: %s, leaseMs: %s, argsSchema: %s, dataSchema: %s, contextOptionalBeforeBinding: %t },",
			k, intval(m["ver"]), str(m["class"]), str(m["batch"]),
			tsNullableStr(m["platformSideEffect"]), tsNullableInt(m["execBudgetMs"]), tsNullableInt(m["deadlineMs"]), tsNullableInt(m["leaseMs"]), tsNullableStr(m["argsSchema"]), tsNullableStr(m["dataSchema"]), boolval(m["contextOptionalBeforeBinding"]))
	}
	w("};")
	w("")

	events := obj(c, "events")
	emitTSEnum("EventName", sortedKeys(events))
	w("export const EVENT_META: Record<EventName, { batch: Batch }> = {")
	for _, k := range sortedKeys(events) {
		w("  %s: { batch: %q },", k, str(obj(events, k)["batch"]))
	}
	w("};")
	w("")

	// 默认参数:原样嵌入(键排序的 JSON)
	defJSON, err := json.MarshalIndent(sortValue(obj(c, "defaults")), "", "  ")
	must(err, "序列化 defaults")
	w("/** 默认参数(welcome 可下发覆盖) */")
	w("export const DEFAULTS = %s as const;", string(defJSON))
	w("")

	bodies := obj(c, "bodies")
	types := obj(obj(c, "schemas"), "types")
	w("/** 各 kind 的线上字段名清单($ 注释键已剔除);用于 body 结构一致性测试 */")
	w("export const KIND_BODY_FIELDS: Record<Kind, readonly string[]> = {")
	for _, k := range sortedKeys(bodies) {
		fields := schemaObjectFields(types, str(obj(bodies, k)["schema"]))
		quoted := make([]string, len(fields))
		for i, f := range fields {
			quoted[i] = strconv.Quote(f)
		}
		w("  %s: [%s],", k, strings.Join(quoted, ", "))
	}
	w("};")
	w("")
	b.Write(genTSSchemas(c))

	return []byte(b.String())
}

func genTSSchemas(c map[string]any) []byte {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	types := obj(obj(c, "schemas"), "types")

	w("/** 线上 body/args/data/event 类型:全部由 schemas.types 生成。 */")
	for _, name := range sortedKeys(types) {
		node := obj(types, name)
		w("export interface %s {", name)
		for _, fieldName := range sortedKeys(obj(node, "fields")) {
			field := obj(obj(node, "fields"), fieldName)
			optional := ""
			if boolval(field["optional"]) {
				optional = "?"
			}
			w("  %s%s: %s;", fieldName, optional, tsSchemaType(field))
		}
		w("}")
		w("")
	}

	w("export interface BodyByKind {")
	for _, kind := range sortedKeys(obj(c, "bodies")) {
		w("  %q: %s;", kind, str(obj(obj(c, "bodies"), kind)["schema"]))
	}
	w("}")
	w("export type AnyBody = BodyByKind[keyof BodyByKind];")
	w("")
	w("export interface PrimitiveArgsByName {")
	for _, name := range sortedKeys(obj(c, "primitives")) {
		p := obj(obj(c, "primitives"), name)
		if intval(p["ver"]) > 0 {
			w("  %q: %s;", name, str(p["argsSchema"]))
		}
	}
	w("}")
	w("export interface PrimitiveDataByName {")
	for _, name := range sortedKeys(obj(c, "primitives")) {
		p := obj(obj(c, "primitives"), name)
		if intval(p["ver"]) > 0 {
			w("  %q: %s;", name, str(p["dataSchema"]))
		}
	}
	w("}")
	w("export type TypedCmdBody<N extends keyof PrimitiveArgsByName = keyof PrimitiveArgsByName> =")
	w("  Omit<CmdBody, \"name\" | \"args\"> & { name: N; args: PrimitiveArgsByName[N] };")
	w("")
	w("export interface EventDataByName {")
	for _, name := range sortedKeys(obj(c, "events")) {
		w("  %q: %s;", name, str(obj(obj(c, "events"), name)["dataSchema"]))
	}
	w("}")
	w("export type TypedEventBody = {")
	w("  [N in keyof EventDataByName]: Omit<EventBody, \"name\" | \"data\"> & { name: N; data: EventDataByName[N] };")
	w("}[keyof EventDataByName];")
	w("")

	typesJSON, err := json.MarshalIndent(types, "", "  ")
	must(err, "序列化 TS schemas.types")
	enumsJSON, err := json.MarshalIndent(schemaEnumValues(c), "", "  ")
	must(err, "序列化 TS schema enums")
	bodyJSON, err := json.MarshalIndent(bodySchemaMap(c), "", "  ")
	must(err, "序列化 TS body schemas")
	primitiveJSON, err := json.MarshalIndent(primitiveSchemaMap(c), "", "  ")
	must(err, "序列化 TS primitive schemas")
	eventJSON, err := json.MarshalIndent(eventSchemaMap(c), "", "  ")
	must(err, "序列化 TS event schemas")

	w("interface SchemaRule {")
	w("  kind: \"requiredWhen\" | \"forbiddenWhen\" | \"exactlyOneWhen\" | \"atLeastOneTrueWhen\" | \"allFalseWhen\";")
	w("  field?: string;")
	w("  fields?: readonly string[];")
	w("  whenField: string;")
	w("  equals: readonly unknown[];")
	w("}")
	w("interface SchemaSpec {")
	w("  type?: \"any\" | \"boolean\" | \"string\" | \"int\" | \"int64\" | \"array\" | \"object\";")
	w("  ref?: string;")
	w("  enumRef?: string;")
	w("  optional?: boolean;")
	w("  nullable?: boolean;")
	w("  raw?: boolean;")
	w("  fields?: Record<string, SchemaSpec>;")
	w("  items?: SchemaSpec;")
	w("  minimum?: number;")
	w("  maximum?: number;")
	w("  minItems?: number;")
	w("  maxItems?: number;")
	w("  minLength?: number;")
	w("  maxLength?: number;")
	w("  maxBytes?: number;")
	w("  maxJsonBytes?: number;")
	w("  const?: boolean;")
	w("  default?: unknown;")
	w("  rules?: readonly SchemaRule[];")
	w("}")
	w("interface PrimitiveSchemaSpec { ver: number; args: string; data: string }")
	w("")
	w("const SCHEMA_TYPES: Record<string, SchemaSpec> = %s;", string(typesJSON))
	w("const SCHEMA_ENUMS: Record<string, readonly string[]> = %s;", string(enumsJSON))
	w("const BODY_SCHEMAS: Record<Kind, string> = %s;", string(bodyJSON))
	w("const PRIMITIVE_SCHEMAS: Record<string, PrimitiveSchemaSpec> = %s;", string(primitiveJSON))
	w("const EVENT_SCHEMAS: Record<EventName, string> = %s;", string(eventJSON))
	w("")

	b.WriteString(`export interface ValidationIssue {
  path: string;
  rule: string;
  message: string;
}

export class SchemaValidationError extends Error {
  readonly issues: readonly ValidationIssue[];
  constructor(issues: readonly ValidationIssue[]) {
    super(issues.map((issue) => issue.path + ": " + issue.message + " (" + issue.rule + ")").join("; "));
    this.name = "SchemaValidationError";
    this.issues = issues;
  }
}

const UTF8_ENCODER = new TextEncoder();

function utf8Bytes(value: string): number {
  return UTF8_ENCODER.encode(value).byteLength;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function pushIssue(issues: ValidationIssue[], path: string, rule: string, message: string): void {
  issues.push({ path, rule, message });
}

function validateByName(schemaName: string, value: unknown): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const schema = SCHEMA_TYPES[schemaName];
  if (schema === undefined) {
    pushIssue(issues, "$", "schema", "未知 schema " + schemaName);
    return issues;
  }
  validateSpec(schema, value, "$", issues);
  return issues;
}

function validateSpec(spec: SchemaSpec, value: unknown, path: string, issues: ValidationIssue[]): void {
  if (value === undefined) {
    pushIssue(issues, path, "type", "JSON 不允许 undefined");
    return;
  }
  if (value === null) {
    if (spec.nullable === true || spec.type === "any") return;
    pushIssue(issues, path, "nullable", "不允许 null");
    return;
  }
  if (spec.ref !== undefined) {
    const target = SCHEMA_TYPES[spec.ref];
    if (target === undefined) pushIssue(issues, path, "schema", "未知引用 " + spec.ref);
    else validateSpec(target, value, path, issues);
    return;
  }
  if (spec.maxJsonBytes !== undefined) {
    const encoded = JSON.stringify(value);
    if (encoded === undefined) {
      pushIssue(issues, path, "json", "值不能编码为 JSON");
      return;
    }
    const size = utf8Bytes(encoded);
    if (size > spec.maxJsonBytes) pushIssue(issues, path, "maxJsonBytes", "JSON 为 " + size + " 字节,上限 " + spec.maxJsonBytes);
  }
  switch (spec.type) {
    case "any":
      return;
    case "boolean":
      if (typeof value !== "boolean") pushIssue(issues, path, "type", "需要 boolean");
      else if (spec.const !== undefined && value !== spec.const) pushIssue(issues, path, "const", "必须为 " + spec.const);
      return;
    case "string": {
      if (typeof value !== "string") {
        pushIssue(issues, path, "type", "需要 string");
        return;
      }
      const length = Array.from(value).length;
      if (length < (spec.minLength ?? 0)) pushIssue(issues, path, "minLength", "长度 " + length + " 小于下限 " + spec.minLength);
      if (spec.maxLength !== undefined && length > spec.maxLength) pushIssue(issues, path, "maxLength", "长度 " + length + " 超过上限 " + spec.maxLength);
      const size = utf8Bytes(value);
      if (spec.maxBytes !== undefined && size > spec.maxBytes) pushIssue(issues, path, "maxBytes", "UTF-8 为 " + size + " 字节,上限 " + spec.maxBytes);
      if (spec.enumRef !== undefined && !(SCHEMA_ENUMS[spec.enumRef] ?? []).includes(value)) pushIssue(issues, path, "enum", JSON.stringify(value) + " 不在 " + spec.enumRef + " 内");
      return;
    }
    case "int":
    case "int64":
      if (typeof value !== "number" || !Number.isInteger(value)) {
        pushIssue(issues, path, "type", "需要整数");
        return;
      }
      if (!Number.isSafeInteger(value)) {
        pushIssue(issues, path, "safeInteger", "整数超出跨 Go/TS 安全范围");
        return;
      }
      if (spec.minimum !== undefined && value < spec.minimum) pushIssue(issues, path, "minimum", value + " 小于下限 " + spec.minimum);
      if (spec.maximum !== undefined && value > spec.maximum) pushIssue(issues, path, "maximum", value + " 超过上限 " + spec.maximum);
      return;
    case "array":
      if (!Array.isArray(value)) {
        pushIssue(issues, path, "type", "需要 array");
        return;
      }
      if (value.length < (spec.minItems ?? 0)) pushIssue(issues, path, "minItems", "元素数 " + value.length + " 小于下限 " + spec.minItems);
      if (spec.maxItems !== undefined && value.length > spec.maxItems) pushIssue(issues, path, "maxItems", "元素数 " + value.length + " 超过上限 " + spec.maxItems);
      if (spec.items === undefined) pushIssue(issues, path, "schema", "array 缺 items");
      else value.forEach((item, index) => validateSpec(spec.items as SchemaSpec, item, path + "[" + index + "]", issues));
      return;
    case "object":
      if (!isRecord(value)) {
        pushIssue(issues, path, "type", "需要 object");
        return;
      }
      for (const [name, field] of Object.entries(spec.fields ?? {})) {
        if (!(name in value)) {
          if (field.optional !== true) pushIssue(issues, path + "." + name, "required", "缺少必填字段");
          continue;
        }
        validateSpec(field, value[name], path + "." + name, issues);
      }
      // 未知字段故意不遍历:主版本内 must-ignore。
      for (const rule of spec.rules ?? []) validateSchemaRule(rule, value, path, issues);
      return;
    default:
      pushIssue(issues, path, "schema", "未知 schema type " + String(spec.type));
  }
}

function validateSchemaRule(rule: SchemaRule, values: Record<string, unknown>, path: string, issues: ValidationIssue[]): void {
  const actual = values[rule.whenField];
  if (!rule.equals.some((candidate) => Object.is(candidate, actual))) return;
  const nonNull = (field: string): boolean => field in values && values[field] !== null;
  switch (rule.kind) {
    case "requiredWhen":
      if (rule.field !== undefined && !nonNull(rule.field)) pushIssue(issues, path + "." + rule.field, "requiredWhen", "条件成立时字段必填且非 null");
      return;
    case "forbiddenWhen":
      if (rule.field !== undefined && nonNull(rule.field)) pushIssue(issues, path + "." + rule.field, "forbiddenWhen", "条件成立时字段必须缺省或 null");
      return;
    case "exactlyOneWhen": {
      const count = (rule.fields ?? []).filter(nonNull).length;
      if (count !== 1) pushIssue(issues, path, "exactlyOneWhen", "条件成立时 " + JSON.stringify(rule.fields) + " 必须恰有一个非 null");
      return;
    }
    case "atLeastOneTrueWhen":
      if (!(rule.fields ?? []).some((field) => values[field] === true)) pushIssue(issues, path, "atLeastOneTrueWhen", "条件成立时 " + JSON.stringify(rule.fields) + " 至少一个必须为 true");
      return;
    case "allFalseWhen":
      for (const field of rule.fields ?? []) {
        if (values[field] !== false) pushIssue(issues, path + "." + field, "allFalseWhen", "条件成立时必须为 false");
      }
      return;
  }
}

function rebaseIssues(issues: readonly ValidationIssue[], base: string): ValidationIssue[] {
  return issues.map((issue) => ({ ...issue, path: issue.path === "$" ? base : base + issue.path.slice(1) }));
}

function validateCommandSemantics(body: Record<string, unknown>): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const name = typeof body.name === "string" ? body.name : "";
  if (!(name in PRIMITIVE_META)) {
    pushIssue(issues, "$.name", "primitive", "未知原语 " + name);
    return issues;
  }
  const meta = PRIMITIVE_META[name as Primitive];
  if (meta.ver === 0) pushIssue(issues, "$.name", "primitive", "原语尚未激活 " + name);
  if (body.ver !== meta.ver) pushIssue(issues, "$.ver", "version", "原语 " + name + " 需要版本 " + meta.ver);
  const isDebug = name.startsWith("debug.");
  const context = isRecord(body.context) ? body.context : undefined;
  if (!isDebug && context === undefined && !meta.contextOptionalBeforeBinding) pushIssue(issues, "$.context", "required", "除绑定前探测外,非 debug 原语必须携带 context");
  if (meta.class === "effectful") {
    if (typeof body.idemKey !== "string" || body.idemKey.length === 0) pushIssue(issues, "$.idemKey", "required", "effectful 原语必须携带 idemKey");
  } else if (body.idemKey !== undefined && body.idemKey !== "") {
    pushIssue(issues, "$.idemKey", "forbidden", "readonly/intrusive 原语禁止携带 idemKey");
  }
  if (meta.leaseMs !== null && body.leaseMs === undefined) pushIssue(issues, "$.leaseMs", "required", "该原语必须携带租约");
  if (meta.leaseMs === null && body.leaseMs !== undefined) pushIssue(issues, "$.leaseMs", "forbidden", "该原语未启用租约");
  if (!isDebug && meta.class !== "readonly") {
    if (context === undefined || typeof context.expectedPrincipalFingerprint !== "string" || context.expectedPrincipalFingerprint.length === 0) {
      pushIssue(issues, "$.context.expectedPrincipalFingerprint", "required", "intrusive/effectful 原语必须携带期望账号指纹");
    }
  }
  return issues;
}

export function validateFrameSize(frame: string | Uint8Array, maxBytes: number = DEFAULTS.maxMsgBytes): ValidationIssue[] {
  const size = typeof frame === "string" ? utf8Bytes(frame) : frame.byteLength;
  return size > maxBytes ? [{ path: "$", rule: "maxBytes", message: "帧为 " + size + " 字节,上限 " + maxBytes }] : [];
}

export function validateKindBody<K extends Kind>(kind: K, value: unknown): ValidationIssue[] {
  const issues = validateByName(BODY_SCHEMAS[kind], value);
  if (issues.length > 0 || !isRecord(value)) return issues;
  if (kind === "cmd") {
    issues.push(...validateCommandSemantics(value));
    if (typeof value.name === "string" && typeof value.ver === "number") issues.push(...rebaseIssues(validatePrimitiveArgs(value.name, value.ver, value.args), "$.args"));
  } else if (kind === "event" && typeof value.name === "string") {
    issues.push(...rebaseIssues(validateEventData(value.name, value.data), "$.data"));
  }
  return issues;
}

export function validatePrimitiveArgs(name: string, ver: number, value: unknown): ValidationIssue[] {
  const schema = PRIMITIVE_SCHEMAS[name];
  if (schema === undefined) return [{ path: "$", rule: "primitive", message: "原语无激活 schema: " + name }];
  if (ver !== schema.ver) return [{ path: "$", rule: "version", message: "原语 " + name + " 需要版本 " + schema.ver + ",收到 " + ver }];
  return validateByName(schema.args, value);
}

export function validatePrimitiveData(name: string, ver: number, value: unknown): ValidationIssue[] {
  const schema = PRIMITIVE_SCHEMAS[name];
  if (schema === undefined) return [{ path: "$", rule: "primitive", message: "原语无激活 schema: " + name }];
  if (ver !== schema.ver) return [{ path: "$", rule: "version", message: "原语 " + name + " 需要版本 " + schema.ver + ",收到 " + ver }];
  return validateByName(schema.data, value);
}

export function validateEventData(name: string, value: unknown): ValidationIssue[] {
  const schemaName = EVENT_SCHEMAS[name as EventName];
  return schemaName === undefined ? [{ path: "$", rule: "event", message: "未知事件 " + name }] : validateByName(schemaName, value);
}

export function assertKindBody<K extends Kind>(kind: K, value: unknown): asserts value is BodyByKind[K] {
  const issues = validateKindBody(kind, value);
  if (issues.length > 0) throw new SchemaValidationError(issues);
}

export function assertPrimitiveArgs(name: string, ver: number, value: unknown): void {
  const issues = validatePrimitiveArgs(name, ver, value);
  if (issues.length > 0) throw new SchemaValidationError(issues);
}

export function assertPrimitiveData(name: string, ver: number, value: unknown): void {
  const issues = validatePrimitiveData(name, ver, value);
  if (issues.length > 0) throw new SchemaValidationError(issues);
}

export function assertEventData(name: string, value: unknown): void {
  const issues = validateEventData(name, value);
  if (issues.length > 0) throw new SchemaValidationError(issues);
}
`)

	return []byte(b.String())
}

func schemaEnumValues(c map[string]any) map[string][]string {
	out := map[string][]string{}
	for _, name := range sortedKeys(obj(c, "enums")) {
		out["enums."+name] = strSlice(obj(c, "enums")[name])
	}
	out["byeCodes"] = strSlice(c["byeCodes"])
	out["errorCodes"] = sortedKeys(obj(c, "errorCodes"))
	out["events"] = sortedKeys(obj(c, "events"))
	return out
}

func bodySchemaMap(c map[string]any) map[string]string {
	out := map[string]string{}
	for _, kind := range sortedKeys(obj(c, "bodies")) {
		out[kind] = str(obj(obj(c, "bodies"), kind)["schema"])
	}
	return out
}

func primitiveSchemaMap(c map[string]any) map[string]any {
	out := map[string]any{}
	for _, name := range sortedKeys(obj(c, "primitives")) {
		p := obj(obj(c, "primitives"), name)
		if intval(p["ver"]) == 0 {
			continue
		}
		out[name] = map[string]any{
			"ver":  intval(p["ver"]),
			"args": str(p["argsSchema"]),
			"data": str(p["dataSchema"]),
		}
	}
	return out
}

func eventSchemaMap(c map[string]any) map[string]string {
	out := map[string]string{}
	for _, name := range sortedKeys(obj(c, "events")) {
		out[name] = str(obj(obj(c, "events"), name)["dataSchema"])
	}
	return out
}

func schemaObjectFields(types map[string]any, name string) []string {
	node := obj(types, name)
	if str(node["type"]) != "object" {
		die("schema %s 不是 object", name)
	}
	return sortedKeys(obj(node, "fields"))
}

func goFieldName(name string) string {
	special := map[string]string{
		"handId": "HandID",
		"bootId": "BootID",
		"msgId":  "MsgID",
	}
	if value, ok := special[name]; ok {
		return value
	}
	return pascal(name)
}

func enumTypeName(ref string) string {
	switch ref {
	case "byeCodes":
		return "ByeCode"
	case "errorCodes":
		return "ErrorCode"
	case "events":
		return "EventName"
	default:
		if strings.HasPrefix(ref, "enums.") {
			return pascal(strings.TrimPrefix(ref, "enums."))
		}
		die("未知 enumRef %q", ref)
		return ""
	}
}

func goSchemaType(node map[string]any) string {
	var base string
	if ref := str(node["ref"]); ref != "" {
		base = ref
	} else {
		switch str(node["type"]) {
		case "any":
			base = "json.RawMessage"
		case "boolean":
			base = "bool"
		case "string":
			if enumRef := str(node["enumRef"]); enumRef != "" {
				base = enumTypeName(enumRef)
			} else {
				base = "string"
			}
		case "int":
			base = "int"
		case "int64":
			base = "int64"
		case "array":
			base = "[]" + goSchemaType(obj(node, "items"))
		case "object":
			if boolval(node["raw"]) {
				base = "json.RawMessage"
			} else {
				base = "map[string]any"
			}
		default:
			die("无法生成 Go 类型:schema type=%q", str(node["type"]))
		}
	}
	if base == "json.RawMessage" || strings.HasPrefix(base, "[]") {
		return base
	}
	if boolval(node["nullable"]) || (boolval(node["optional"]) && str(node["ref"]) != "") {
		return "*" + base
	}
	return base
}

func tsSchemaType(node map[string]any) string {
	var base string
	if ref := str(node["ref"]); ref != "" {
		base = ref
	} else {
		switch str(node["type"]) {
		case "any":
			base = "unknown"
		case "boolean":
			base = "boolean"
		case "string":
			if enumRef := str(node["enumRef"]); enumRef != "" {
				base = enumTypeName(enumRef)
			} else {
				base = "string"
			}
		case "int", "int64":
			base = "number"
		case "array":
			base = "Array<" + tsSchemaType(obj(node, "items")) + ">"
		case "object":
			base = "Record<string, unknown>"
		default:
			die("无法生成 TS 类型:schema type=%q", str(node["type"]))
		}
	}
	if boolval(node["nullable"]) {
		base += " | null"
	}
	return base
}

// ---------- 小工具 ----------

func must(err error, what string) {
	if err != nil {
		die("%s: %v", what, err)
	}
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "codegen: "+f+"\n", a...)
	os.Exit(1)
}

func obj(m map[string]any, key string) map[string]any {
	v, ok := m[key].(map[string]any)
	if !ok {
		die("契约缺少对象 %q", key)
	}
	return v
}

func str(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		die("期望字符串,得到 %T(%v)", v, v)
	}
	return s
}

func intval(v any) int64 {
	if v == nil {
		return 0
	}
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) {
		die("期望整数,得到 %T(%v)", v, v)
	}
	return int64(f)
}

func boolval(v any) bool {
	b, _ := v.(bool)
	return b
}

func strSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		die("期望数组,得到 %T", v)
	}
	out := make([]string, len(arr))
	for i, x := range arr {
		out[i] = str(x)
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// objectKeys:值为对象的键(排序),用于跳过同级散文字段(litmus 等)。
func objectKeys(m map[string]any) []string {
	var keys []string
	for k, v := range m {
		if _, ok := v.(map[string]any); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// wireFields:剔除 $ 前缀注释键后的字段名(排序)。
func wireFields(m map[string]any) []string {
	var keys []string
	for k := range m {
		if !strings.HasPrefix(k, "$") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// pascal:proto_malformed / debug.ping / progress/1 / PROTO_INCOMPATIBLE -> ProtoMalformed / DebugPing / Progress1 / ProtoIncompatible
func pascal(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == '/' || r == '@' })
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if p == strings.ToUpper(p) {
			p = strings.ToLower(p)
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

func tsNullableStr(v any) string {
	if v == nil {
		return "null"
	}
	return strconv.Quote(str(v))
}

func tsNullableInt(v any) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatInt(intval(v), 10)
}

// flattenDefaults:defaults 嵌套展平为 Go 常量/变量行。
func flattenDefaults(prefix string, m map[string]any, consts, vars *[]string) {
	for _, k := range sortedKeys(m) {
		name := prefix + pascal(k)
		switch v := m[k].(type) {
		case float64:
			if v == math.Trunc(v) {
				*consts = append(*consts, fmt.Sprintf("%s = %d", name, int64(v)))
			} else {
				*consts = append(*consts, fmt.Sprintf("%s = %s", name, strconv.FormatFloat(v, 'g', -1, 64)))
			}
		case map[string]any:
			flattenDefaults(name, v, consts, vars)
		case []any:
			nums := make([]string, len(v))
			for i, x := range v {
				nums[i] = strconv.FormatInt(intval(x), 10)
			}
			*vars = append(*vars, fmt.Sprintf("var %s = []int{%s}", name, strings.Join(nums, ", ")))
		default:
			die("defaults.%s 是不支持的类型 %T", k, v)
		}
	}
}

// sortValue:递归把 map 换成排序后的有序输出(json.Marshal 对 map 本就按键排序,这里处理嵌套数组内的 map)。
func sortValue(v any) any { return v }

func withLineNumbers(src string) string {
	lines := strings.Split(src, "\n")
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "%4d  %s\n", i+1, l)
	}
	return b.String()
}
