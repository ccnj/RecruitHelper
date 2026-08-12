package secposture

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

// parseMrtLog 从 mrt.log 内容里提取最近一次运行的摘要。纯函数,固定样本可测。
//
// 日志是追加式的,每次运行一段,段内有 "Started On <时刻>"、版本行、
// "Quick Scan Results"、"Finished On"。我们只看最后一段:
//   - LastRunAt 取 "Started On " 后的原文;
//   - Version 取该段之前最近的 "Removal Tool v<版本>";
//   - 检出判定必须同时出现 "Threat Detected" 与我们的 exe 名 ——
//     别家威胁被检出不关我们的事,不采不报。
func parseMrtLog(raw []byte) MsrtRun {
	text := decodeMaybeUTF16(raw)
	idx := strings.LastIndex(text, "Started On ")
	if idx < 0 {
		return MsrtRun{Status: StateUnknown}
	}
	head, tail := text[:idx], text[idx:]

	run := MsrtRun{Status: MsrtScanned}
	startLine := tail[len("Started On "):]
	if lineEnd := strings.IndexAny(startLine, "\r\n"); lineEnd >= 0 {
		startLine = startLine[:lineEnd]
	}
	run.LastRunAt = strings.TrimSpace(startLine)

	if match := lastVersionPattern.FindAllStringSubmatch(head, -1); len(match) > 0 {
		run.Version = "v" + match[len(match)-1][1]
	}
	if strings.Contains(tail, "Threat Detected") && strings.Contains(tail, exeMarker) {
		run.DetectedUs = true
		run.RemovedUs = strings.Contains(tail, "and Removed!")
	}
	return run
}

var lastVersionPattern = regexp.MustCompile(`Removal Tool v([0-9.]+)`)

// decodeMaybeUTF16 兜 Windows 日志两种常见编码:带 BOM 的 UTF-16LE/BE 与
// 平文 ASCII/UTF-8。判据只认 BOM,不猜。
func decodeMaybeUTF16(raw []byte) string {
	if len(raw) < 2 {
		return string(raw)
	}
	littleEndian := raw[0] == 0xFF && raw[1] == 0xFE
	bigEndian := raw[0] == 0xFE && raw[1] == 0xFF
	if !littleEndian && !bigEndian {
		return string(raw)
	}
	payload := raw[2:]
	units := make([]uint16, 0, len(payload)/2)
	for i := 0; i+1 < len(payload); i += 2 {
		if littleEndian {
			units = append(units, uint16(payload[i])|uint16(payload[i+1])<<8)
		} else {
			units = append(units, uint16(payload[i])<<8|uint16(payload[i+1]))
		}
	}
	return string(utf16.Decode(units))
}
