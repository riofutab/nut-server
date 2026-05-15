package logging

import (
	"io"
	"log/slog"
	"os"
)

func Init(service string) *slog.Logger {
	return InitWithWriter(service, os.Stdout)
}

func InitWithWriter(service string, w io.Writer) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(handler).With("service", service)
	slog.SetDefault(logger)
	return logger
}
