package reader

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"time"
)

type RawLine struct {
	Content  string
	Filepath string
	Offset   int64
}

type Config struct {
	Filepath     string
	PollInterval time.Duration
	StateFile    string
}

type Tailer struct {
	cfg       Config
	out       chan<- RawLine
	logger    *slog.Logger
	file      *os.File
	offset    int64
	lastInode uint64
	lastSize  int64
}

type State struct {
	Offset int64  `json:"offset"`
	Inode  uint64 `json:"inode"`
}

// FIX 2: Không mở file trong constructor — để Run() tự mở và retry
func NewTailer(cfg Config, out chan<- RawLine, logger *slog.Logger) *Tailer {
	t := &Tailer{
		cfg:    cfg,
		out:    out,
		logger: logger.With("component", "Tailer"),
	}
	// Chỉ load state, không mở file
	state, err := t.LoadState()
	if err == nil {
		t.offset = state.Offset
		t.lastInode = state.Inode
	}
	return t
}

func (t *Tailer) OpenFile() error {
	f, err := os.Open(t.cfg.Filepath)
	if err != nil {
		return err
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	if info.Size() < t.lastSize {
		t.logger.Info("File truncated, resetting offset to 0")
		t.offset = 0
	}

	_, err = f.Seek(t.offset, 0)
	if err != nil {
		f.Close()
		return err
	}

	t.file = f
	t.lastSize = info.Size()

	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		t.lastInode = stat.Ino
	}

	return nil
}

// FIX 4: Check StateFile rỗng trước khi đọc
func (t *Tailer) LoadState() (*State, error) {
	if t.cfg.StateFile == "" {
		return &State{}, nil
	}
	data, err := os.ReadFile(t.cfg.StateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, err
	}

	var state State
	err = json.Unmarshal(data, &state)
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (t *Tailer) SaveState() error {
	if t.cfg.StateFile == "" {
		return nil
	}
	state := State{
		Offset: t.offset,
		Inode:  t.lastInode,
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(t.cfg.StateFile, data, 0644)
}

func (t *Tailer) Run(ctx context.Context) {
	t.logger.Info("tailer starting", "filepath", t.cfg.Filepath)

	// FIX 2: Mở file lần đầu trong Run(), có thể retry nếu thất bại
	if err := t.OpenFile(); err != nil {
		t.logger.Warn("initial open failed, will retry on next tick", "err", err)
	}

	ticker := time.NewTicker(t.cfg.PollInterval)
	defer ticker.Stop()
	defer func() {
		if t.file != nil {
			t.file.Close()
		}
	}()

	for {
		select {
		case <-ticker.C:
			// FIX 3: Thử mở lại nếu file nil
			if t.file == nil {
				if err := t.OpenFile(); err != nil {
					t.logger.Warn("file not ready, retrying next tick", "err", err)
					continue
				}
			}
			t.CheckFile()
			// FIX 3: Check nil trước khi đọc
			if t.file != nil {
				t.ReadNewLines()
			}
		case <-ctx.Done():
			t.logger.Info("tailer stopping")
			t.SaveState()
			return
		}
	}
}

func (t *Tailer) CheckFile() {
	info, err := os.Stat(t.cfg.Filepath)
	if err != nil {
		return
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	currentInode := stat.Ino

	if t.lastInode == 0 {
		t.lastInode = currentInode
	}

	if currentInode != t.lastInode {
		t.logger.Info("file rotation detected",
			"old_inode", t.lastInode,
			"new_inode", currentInode,
		)

		if t.file != nil {
			t.file.Close()
			t.file = nil
		}

		t.offset = 0
		t.lastSize = 0
		t.lastInode = currentInode

		if err := t.OpenFile(); err != nil {
			t.logger.Error("failed to open rotated file", "error", err)
		}
	}
}

func (t *Tailer) ReadNewLines() {
	buf := make([]byte, 4096)
	var remainder string

	currentOffset, err := t.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return
	}

	for {
		n, err := t.file.Read(buf)
		if n > 0 {
			content := remainder + string(buf[:n])
			lines := strings.Split(content, "\n")

			for i, line := range lines {
				if i == len(lines)-1 {
					remainder = line
				} else {
					// Cập nhật offset dù dòng rỗng hay không
					lineOffset := currentOffset + int64(len(line)+1)

					// FIX 1: Chỉ gửi nếu dòng không rỗng
					if line != "" {
						t.out <- RawLine{
							Content:  line,
							Filepath: t.cfg.Filepath,
							Offset:   lineOffset,
						}
					}
					currentOffset = lineOffset
				}
			}
			t.offset = currentOffset
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			t.logger.Error("failed to read file", "error", err)
			break
		}
	}

	if info, err := t.file.Stat(); err == nil {
		t.lastSize = info.Size()
	}

	t.SaveState()
}
