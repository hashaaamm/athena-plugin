package app

import (
	"io"
	"log/slog"

	"{{ cookiecutter.go_module }}/internal/config"
)

// NewLogger builds the process logger: JSON where a log aggregator reads it, text locally.
//
// Cloud Run's logging agent parses a JSON line into structured fields you can filter on; a formatted
// string arrives as one opaque blob. It also reads the level from a field named `severity` and the
// text from `message` — slog writes `level` and `msg`, which Cloud Logging shows as DEFAULT-severity
// entries with the text buried in jsonPayload. The JSON handler renames both, and spells WARN the
// way Cloud Logging does.
func NewLogger(cfg config.Config, out io.Writer) *slog.Logger {
	level, _ := cfg.SlogLevel() // validated at load
	options := &slog.HandlerOptions{Level: level}
	if !cfg.JSONLogs {
		return slog.New(slog.NewTextHandler(out, options))
	}
	options.ReplaceAttr = func(groups []string, attr slog.Attr) slog.Attr {
		if len(groups) > 0 {
			return attr
		}
		switch attr.Key {
		case slog.LevelKey:
			attr.Key = "severity"
			if level, ok := attr.Value.Any().(slog.Level); ok && level == slog.LevelWarn {
				attr.Value = slog.StringValue("WARNING")
			}
		case slog.MessageKey:
			attr.Key = "message"
		case "message", "severity":
			// An attribute that would now collide with the renamed built-ins. Two `message` keys in
			// one JSON object is a line whose meaning depends on which parser reads it.
			attr.Key = "attr_" + attr.Key
		}
		return attr
	}
	return slog.New(slog.NewJSONHandler(out, options))
}
