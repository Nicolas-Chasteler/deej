package deej

import (
	"fmt"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	buildTypeDev     = "dev"
	buildTypeRelease = "release"

	// logDirectory holds the internal config (config.go)
	logDirectory = "logs"
)

// NewLogger provides a logger instance for the whole program. Anything that
// isn't explicitly a dev build gets the quiet release logger - a plain
// "go build" used to fall through to debug level with colour codes and a stack
// trace on every warning, all of it landing in the journal. --verbose asks for
// the debug logger regardless of build type.
func NewLogger(buildType string, verbose bool) (*zap.SugaredLogger, error) {
	var loggerConfig zap.Config

	// release: info and above, log to stderr (captured by systemd/journald)
	if buildType != buildTypeDev && !verbose {
		loggerConfig = zap.NewProductionConfig()

		loggerConfig.OutputPaths = []string{"stderr"}
		loggerConfig.Encoding = "console"

		// development: debug and above, log to stderr only, colorful
	} else {
		loggerConfig = zap.NewDevelopmentConfig()

		// make it colorful
		loggerConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	// all build types: make it readable
	loggerConfig.EncoderConfig.EncodeCaller = nil
	loggerConfig.EncoderConfig.EncodeTime = func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(t.Format("2006-01-02 15:04:05.000"))
	}

	loggerConfig.EncoderConfig.EncodeName = func(s string, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(fmt.Sprintf("%-27s", s))
	}

	logger, err := loggerConfig.Build()
	if err != nil {
		return nil, fmt.Errorf("create zap logger: %w", err)
	}

	// no reason not to use the sugared logger - it's fast enough for anything we're gonna do
	sugar := logger.Sugar()

	return sugar, nil
}
