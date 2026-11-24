package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var indexLine = regexp.MustCompile("(?m)^index ([0-9a-f]{40}|[0-9a-f]{64})\\.\\.[0-9a-f]+(?: [0-7]+)?$")

func patchBlobs(s snapshot) map[string]bool {
	blobs := make(map[string]bool)
	for _, patch := range []string{s.Staged, s.Unstaged} {
		for _, match := range indexLine.FindAllStringSubmatch(patch, -1) {
			if strings.Trim(match[1], "0") != "" {
				blobs[match[1]] = true
			}
		}
	}
	return blobs
}

type contextPage struct {
	Lines []string `json:"lines"`
	More  bool     `json:"more"`
}

// Skip preceding lines without buffering them. Bound the size of each response.
func readContext(src io.Reader, start, count int) (contextPage, error) {
	page := contextPage{Lines: []string{}}
	reader := bufio.NewReader(src)
	var line strings.Builder
	size := 0
	for n := 1; ; {
		part, err := reader.ReadSlice('\n')
		if n >= start {
			if n-start == count {
				page.More = len(part) > 0
				return page, nil
			}
			size += len(part)
			if size > 512*1024 {
				return page, errors.New("context exceeds 512 KiB")
			}
			line.Write(part)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && err != io.EOF {
			return page, err
		}
		if n >= start && line.Len() > 0 {
			page.Lines = append(page.Lines, strings.TrimSuffix(line.String(), "\n"))
			line.Reset()
		}
		if err == io.EOF {
			return page, nil
		}
		n++
	}
}

func (p *project) serveContext(w http.ResponseWriter, r *http.Request) {
	blob := r.PathValue("blob")
	p.mu.RLock()
	allowed := p.contextBlobs[blob]
	p.mu.RUnlock()
	if !allowed {
		http.Error(w, "Diff changed. Refresh and try again.", http.StatusConflict)
		return
	}
	start, e1 := strconv.Atoi(r.URL.Query().Get("start"))
	count, e2 := strconv.Atoi(r.URL.Query().Get("count"))
	if e1 != nil || e2 != nil || start < 1 || count < 1 || count > 10 {
		http.Error(w, "Invalid line range", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", p.Root, "cat-file", "blob", blob)
	out, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "Cannot read context", 500)
		return
	}
	if err = cmd.Start(); err != nil {
		http.Error(w, "Cannot open context", 500)
		return
	}
	page, err := readContext(out, start, count)
	// A full page may leave unread bytes in the pipe.
	if err != nil || page.More {
		cancel()
	}
	waitErr := cmd.Wait()
	if err != nil || (!page.More && waitErr != nil) {
		http.Error(w, "Cannot read this context range", 422)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(page)
}
