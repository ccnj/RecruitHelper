package jobconfig

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// 发布参数文档里"不参与发布"的字段。两者都是 2026-07-29 甲方裁决的死字段：
// 职位名称改取 job.name（它才是系统的职位身份键）；职位类别由平台按职位描述
// 自动判定，页面上根本没有人工选择入口。预检必须把它们显式列出来，否则运营
// 会以为自己填的值生效了。
const (
	DeadFieldJobName  = "职位名称"
	DeadFieldJobClass = "职位类别"
)

// 页面下拉的完整取值域，2026-07-29 真机逐项读取所得（见
// docs/智联职位发布页事实与发布裁决-2026-07-29.md §1.2）。发布参数存的就是
// 这些字面文本，填充时按文本精确匹配，因此预检可以离线判定值是否可用。
var (
	employmentTypes = []string{"社招全职", "应届校园招聘", "实习生招聘", "兼职招聘"}
	educationLevels = []string{"不限", "初中及以下", "高中", "中专/中技", "大专", "本科", "硕士", "MBA/EMBA", "博士"}
	experienceBands = []string{"经验不限", "1年以下", "1-3年", "3-5年", "5-10年", "10年以上"}
	salaryMonths    = []string{
		"12个月", "13个月", "14个月", "15个月", "16个月", "17个月", "18个月",
		"19个月", "20个月", "21个月", "22个月", "23个月", "24个月", "25个月",
	}
	// 薪资档位不是连续值，必须按枚举判定：真机下拉共 59 档，1万～2.9万步长
	// 0.1万、3万～9.5万步长 0.5万、10万以上步长 1万。只做"能被 1000 整除"
	// 之类的宽松判定会放过 3.1万 这种并不存在的档位，到填表时才失败。
	salaryTiers = buildSalaryTiers()
)

func buildSalaryTiers() map[string]int64 {
	tiers := map[string]int64{"1千以下": 0}
	for thousand := int64(1); thousand <= 9; thousand++ {
		tiers[strconv.FormatInt(thousand, 10)+"千"] = thousand * 1000
	}
	appendWan := func(value int64) { tiers[formatSalaryTier(value)] = value }
	for step := int64(10); step <= 29; step++ {
		appendWan(step * 1000)
	}
	for step := int64(30); step <= 95; step += 5 {
		appendWan(step * 1000)
	}
	for wan := int64(10); wan <= 24; wan++ {
		appendWan(wan * 10000)
	}
	return tiers
}

// 关键词弹层的总配额。分组结构、组内词库与是否自动预选都随职位类别变化，
// 唯独总数 11 在两个样本上稳定，因此它是关键词唯一能离线校验的硬规则。
const maxPublishKeywords = 11

// 工作地址的约定值：不是地址文本，含义为沿用发布页预填的公司地址。
const defaultWorkplaceLiteral = "默认"

// PublishSpec 是发布参数文档解析后的结构化形态。只保留发布真正要用的字段；
// 死字段解析出来仅用于生成提示，不进入任何填充路径。
type PublishSpec struct {
	EmploymentType string
	Description    string
	Education      string
	Experience     string
	SalaryMin      string
	SalaryMax      string
	SalaryMonths   string
	Keywords       []string
	Workplace      string
	Headcount      int64

	ShowToSeeker  bool
	SyncToMailbox bool

	// 死字段的原值，仅用于告诉运营"这一行没有生效"。
	DeadJobName  string
	DeadJobClass string
}

// PublishIssue 是一条预检问题。Field 为空表示问题不归属单一字段。
type PublishIssue struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type publishParamsDoc struct {
	EmploymentType *string          `json:"工作性质"`
	JobName        *string          `json:"职位名称"`
	Description    *string          `json:"职位描述"`
	JobClass       *string          `json:"职位类别"`
	Education      *string          `json:"最低学历"`
	Experience     *string          `json:"工作经验"`
	SalaryMin      *string          `json:"最低月薪"`
	SalaryMax      *string          `json:"最高月薪"`
	SalaryMonths   *string          `json:"薪资月数"`
	Keywords       []string         `json:"职位关键词"`
	Workplace      *string          `json:"工作地址"`
	Headcount      *json.Number     `json:"招聘人数"`
	ShowToSeeker   *bool            `json:"对求职者展示"`
	SyncToMailbox  *bool            `json:"同步至我的邮箱"`
	SyncColleagues *json.RawMessage `json:"简历同步至同事"`
}

// ParsePublishSpec 解析发布参数文档并做确定性预检。
//
// 返回的 issue 列表为空即表示"按当前可离线判定的规则可以发布"。它**不**保证
// 发布一定成功：关键词能否命中词库、平台会把职位判成哪个类别，都随职位变化，
// 只能在真正打开发布页时才知道，按裁决不进预检。
func ParsePublishSpec(raw string) (PublishSpec, []PublishIssue) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return PublishSpec{}, []PublishIssue{{Message: "发布参数为空，该职位尚未配置发布内容"}}
	}
	var doc publishParamsDoc
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return PublishSpec{}, []PublishIssue{{Message: "发布参数不是合法 JSON，无法解析"}}
	}

	spec := PublishSpec{
		DeadJobName:  deref(doc.JobName),
		DeadJobClass: deref(doc.JobClass),
	}
	var issues []PublishIssue
	add := func(field, format string, args ...any) {
		issues = append(issues, PublishIssue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	spec.EmploymentType = requireEnum(&issues, add, "工作性质", doc.EmploymentType, employmentTypes)
	spec.Education = requireEnum(&issues, add, "最低学历", doc.Education, educationLevels)
	spec.Experience = requireEnum(&issues, add, "工作经验", doc.Experience, experienceBands)
	spec.SalaryMonths = requireEnum(&issues, add, "薪资月数", doc.SalaryMonths, salaryMonths)

	spec.Description = strings.TrimSpace(deref(doc.Description))
	if spec.Description == "" {
		add("职位描述", "缺少职位描述；平台的职位类别正是由它自动判定，缺了会卡在必填")
	} else if len([]rune(spec.Description)) > 10000 {
		add("职位描述", "职位描述超过平台 10000 字上限")
	}

	spec.Workplace = strings.TrimSpace(deref(doc.Workplace))
	if spec.Workplace == "" {
		add("工作地址", "缺少工作地址")
	}

	spec.SalaryMin, spec.SalaryMax = checkSalary(add, doc.SalaryMin, doc.SalaryMax)
	spec.Keywords = checkKeywords(add, doc.Keywords)
	spec.Headcount = checkHeadcount(add, doc.Headcount)

	spec.ShowToSeeker = doc.ShowToSeeker != nil && *doc.ShowToSeeker
	spec.SyncToMailbox = doc.SyncToMailbox != nil && *doc.SyncToMailbox
	return spec, issues
}

// DeadFieldNotices 返回"这些字段不参与发布"的提示。它不是问题，不阻塞发布，
// 但必须让运营看见——否则精心填的值静默失效，界面上完全看不出来。
func (s PublishSpec) DeadFieldNotices(jobName string) []PublishIssue {
	var out []PublishIssue
	if name := strings.TrimSpace(s.DeadJobName); name != "" {
		if normalizeJobName(name) == normalizeJobName(jobName) {
			out = append(out, PublishIssue{
				Field: DeadFieldJobName, Message: "不参与发布，职位名以后台职位名为准（当前两者一致）",
			})
		} else {
			out = append(out, PublishIssue{
				Field:   DeadFieldJobName,
				Message: fmt.Sprintf("不参与发布，将以后台职位名“%s”发布", jobName),
			})
		}
	}
	if class := strings.TrimSpace(s.DeadJobClass); class != "" {
		out = append(out, PublishIssue{
			Field:   DeadFieldJobClass,
			Message: "不参与发布，平台按职位描述自动判定类别",
		})
	}
	return out
}

func requireEnum(
	issues *[]PublishIssue,
	add func(string, string, ...any),
	field string,
	value *string,
	allowed []string,
) string {
	got := strings.TrimSpace(deref(value))
	if got == "" {
		add(field, "缺少%s", field)
		return ""
	}
	for _, candidate := range allowed {
		if candidate == got {
			return got
		}
	}
	add(field, "“%s”不是平台可选值；可选：%s", got, strings.Join(allowed, "、"))
	return ""
}

// checkSalary 校验薪资档位与"最高不超过最低 2 倍"。后台的
// validate_publish_params 完全不管这个关系，运营可以存进平台不接受的组合，
// 所以这条只能由本地预检拦。
func checkSalary(add func(string, string, ...any), rawMin, rawMax *string) (string, string) {
	minText := strings.TrimSpace(deref(rawMin))
	maxText := strings.TrimSpace(deref(rawMax))
	if minText == "" {
		add("最低月薪", "缺少最低月薪")
	}
	if maxText == "" {
		add("最高月薪", "缺少最高月薪")
	}
	if minText == "" || maxText == "" {
		return minText, maxText
	}

	minValue, minOK := parseSalaryTier(minText)
	maxValue, maxOK := parseSalaryTier(maxText)
	if !minOK {
		add("最低月薪", "“%s”不是平台的薪资档位", minText)
	}
	if !maxOK {
		add("最高月薪", "“%s”不是平台的薪资档位", maxText)
	}
	if !minOK || !maxOK {
		return minText, maxText
	}
	// “1千以下”没有确定数值，无法参与倍数比较；此时只保留档位合法性校验。
	if minValue <= 0 {
		return minText, maxText
	}
	if maxValue <= minValue {
		add("最高月薪", "最高月薪必须高于最低月薪")
		return minText, maxText
	}
	if maxValue > minValue*2 {
		add("最高月薪", "平台只允许最高月薪不超过最低月薪的 2 倍（%s 对应上限 %s）",
			minText, formatSalaryTier(minValue*2))
	}
	return minText, maxText
}

// parseSalaryTier 只接受真机枚举内的档位。“1千以下”是合法档位但没有确定
// 数值，返回 0，由调用方跳过倍数比较。
func parseSalaryTier(text string) (int64, bool) {
	value, ok := salaryTiers[text]
	return value, ok
}

func formatSalaryTier(value int64) string {
	if value%10000 == 0 {
		return strconv.FormatInt(value/10000, 10) + "万"
	}
	if value >= 10000 {
		return strconv.FormatFloat(float64(value)/10000, 'f', -1, 64) + "万"
	}
	return strconv.FormatInt(value/1000, 10) + "千"
}

func checkKeywords(add func(string, string, ...any), keywords []string) []string {
	if len(keywords) == 0 {
		add("职位关键词", "缺少职位关键词")
		return nil
	}
	seen := make(map[string]struct{}, len(keywords))
	out := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		trimmed := strings.TrimSpace(keyword)
		if trimmed == "" {
			add("职位关键词", "存在空的关键词")
			continue
		}
		if _, duplicated := seen[trimmed]; duplicated {
			add("职位关键词", "关键词“%s”重复", trimmed)
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) > maxPublishKeywords {
		add("职位关键词", "关键词 %d 个，超过平台总配额 %d 个", len(out), maxPublishKeywords)
	}
	return out
}

func checkHeadcount(add func(string, string, ...any), raw *json.Number) int64 {
	if raw == nil {
		add("招聘人数", "缺少招聘人数")
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(*raw)), 10, 64)
	if err != nil || value <= 0 {
		add("招聘人数", "招聘人数必须是大于 0 的整数")
		return 0
	}
	return value
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// normalizeJobName 是同名判定的唯一口径：统一全角/半角括号与空白后比较。
//
// 归一化**只放宽匹配、不放宽发布**——真正发布时仍用原始 job.name。误判方向
// 因此是"以为平台上已存在而跳过"，即少发；反过来把已存在判成可发才会造成
// 平台上出现两个同名职位。
func normalizeJobName(name string) string {
	replacer := strings.NewReplacer(
		"（", "(", "）", ")", "［", "[", "］", "]", "【", "[", "】", "]",
		"　", " ", "／", "/", "，", ",", "、", ",", "：", ":",
	)
	folded := replacer.Replace(name)
	return strings.Join(strings.Fields(folded), "")
}

// MatchesExistingPosting 判断后台职位名是否已经存在于平台职位名清单中。
func MatchesExistingPosting(jobName string, postingNames []string) bool {
	target := normalizeJobName(jobName)
	if target == "" {
		return false
	}
	for _, posting := range postingNames {
		if normalizeJobName(posting) == target {
			return true
		}
	}
	return false
}

// SortIssues 让预检结论对同一份输入稳定输出，便于人比对两次预检的差异。
func SortIssues(issues []PublishIssue) []PublishIssue {
	sorted := make([]PublishIssue, len(issues))
	copy(sorted, issues)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Field != sorted[j].Field {
			return sorted[i].Field < sorted[j].Field
		}
		return sorted[i].Message < sorted[j].Message
	})
	return sorted
}
