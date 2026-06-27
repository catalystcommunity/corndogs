package logging

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/CatalystCommunity/corndogs/corndogs/server/config"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// gormZerolog adapts gorm's logger to zerolog so database logs honor the same
// LOGLEVEL threshold (and format) as the rest of the service instead of gorm's
// own default (which, for example, emits SLOW SQL warnings regardless).
type gormZerolog struct {
	level         gormlogger.LogLevel
	slowThreshold time.Duration
}

// NewGormLogger returns a gorm logger that writes through zerolog at the level
// mapped from config.LogLevel.
func NewGormLogger() gormlogger.Interface {
	return &gormZerolog{
		level:         gormLevelFromConfig(),
		slowThreshold: 200 * time.Millisecond,
	}
}

// gormLevelFromConfig maps the repo's LOGLEVEL onto gorm's coarser levels,
// mirroring logging.setLogLevel (including its default-to-Info fallback).
func gormLevelFromConfig() gormlogger.LogLevel {
	switch strings.ToLower(config.LogLevel) {
	case "panic", "fatal", "error":
		return gormlogger.Error
	case "warn":
		return gormlogger.Warn
	case "info", "debug", "trace":
		return gormlogger.Info
	case "silent", "disabled":
		return gormlogger.Silent
	default:
		return gormlogger.Info
	}
}

func (g gormZerolog) LogMode(l gormlogger.LogLevel) gormlogger.Interface {
	g.level = l
	return &g
}

func (g *gormZerolog) Info(_ context.Context, msg string, data ...interface{}) {
	if g.level >= gormlogger.Info {
		log.Info().Msgf(msg, data...)
	}
}

func (g *gormZerolog) Warn(_ context.Context, msg string, data ...interface{}) {
	if g.level >= gormlogger.Warn {
		log.Warn().Msgf(msg, data...)
	}
}

func (g *gormZerolog) Error(_ context.Context, msg string, data ...interface{}) {
	if g.level >= gormlogger.Error {
		log.Error().Msgf(msg, data...)
	}
}

func (g *gormZerolog) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.level <= gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	switch {
	case err != nil && g.level >= gormlogger.Error && !errors.Is(err, gorm.ErrRecordNotFound):
		sql, rows := fc()
		log.Error().Err(err).Dur("elapsed", elapsed).Int64("rows", rows).Str("sql", sql).Msg("gorm")
	case g.slowThreshold != 0 && elapsed > g.slowThreshold && g.level >= gormlogger.Warn:
		sql, rows := fc()
		log.Warn().Dur("elapsed", elapsed).Int64("rows", rows).Str("sql", sql).Msg("gorm slow sql")
	case g.level >= gormlogger.Info:
		sql, rows := fc()
		log.Debug().Dur("elapsed", elapsed).Int64("rows", rows).Str("sql", sql).Msg("gorm")
	}
}
