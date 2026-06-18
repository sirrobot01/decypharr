package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	once   sync.Once
	logger zerolog.Logger

	rotatingLogFileOnce sync.Once
	rotatingLogFile     *lumberjack.Logger
)

func GetLogPath() string {
	logsDir := filepath.Join(config.GetMainPath(), "logs")

	if _, err := os.Stat(logsDir); os.IsNotExist(err) {
		if err := os.MkdirAll(logsDir, 0755); err != nil {
			panic(fmt.Sprintf("Failed to create logs directory: %v", err))
		}
	}

	return logsDir
}

// sharedRotatingLogFile returns the process-wide lumberjack writer. All
// component loggers share one rotator so they don't race on the same file
// (each *lumberjack.Logger runs its own mill goroutine and rotation cycle).
func sharedRotatingLogFile() *lumberjack.Logger {
	rotatingLogFileOnce.Do(func() {
		rotatingLogFile = &lumberjack.Logger{
			Filename:   filepath.Join(GetLogPath(), "decypharr.log"),
			MaxSize:    10,
			MaxAge:     15,
			MaxBackups: 10,
			Compress:   true,
		}
	})
	return rotatingLogFile
}

func New(prefix string) zerolog.Logger {
	level := config.Get().LogLevel

	rotatingLogFile := sharedRotatingLogFile()

	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "2006-01-02 15:04:05",
		NoColor:    false, // Set to true if you don't want colors
		FormatLevel: func(i interface{}) string {
			var colorCode string
			switch strings.ToLower(fmt.Sprintf("%s", i)) {
			case "debug":
				colorCode = "\033[36m"
			case "info":
				colorCode = "\033[32m"
			case "warn":
				colorCode = "\033[33m"
			case "error":
				colorCode = "\033[31m"
			case "fatal":
				colorCode = "\033[35m"
			case "panic":
				colorCode = "\033[41m"
			default:
				colorCode = "\033[37m" // White
			}
			return fmt.Sprintf("%s| %-6s|\033[0m", colorCode, strings.ToUpper(fmt.Sprintf("%s", i)))
		},
		FormatMessage: func(i interface{}) string {
			return fmt.Sprintf("[%s] %v", prefix, i)
		},
	}

	fileWriter := zerolog.ConsoleWriter{
		Out:        rotatingLogFile,
		TimeFormat: "2006-01-02 15:04:05",
		NoColor:    true, // No colors in file output
		FormatLevel: func(i interface{}) string {
			return strings.ToUpper(fmt.Sprintf("| %-6s|", i))
		},
		FormatMessage: func(i interface{}) string {
			return fmt.Sprintf("[%s] %v", prefix, i)
		},
	}

	multi := zerolog.MultiLevelWriter(consoleWriter, fileWriter)

	logger := zerolog.New(multi).
		With().
		Timestamp().
		Logger().
		Level(zerolog.InfoLevel)

	// Set the log level
	level = strings.ToLower(level)
	switch level {
	case "debug":
		logger = logger.Level(zerolog.DebugLevel)
	case "info":
		logger = logger.Level(zerolog.InfoLevel)
	case "warn":
		logger = logger.Level(zerolog.WarnLevel)
	case "error":
		logger = logger.Level(zerolog.ErrorLevel)
	case "trace":
		logger = logger.Level(zerolog.TraceLevel)
	}
	return logger
}

func Default() zerolog.Logger {
	once.Do(func() {
		logger = New("decypharr")
	})
	return logger
}
