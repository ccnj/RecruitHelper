package testfixture

// SourcingFiltersDocument is a complete, valid six-group legacy 职位筛选
// document for tests that exercise the executable sourcing view.
const SourcingFiltersDocument = `[
	{"fieldKey":"age","title":"年龄要求","multiple":false,"controlType":"checkbox-group","customMinAge":25,"customMaxAge":45,"options":[
		{"label":"不限","action":"age:不限","selected":false},
		{"label":"20-25","action":"age:20-25","selected":false},
		{"label":"25-30","action":"age:25-30","selected":false},
		{"label":"30-35","action":"age:30-35","selected":false},
		{"label":"35-40","action":"age:35-40","selected":false},
		{"label":"40以上","action":"age:40以上","selected":false},
		{"label":"自定义","action":"age:自定义","selected":true}
	]},
	{"fieldKey":"activeTime","title":"活跃日期","multiple":false,"controlType":"radio-group","options":[
		{"label":"不限","action":"activeTime:不限","selected":false},
		{"label":"今日活跃","action":"activeTime:今日活跃","selected":false},
		{"label":"3天内活跃","action":"activeTime:3天内活跃","selected":true},
		{"label":"7天内活跃","action":"activeTime:7天内活跃","selected":false},
		{"label":"30天内活跃","action":"activeTime:30天内活跃","selected":false}
	]},
	{"fieldKey":"careerStatuses","title":"求职状态","multiple":true,"controlType":"checkbox-group","options":[
		{"label":"不限","action":"careerStatuses:不限","selected":true},
		{"label":"在职-正在找工作","action":"careerStatuses:在职-正在找工作","selected":false},
		{"label":"离职-正在找工作","action":"careerStatuses:离职-正在找工作","selected":false},
		{"label":"在职-看看机会","action":"careerStatuses:在职-看看机会","selected":false},
		{"label":"在职-暂不找工作","action":"careerStatuses:在职-暂不找工作","selected":false}
	]},
	{"fieldKey":"educations","title":"学历要求","multiple":true,"controlType":"checkbox-group","options":[
		{"label":"不限","action":"educations:不限","selected":false},
		{"label":"初中及以下","action":"educations:初中及以下","selected":false},
		{"label":"高中","action":"educations:高中","selected":false},
		{"label":"中专/中技","action":"educations:中专/中技","selected":false},
		{"label":"大专","action":"educations:大专","selected":true},
		{"label":"本科","action":"educations:本科","selected":true},
		{"label":"硕士","action":"educations:硕士","selected":true},
		{"label":"MBA/EMBA","action":"educations:MBA/EMBA","selected":true},
		{"label":"博士","action":"educations:博士","selected":true}
	]},
	{"fieldKey":"gender","title":"性别要求","multiple":false,"controlType":"radio-group","options":[
		{"label":"不限","action":"gender:不限","selected":true},
		{"label":"男","action":"gender:男","selected":false},
		{"label":"女","action":"gender:女","selected":false}
	]},
	{"fieldKey":"filterTypes","title":"人才范围","multiple":true,"controlType":"checkbox-group","options":[
		{"label":"不限","action":"filterTypes:不限","selected":false},
		{"label":"过滤我已看过","action":"filterTypes:过滤我已看过","selected":true},
		{"label":"过滤同事已聊","action":"filterTypes:过滤同事已聊","selected":false}
	]}
]`
