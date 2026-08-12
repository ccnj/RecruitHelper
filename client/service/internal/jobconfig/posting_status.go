package jobconfig

import (
	"strings"

	"recruithelper/contract/gen/go/protocol"
)

// PostingStatusLabelOnline 是"职位在线"的唯一判定页签文案。智联职位管理页
// 状态分区页签的已知全集:在线中/未上线/审核中/未过审
// (docs/智联职位发布页事实与发布裁决-2026-07-29.md)。
const PostingStatusLabelOnline = "在线中"

// PostingStatusNotFound 表示平台全部状态分区都找不到同名职位。
const PostingStatusNotFound = "平台未见"

// FindPostingStatus 返回职位名落在哪个状态分区:按分区顺序取第一个命中分区
// 的页签原样文案,全部分区未命中返回 PostingStatusNotFound。同名判定与发布
// 预检共用 normalizeJobName 口径。
func FindPostingStatus(jobName string, sections []protocol.JobPostingSection) string {
	target := normalizeJobName(jobName)
	if target == "" {
		return PostingStatusNotFound
	}
	for _, section := range sections {
		for _, name := range section.Names {
			if normalizeJobName(name) == target {
				return strings.TrimSpace(section.Label)
			}
		}
	}
	return PostingStatusNotFound
}
