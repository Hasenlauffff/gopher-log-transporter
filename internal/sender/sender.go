package sender

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/namsson/gopher-log-transporter/internal/parser"
)

type LokiPushRequest struct {
	Streams []LokiStream `json:"streams"`
}

type LokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][]string        `json:"values"`
}

type LokiSender struct {
	logger        *slog.Logger
	in            <-chan parser.LogEntry
	maxBatch      int
	flushInterval time.Duration
	httpClient    *http.Client
	lokiURL       string
	buffer        []parser.LogEntry
}

func NewLokiSender(in <-chan parser.LogEntry, logger *slog.Logger, lokiURL string) *LokiSender {
	return &LokiSender{
		logger:        logger.With("component", "LokiSender"),
		in:            in,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		buffer:        make([]parser.LogEntry, 0, 100),
		lokiURL:       lokiURL,
		maxBatch:      100,
		flushInterval: 5 * time.Second,
	}
}
func (s *LokiSender) Run(ctx context.Context) {
	s.logger.Info("sender starting")

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("sender stopping")
			if len(s.buffer) > 0 {
				s.logger.Info("flushing remaining buffer before exit", "count", len(s.buffer))
				s.sendLogs()
			}
			return
		case <-ticker.C:
			if len(s.buffer) > 0 {
				s.sendLogs()
			}
		case logEntry, ok := <-s.in:
			if !ok {
				if len(s.buffer) > 0 {
					s.sendLogs()
				}
				return
			}
			s.buffer = append(s.buffer, logEntry)
			if len(s.buffer) >= s.maxBatch {
				s.sendLogs()
			}
		}
	}
}
func (s *LokiSender) sendLogs() {
	s.logger.Info("flushing buffer", "count", len(s.buffer))
	bodyBytes := s.buildPayload()
	if bodyBytes == nil {
		s.buffer = s.buffer[:0]
		return
	}
	if err := s.sendWithRetry(bodyBytes); err != nil {
		s.logger.Error("failed to send after retries, dropping batch",
			"count", len(s.buffer),
			"err", err,
		)
	} else {
		s.logger.Info("successfully pushed logs to Loki", "count", len(s.buffer))
	}
	s.buffer = s.buffer[:0]
}
func (s *LokiSender) sendWithRetry(body []byte) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<attempt) * time.Second // 2s, 4s
			s.logger.Warn("retrying send",
				"attempt", attempt,
				"wait", wait,
			)
			time.Sleep(wait)
		}

		lastErr = s.doRequest(body)
		if lastErr == nil {
			return nil // thành công
		}
		s.logger.Error("send attempt failed", "attempt", attempt, "err", lastErr)
	}

	return errors.New("max retries exceeded: " + lastErr.Error())
}
func (s *LokiSender) doRequest(body []byte) error {
	req, err := http.NewRequest("POST", s.lokiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return errors.New("loki returned status: " + strconv.Itoa(resp.StatusCode))
	}
	return nil
}
func (s *LokiSender) buildPayload() []byte {
	groups := make(map[string]*LokiStream)
	for _, entry := range s.buffer {
		pod := entry.Labels["pod"]
		if pod == "" {
			pod = "unknown"
		}
		if _, exists := groups[pod]; !exists {
			streamLabels := make(map[string]string)
			for k, v := range entry.Labels {
				streamLabels[k] = v
			}
			groups[pod] = &LokiStream{
				Stream: streamLabels,
				Values: [][]string{},
			}
		}
		timestampStr := strconv.FormatInt(entry.Timestamp.UnixNano(), 10)
		groups[pod].Values = append(groups[pod].Values, []string{
			timestampStr,
			entry.Message,
		})
	}
	var streams []LokiStream
	for _, stream := range groups {
		streams = append(streams, *stream)
	}
	payload := LokiPushRequest{Streams: streams}
	data, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("failed to marshal payload", "err", err)
		return nil
	}
	return data
}
