// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"
	"log/slog"
	"strings"
)

// enginelog.go gives the engine ONE log format.
//
// ⛔ WHAT WAS MEASURED, 2026-09-18. One first hour produced TWO formats from one
// binary:
//
//	2026/09/18 11:44:24 INFO session runtime: ...        (no --quiet)
//	time=2026-09-18T11:44:24.000+02:00 level=ERROR ...   (with --quiet)
//
// The first is Go's default slog handler, which nobody had chosen — it is what
// `slog.Default()` does when no handler is installed, and it writes through the
// `log` package. The second came from the one place that DID install a handler:
// `quickstart --quiet`, which set a TextHandler to raise the level. So the flag
// that was supposed to make the output quieter also changed its shape, and an
// operator grepping their logs had to know which flag the engine was started with.
//
// And the same line mixed two clocks: the prefix timestamp was LOCAL with no zone
// while the values inside the line were UTC (`ObservedAt:2026-09-18 14:10:36 +0000
// UTC`). Two timestamps eleven minutes apart in the same line, both correct.
//
// One handler, installed by `runEngine`, is what removes both: every engine start
// — `serve`, `quickstart`, with or without --quiet — writes `time=…Z level=… msg=…`
// with the time in UTC. --quiet now changes only the LEVEL, which is what it says
// it does.
//
// ⛔ AND THE LEVEL IS NOW THE DOCUMENTED ONE. The configuration reference has said
// for releases that OLIVARES_LOG_LEVEL is "the minimum log level the engine emits",
// and it governed only the in-memory capture ring: the process itself emitted
// whatever Go's default handler did, so `debug` produced nothing extra and `warn`
// silenced nothing. Honouring it here makes the documented sentence true.

// engineLogFormat is the one format, named so a test can assert the choice rather
// than a substring of one line.
const engineLogFormat = "logfmt-utc"

// engineLogHandler builds the engine's log handler: logfmt with the timestamp in
// UTC, at the given level.
func engineLogHandler(w io.Writer, level slog.Level) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// The record's own timestamp, in UTC. Every value INSIDE these lines is
			// already UTC (the engine's clock is), and a prefix in local time with no
			// zone beside them puts two clocks in one line — measured on a first boot,
			// where it made an operator read one event as two.
			if a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
				a.Value = slog.TimeValue(a.Value.Time().UTC())
			}
			return a
		},
	})
}

// engineLogLevel resolves the level the engine EMITS at: the operator's
// OLIVARES_LOG_LEVEL, or INFO. An unreadable value is INFO and says so, exactly as
// the capture ring's own loader does — the two must not disagree about one
// variable.
func engineLogLevel(getenv func(string) string, quiet bool) (slog.Level, string) {
	if quiet {
		// --quiet is a per-invocation instruction about THIS run's output and wins over
		// a deployment-wide variable, which is the only order that makes the flag mean
		// what its help says.
		return slog.LevelError, "--quiet"
	}
	raw := strings.TrimSpace(getenv(envLogLevel))
	switch strings.ToLower(raw) {
	case "":
		return slog.LevelInfo, "default"
	case "debug":
		return slog.LevelDebug, envLogLevel
	case "info":
		return slog.LevelInfo, envLogLevel
	case "warn":
		return slog.LevelWarn, envLogLevel
	case "error":
		return slog.LevelError, envLogLevel
	default:
		return slog.LevelInfo, "default (" + envLogLevel + " was not a level)"
	}
}

// installEngineLogger installs the engine's one handler as the process default and
// returns the logger everything downstream reads through slog.Default().
//
// It runs before the first boot line, so there is no window in which the engine
// writes in one shape and then changes to another.
func installEngineLogger(w io.Writer, getenv func(string) string, quiet bool) *slog.Logger {
	level, _ := engineLogLevel(getenv, quiet)
	log := slog.New(engineLogHandler(w, level))
	slog.SetDefault(log)
	return log
}
