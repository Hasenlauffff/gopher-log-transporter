package parser

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/namsson/gopher-log-transporter/internal/reader"
)

type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Labels    map[string]string `json:"labels"`
	Raw       string            `json:"raw"`
	Filepath  string            `json:"filepath"`
}

type ContainerLog struct {
	Log    string `json:"log"`
	Stream string `json:"stream"`
	Time   string `json:"time"`
}

type AppLog struct {
	Level string  `json:"level"`
	Msg   string  `json:"msg"`
	TS    float64 `json:"ts"`
}

type Parser struct {
	logger *slog.Logger
	in     <-chan reader.RawLine
	out    chan<- LogEntry
}

func NewParser(in <-chan reader.RawLine, out chan<- LogEntry, logger *slog.Logger) *Parser {
	return &Parser{
		logger: logger.With("component", "Parser"),
		in:     in,
		out:    out,
	}
}

func decodeOuter(raw string) (ContainerLog, error) {
	var cl ContainerLog
	if err := json.Unmarshal([]byte(raw), &cl); err == nil && cl.Log != "" {
		return cl, nil
	}

	parts := strings.SplitN(strings.TrimSpace(raw), " ", 4)
	if len(parts) < 4 {
		return ContainerLog{}, errors.New("unknown log format")
	}
	return ContainerLog{
		Time:   parts[0],
		Stream: parts[1],
		Log:    parts[3],
	}, nil
}
func decodeInner(log string) (AppLog, error) {
	if !strings.HasPrefix(strings.TrimSpace(log), "{") {
		return AppLog{}, errors.New("not JSON")
	}
	var alog AppLog
	if err := json.Unmarshal([]byte(log), &alog); err != nil {
		return AppLog{}, err
	}
	return alog, nil
}

func buildLabels(filepath string) map[string]string {
	labels := map[string]string{
		"namespace": "unknown",
		"pod":       "unknown",
		"container": "unknown",
	}
	parts := strings.Split(filepath, "/")
	for i, p := range parts {
		if p == "pods" && i+2 < len(parts) {
			labels["container"] = parts[i+2]
			segs := strings.SplitN(parts[i+1], "_", 3)
			if len(segs) >= 2 {
				labels["namespace"] = segs[0]
				labels["pod"] = segs[1]
			}
			break
		}
	}
	return labels
}

func (p *Parser) parse(line reader.RawLine) (LogEntry, error) {
	cl, err := decodeOuter(line.Content)
	if err != nil {
		return LogEntry{}, err
	}
	alog, innerErr := decodeInner(cl.Log)
	labels := buildLabels(line.Filepath)
	labels["stream"] = cl.Stream
	entry := LogEntry{
		Labels:   labels,
		Filepath: line.Filepath,
		Raw:      strings.TrimSpace(cl.Log),
	}

	if innerErr != nil {
		entry.Level = "unknown"
		entry.Message = strings.TrimSpace(cl.Log)
		entry.Timestamp, _ = time.Parse(time.RFC3339Nano, cl.Time)
	} else {
		entry.Level = alog.Level
		entry.Message = alog.Msg
		if alog.TS > 0 {
			entry.Timestamp = time.Unix(int64(alog.TS), 0).UTC()
		} else {
			entry.Timestamp, _ = time.Parse(time.RFC3339Nano, cl.Time)
		}
	}
	return entry, nil
}

func (p *Parser) Run(ctx context.Context) {
	p.logger.Info("parser starting")
	for {
		select {
		case <-ctx.Done():
			p.logger.Info("parser stopping")
			return

		case line, ok := <-p.in:
			if !ok {
				p.logger.Info("input channel closed, parser stopping")
				return
			}
			entry, err := p.parse(line)
			if err != nil {
				p.logger.Warn("parse failed, skipping line",
					"err", err,
					"filepath", line.Filepath,
					"offset", line.Offset,
				)
				continue
			}
			p.out <- entry
		}
	}
}
