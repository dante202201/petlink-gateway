// Package logger 封装 zap 结构化日志初始化。
package logger

import (
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Init 根据级别与文件路径初始化全局 zap Logger。
// filename 为空时仅输出到控制台。
func Init(level, filename string) (*zap.Logger, error) {
	lvl := zapcore.InfoLevel
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = zapcore.InfoLevel
	}

	consoleCfg := zap.NewDevelopmentEncoderConfig()
	consoleCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	consoleCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	consoleEnc := zapcore.NewConsoleEncoder(consoleCfg)

	fileCfg := zap.NewProductionEncoderConfig()
	fileCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	fileCfg.EncodeLevel = zapcore.CapitalLevelEncoder
	fileEnc := zapcore.NewJSONEncoder(fileCfg)

	var cores []zapcore.Core
	cores = append(cores, zapcore.NewCore(consoleEnc, zapcore.AddSync(os.Stdout), lvl))

	if filename != "" {
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(fileEnc, zapcore.AddSync(f), lvl))
	}

	core := zapcore.NewTee(cores...)
	log := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(0))
	zap.ReplaceGlobals(log)
	return log, nil
}
