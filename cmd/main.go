package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/namsson/gopher-log-transporter/internal/parser"
	"github.com/namsson/gopher-log-transporter/internal/reader"
	"github.com/namsson/gopher-log-transporter/internal/sender"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	logger.Info("gopher-log-transporter starting")

	// Đọc từ env var, fallback về giá trị mặc định nếu không có
	logFilePath := getEnv("LOG_FILE_PATH",
		"/var/log/pods/default_busybox-app/busybox/0.log")
	stateFilePath := getEnv("STATE_FILE_PATH",
		"/tmp/gopher-tailer.state")
	lokiURL := getEnv("LOKI_URL",
		"http://localhost:3100/loki/api/v1/push")

	rawLines := make(chan reader.RawLine, 1000)
	logEntries := make(chan parser.LogEntry, 1000)

	tailer := reader.NewTailer(
		reader.Config{
			Filepath:     logFilePath,
			PollInterval: 500 * time.Millisecond,
			StateFile:    stateFilePath,
		},
		rawLines,
		logger,
	)

	p := parser.NewParser(rawLines, logEntries, logger)

	s := sender.NewLokiSender(logEntries, logger, lokiURL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go tailer.Run(ctx)
	go p.Run(ctx)
	go s.Run(ctx)

	logger.Info("pipeline started",
		"log_file", logFilePath,
		"loki_url", lokiURL,
		"state_file", stateFilePath,
	)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	received := <-sig
	logger.Info("shutdown signal received", "signal", received.String())
	cancel()
	time.Sleep(3 * time.Second)
	logger.Info("gopher-log-transporter stopped")
}
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
