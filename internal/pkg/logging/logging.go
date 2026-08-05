// Package logging 为所有 sctl 子命令初始化统一的 zap 日志。
//
// 关键约束:日志绝不写 stdout —— `sctl mcp` 以 stdout 承载 MCP 协议帧,CLI 动词以
// stdout 输出 `-o json` / `-o source` 结果,任何日志混入都会破坏它们。cago 的 component.Core() 默认
// 把日志写 stdout,因此 sctl 改由本包用 logger.New +
// NewFileCore 构建 logger 并 logger.SetLogger 注入 cago,后续代码照常用 logger.Ctx(ctx)。
//
// 出口:持久文件日志(<dataDir>/logs/sctl.log 收 level+,sctl.err.log 只收 error+)
// 外加 stderr console core —— sctl serve/connect 是前台命令,stderr 让用户实时看到日志,
// 且 stderr 与 stdout 协议/JSON 输出互不干扰。
package logging

import (
	"os"
	"path/filepath"

	"github.com/cago-frame/cago/pkg/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/scriptscat/sctl/internal/pkg/paths"
)

// Setup 构建并注入全局 logger。level 取 debug/info/warn/error。文件日志目录不可写时
// 静默降级为仅 stderr(不因日志目录问题阻断命令)。返回构建出的 logger 供直接使用。
func Setup(level string) *zap.Logger {
	lvl := logger.ToLevel(level)

	cores := []zapcore.Core{stderrCore(lvl)}
	if fileCores, ok := fileCores(lvl); ok {
		cores = append(cores, fileCores...)
	}

	l := zap.New(zapcore.NewTee(cores...), zap.AddCaller())
	logger.SetLogger(l)
	return l
}

func stderrCore(lvl zapcore.Level) zapcore.Core {
	cfg := zap.NewDevelopmentEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	return zapcore.NewCore(zapcore.NewConsoleEncoder(cfg), zapcore.Lock(os.Stderr), lvl)
}

func fileCores(lvl zapcore.Level) ([]zapcore.Core, bool) {
	dir := paths.LogsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false
	}
	return []zapcore.Core{
		logger.NewFileCore(lvl, filepath.Join(dir, "sctl.log")),
		logger.NewFileCore(logger.ToLevel("error"), filepath.Join(dir, "sctl.err.log")),
	}, true
}
