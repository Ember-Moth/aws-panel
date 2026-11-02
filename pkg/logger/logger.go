package logger

import (
	"io"
	"log/slog"
	"strings"
)

func New(writer io.Writer, level string) *slog.Logger {
	var threshold slog.Level
	_ = threshold.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: threshold,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			key := strings.ToLower(attr.Key)
			for _, word := range []string{"password", "secret", "token", "authorization", "dsn"} {
				if strings.Contains(key, word) {
					return slog.String(attr.Key, "[redacted]")
				}
			}
			return attr
		},
	}))
}
