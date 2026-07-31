package report

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这个测试守的是本包唯一的阻断级不变量:数据目录里的 API key 与备份绝不进包。
// 打包实现如果哪天从"按名字取"退回"遍历目录再排除",这里必须红。
func TestBuildOnlyPacksWhitelistedNames(t *testing.T) {
	dataDir := t.TempDir()
	logDir := t.TempDir()

	// 数据目录里放上真实会出现、且绝对不能出包的东西。
	writeFile(t, filepath.Join(dataDir, "llm-provider.json"), `{"api_key":"sk-must-never-leave"}`)
	writeFile(t, filepath.Join(dataDir, "legacy-job-config.json"), `{"licenseToken":"tok"}`)
	writeFile(t, filepath.Join(dataDir, "brain.db.bak-20260728-manual-sql"), "旧备份")
	if err := os.MkdirAll(filepath.Join(dataDir, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dataDir, "blobs", "shot.jpg"), "截图字节")

	// 多代轮转后的现场：当前 + 两代序号 + 单代时期遗留的 .old。
	writeFile(t, filepath.Join(logDir, "brain.log"), "当期日志")
	writeFile(t, filepath.Join(logDir, "brain.log.1"), "上一代")
	writeFile(t, filepath.Join(logDir, "brain.log.2"), "上上代")
	writeFile(t, filepath.Join(logDir, "brain.log.old"), "旧格式遗留")

	pack, cleanup, err := Build(Options{
		DataDir: dataDir,
		LogDir:  logDir,
		BrainSnapshot: func(dst string) error {
			return os.WriteFile(dst, []byte("brain 快照"), 0o600)
		},
		TraceSnapshot: func(dst string) error {
			return os.WriteFile(dst, []byte("trace 快照"), 0o600)
		},
	})
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	defer cleanup()

	names, bodies := readPack(t, pack.Path)

	want := []string{
		brainLogName, brainLogName + ".1", brainLogName + ".2", brainLogName + ".old",
		brainDBName, traceDBName, manifestName,
	}
	for _, name := range want {
		if _, ok := names[name]; !ok {
			t.Errorf("包里缺少 %s", name)
		}
	}
	if len(names) != len(want) {
		t.Errorf("包内文件数应为 %d，实际 %d：%v", len(want), len(names), keys(names))
	}

	// 逐字节确认 key 没有从任何缝里漏进去(比如被谁塞进 manifest)。
	joined := strings.Join(bodies, "\n")
	for _, forbidden := range []string{"sk-must-never-leave", "llm-provider", "brain.db.bak", "shot.jpg"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("包内出现禁止内容 %q", forbidden)
		}
	}
}

// 快照失败不该让整包失败:日志常常已经够定位问题,而且这时人更需要拿到日志。
func TestBuildKeepsGoingWhenSnapshotFails(t *testing.T) {
	logDir := t.TempDir()
	writeFile(t, filepath.Join(logDir, "brain.log"), "只有日志")

	pack, cleanup, err := Build(Options{
		DataDir:       t.TempDir(),
		LogDir:        logDir,
		BrainSnapshot: func(string) error { return errors.New("库正忙") },
	})
	if err != nil {
		t.Fatalf("快照失败不应让打包失败: %v", err)
	}
	defer cleanup()

	names, _ := readPack(t, pack.Path)
	if _, ok := names[brainDBName]; ok {
		t.Error("快照失败时不该有 brain.db 进包")
	}
	if _, ok := names[brainLogName]; !ok {
		t.Error("日志仍应进包")
	}
	if len(pack.Manifest.Skipped) == 0 {
		t.Error("清单必须记下被跳过的项，静默省略等于骗读包的人")
	}
}

// 清理函数必须真的把候选人明文快照从磁盘上抹掉。
func TestCleanupRemovesTempArtifacts(t *testing.T) {
	logDir := t.TempDir()
	writeFile(t, filepath.Join(logDir, "brain.log"), "日志")

	pack, cleanup, err := Build(Options{DataDir: t.TempDir(), LogDir: logDir})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()

	if _, err := os.Stat(pack.Path); !os.IsNotExist(err) {
		t.Errorf("cleanup 后包文件仍在: %v", err)
	}
}

func TestBuildRejectsEmptyDataDir(t *testing.T) {
	if _, _, err := Build(Options{}); err == nil {
		t.Fatal("数据目录为空时应报错")
	}
}

func readPack(t *testing.T, path string) (map[string]bool, []string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()

	names := map[string]bool{}
	var bodies []string
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err != nil {
			break
		}
		names[header.Name] = true
		payload := make([]byte, header.Size)
		_, _ = reader.Read(payload)
		bodies = append(bodies, string(payload))

		if header.Name == manifestName {
			var manifest Manifest
			if err := json.Unmarshal(payload, &manifest); err != nil {
				t.Errorf("清单不是合法 JSON: %v", err)
			}
		}
	}
	return names, bodies
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
