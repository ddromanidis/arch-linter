package logger

import (
	"context"
	"log/slog"
	"os"
)

// ANSI color codes
// color cods
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
)

type Logger struct {
	l slog.Logger
}

func NewLogger() Logger {
	return Logger{
		l: *slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			AddSource: false,
			Level:     slog.LevelInfo,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				// Check if the attribute key is the time key.
				if a.Key == slog.TimeKey {
					// a.Value is a slog.TimeValue, so we can get the time.Time object.
					t := a.Value.Time()
					// Format the time into the desired layout.
					// The layout string "2006-01-02 15:04:05" is Go's special way to define a format.
					newTimeStr := t.Format("15:04:05")
					// Return a new slog.Attr with the same key but the new formatted string value.
					return slog.String(slog.TimeKey, newTimeStr)
				}

				// If it's not the time key, return the attribute unchanged.
				return a
			},
		})),
	}
}

func (l Logger) Log(msg string, kvs ...KeyValue) {
	attrs := make([]slog.Attr, 0, len(kvs))
	for _, kv := range kvs {
		attrs = append(attrs, kv.toSlogAttr())
	}

	l.l.LogAttrs(context.TODO(), slog.LevelInfo, msg, attrs...)
}

type KeyValue struct {
	Key   string
	Value any
}

func KV(k string, v any) KeyValue {
	return KeyValue{
		Key:   k,
		Value: v,
	}
}

func (kv KeyValue) toSlogAttr() slog.Attr {
	return slog.Any(kv.Key, kv.Value)
}
