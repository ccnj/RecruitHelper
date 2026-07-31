// Package report 打包并上传现场诊断包(AGENTS.md「全局约定·现场数据上报」,
// 2026-07-31 甲方裁决)。
//
// 包内容是**白名单**,而且是靠"只按固定名字取文件、绝不遍历目录"实现的白名单。
// 这一点是本包最重要的设计:数据目录里躺着 llm-provider.json(含 AI provider 的
// API key)、legacy-job-config.json、几百 MB 的 brain.db.bak-* 备份,以及 blobs/
// 下的候选人截图。如果改成"遍历目录再排除几个",那么将来任何一个新落盘的文件都
// 默认进包 —— 漏的那一次就是 API key 上公网。所以这里没有 filepath.Walk。
//
// 其余边界:包不加密、经明文 http 上传、在服务器上明文留存,均为甲方 2026-07-31
// 知情接受;上报只由人在诊断台显式点击触发,不定时、不自动重试。
package report

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// 包内文件名。与磁盘上的来源名保持一致,解包的人不需要对照表。
const (
	brainLogName = "brain.log"
	brainDBName  = "brain.db"
	traceDBName  = "ai-traces.db"
	manifestName = "manifest.json"

	// 日志保留代数,与 client/electron/logRotate.js 的 DEFAULT_KEEP 对齐。
	// 两边对不上只会导致少带或多找几个不存在的文件,不会出错,但对上更省事。
	logKeepGenerations = 5
)

// logSourceNames 列出要取的日志文件:当前 + 5 代轮转 + 旧格式 .old。
//
// **仍然是固定枚举,不是扫目录**。多代之后名字有了序号,但白名单的实质是"不遍历
// 目录",不是"名字写死"——日志目录将来若混进别的东西,这里也不会把它带走。
// .old 是单代时期的产物,已装机器上还有,留着以免历史断档。
func logSourceNames() []string {
	names := make([]string, 0, logKeepGenerations+2)
	names = append(names, brainLogName)
	for index := 1; index <= logKeepGenerations; index++ {
		names = append(names, fmt.Sprintf("%s.%d", brainLogName, index))
	}
	return append(names, brainLogName+".old")
}

// SnapshotFunc 把一个 SQLite 库的一致快照写到 dst。由 store / aitrace 各自提供,
// 实现是 VACUUM INTO —— 直接拷文件会漏掉未 checkpoint 的 WAL。
type SnapshotFunc func(dst string) error

type Options struct {
	// DataDir 是脑的数据目录。**只用来拼固定文件名**,不遍历。
	DataDir string
	// LogDir 是 Electron 写 brain.log 的目录,由 RECRUITHELPER_LOG_DIR 传入。
	// 开发期直接跑脑时可能为空,那时包里就没有日志,不算错。
	LogDir string
	// AppVersion 打包态由 Electron 注入;开发期为空。
	AppVersion string

	BrainSnapshot SnapshotFunc
	TraceSnapshot SnapshotFunc
}

type FileEntry struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Manifest 是随包的元数据。只有文件清单和版本 —— 候选人明文在包里,不在清单里,
// 因为清单会被写进服务端的元数据表并显示在管理前台列表上。
type Manifest struct {
	AppVersion string      `json:"appVersion,omitempty"`
	PackedAt   string      `json:"packedAt"`
	Files      []FileEntry `json:"files"`
	// Skipped 记录白名单里没取到的项(文件不存在、快照失败)。上报是排障工具,
	// "少了什么"本身就是排障线索,静默省略等于骗读包的人。
	Skipped []string `json:"skipped,omitempty"`
}

type Pack struct {
	Path     string
	Bytes    int64
	Manifest Manifest
}

// Build 打出诊断包,返回包路径与清理函数。**清理函数必须被调用**(defer 即可):
// 临时目录里躺着候选人全库明文的快照,传完就该消失。
func Build(opts Options) (*Pack, func(), error) {
	if opts.DataDir == "" {
		return nil, nil, errors.New("数据目录为空,无法打包")
	}

	workDir, err := os.MkdirTemp("", "recruithelper-report-")
	if err != nil {
		return nil, nil, fmt.Errorf("创建打包临时目录: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(workDir) }

	manifest := Manifest{
		AppVersion: opts.AppVersion,
		PackedAt:   time.Now().Format(time.RFC3339),
	}

	// sources 是白名单本身:名字 -> 磁盘路径。顺序即包内顺序。
	type source struct {
		name string
		path string
	}
	var sources []source

	if opts.LogDir != "" {
		for _, name := range logSourceNames() {
			sources = append(sources, source{name, filepath.Join(opts.LogDir, name)})
		}
	} else {
		manifest.Skipped = append(manifest.Skipped, "日志目录未配置(RECRUITHELPER_LOG_DIR)")
	}

	// 两个库先快照到临时目录,再作为普通文件进包。
	if opts.BrainSnapshot != nil {
		dst := filepath.Join(workDir, brainDBName)
		if err := opts.BrainSnapshot(dst); err != nil {
			// 快照失败不该让整个包失败:日志往往已经够定位问题了。
			manifest.Skipped = append(manifest.Skipped, brainDBName+": "+err.Error())
		} else {
			sources = append(sources, source{brainDBName, dst})
		}
	}
	if opts.TraceSnapshot != nil {
		dst := filepath.Join(workDir, traceDBName)
		if err := opts.TraceSnapshot(dst); err != nil {
			manifest.Skipped = append(manifest.Skipped, traceDBName+": "+err.Error())
		} else {
			sources = append(sources, source{traceDBName, dst})
		}
	}

	packPath := filepath.Join(workDir, "report.tar.gz")
	packFile, err := os.OpenFile(packPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("创建包文件: %w", err)
	}

	gzipWriter := gzip.NewWriter(packFile)
	tarWriter := tar.NewWriter(gzipWriter)

	for _, item := range sources {
		written, err := appendFile(tarWriter, item.name, item.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// brain.log.old 只有轮转过才有;首次运行没有是正常的。
				manifest.Skipped = append(manifest.Skipped, item.name+": 不存在")
				continue
			}
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = packFile.Close()
			cleanup()
			return nil, nil, fmt.Errorf("打包 %s: %w", item.name, err)
		}
		manifest.Files = append(manifest.Files, FileEntry{Name: item.name, Bytes: written})
	}

	// 清单最后写:它要记录前面实际打进去了什么。
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err == nil {
		err = appendBytes(tarWriter, manifestName, manifestBytes)
	}
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = packFile.Close()
		cleanup()
		return nil, nil, fmt.Errorf("写清单: %w", err)
	}

	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		_ = packFile.Close()
		cleanup()
		return nil, nil, fmt.Errorf("收尾 tar: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		_ = packFile.Close()
		cleanup()
		return nil, nil, fmt.Errorf("收尾 gzip: %w", err)
	}
	if err := packFile.Close(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("关闭包文件: %w", err)
	}

	info, err := os.Stat(packPath)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("读包大小: %w", err)
	}

	return &Pack{Path: packPath, Bytes: info.Size(), Manifest: manifest}, cleanup, nil
}

func appendFile(writer *tar.Writer, name, path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	header := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if err := writer.WriteHeader(header); err != nil {
		return 0, err
	}
	return io.Copy(writer, file)
}

func appendBytes(writer *tar.Writer, name string, payload []byte) error {
	header := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(payload)),
		ModTime: time.Now(),
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}
