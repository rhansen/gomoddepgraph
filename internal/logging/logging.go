package logging

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

const (
	LevelTrace   = slog.LevelDebug - 4 // -8
	LevelDebug   = slog.LevelDebug     // -4
	LevelVerbose = slog.LevelDebug + 2 // -2
	LevelInfo    = slog.LevelInfo      // 0
	LevelNotice  = slog.LevelInfo + 2  // 2
	LevelWarn    = slog.LevelWarn      // 4
	LevelError   = slog.LevelError     // 8
	LevelFatal   = slog.LevelError + 4 // 12
)

var validLevels = []string{"trace", "debug", "verbose", "info", "notice", "warn", "error", "fatal"}

// BumpLevel returns lvl bumped to the next higher (more severe) or lower (less severe) named level.
func BumpLevel(lvl slog.Level, lower bool) slog.Level {
	// Take advantage of the symmetry around 0.
	var orient slog.Level = 1
	if lower {
		orient = -1
		lvl *= orient
	}
	var adj slog.Level = 4
	if LevelDebug+2 <= lvl && lvl < LevelWarn+2 {
		adj = 2
	}
	lvl += adj
	lvl *= orient
	return lvl
}

func StringToLevel(arg string) (slog.Level, error) {
	arg = strings.ToLower(arg)
	switch arg {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "verbose":
		return LevelVerbose, nil
	case "info":
		return LevelInfo, nil
	case "notice":
		return LevelNotice, nil
	case "warn":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	case "fatal":
		return LevelFatal, nil
	default:
		if slices.Contains(validLevels, arg) {
			panic("need to update the switch cases")
		}
		return 0, fmt.Errorf("invalid log level; expected one of: %v", strings.Join(validLevels, ", "))
	}
}
