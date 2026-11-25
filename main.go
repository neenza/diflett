package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

//go:embed web/*
var assets embed.FS

const (
	maxUntrackedFileBytes  = 128 * 1024
	maxUntrackedTotalBytes = 512 * 1024
)

type untrackedFile struct {
	Path      string
	Content   string `json:",omitempty"`
	Size      int64  `json:",omitempty"`
	Binary    bool   `json:",omitempty"`
	Truncated bool   `json:",omitempty"`
	Note      string `json:",omitempty"`
}

type snapshot struct {
	Branch    string
	Staged    string
	Unstaged  string
	Untracked []untrackedFile
	Error     string
}

type project struct {
	Name, Root   string
	mu           sync.RWMutex
	data         []byte
	version      string
	changed      chan struct{}
	contextBlobs map[string]bool
}

type limitedBuffer struct {
	buf     bytes.Buffer
	limit   int
	clipped bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n > b.limit {
		p = p[:b.limit]
		b.clipped = true
	}
	b.buf.Write(p)
	b.limit -= len(p)
	return n, nil
}
func git(ctx context.Context, root string, args ...string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", root}, args...)...)
	out := &limitedBuffer{limit: 256 * 1024}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("%s: %w", strings.TrimSpace(stderr.buf.String()), err)
	}
	return out.buf.String(), out.clipped, nil
}
func readUntrackedFile(root, relPath string, budget int) (untrackedFile, int) {
	fullPath := filepath.Join(root, filepath.FromSlash(relPath))
	info, err := os.Lstat(fullPath)
	if err != nil {
		return untrackedFile{Path: relPath, Note: err.Error()}, 0
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return untrackedFile{Path: relPath, Note: err.Error()}, 0
		}
		return untrackedFile{Path: relPath, Note: "Symbolic link -> " + target}, 0
	}
	if !info.Mode().IsRegular() {
		return untrackedFile{Path: relPath, Note: "Non-regular file"}, 0
	}
	if budget <= 0 {
		return untrackedFile{Path: relPath, Note: "[Content omitted: 512 KiB untracked limit reached]"}, 0
	}
	limit := min(maxUntrackedFileBytes, budget)
	f, err := os.Open(fullPath)
	if err != nil {
		return untrackedFile{Path: relPath, Note: err.Error()}, 0
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return untrackedFile{Path: relPath, Note: err.Error()}, 0
	}
	truncated := len(buf) > limit
	if truncated {
		buf = buf[:limit]
	}
	if bytes.IndexByte(buf, 0) >= 0 {
		return untrackedFile{Path: relPath, Binary: true, Size: info.Size()}, 0
	}
	if truncated && !utf8.Valid(buf) {
		for i := 1; i <= 3 && i <= len(buf); i++ {
			if utf8.Valid(buf[:len(buf)-i]) {
				buf = buf[:len(buf)-i]
				break
			}
		}
	}
	return untrackedFile{Path: relPath, Content: string(buf), Truncated: truncated}, len(buf)
}

func scan(ctx context.Context, root string) (s snapshot) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	branch, _, err := git(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		branch, _, err = git(ctx, root, "symbolic-ref", "--short", "HEAD")
	} else if strings.TrimSpace(branch) == "HEAD" {
		branch, _, err = git(ctx, root, "rev-parse", "--short", "HEAD")
	}
	if err != nil {
		s.Error = err.Error()
		return
	}
	s.Branch = strings.TrimSpace(branch)
	diff := []string{"diff", "--patch", "--full-index", "--unified=3", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/"}
	for _, staged := range []bool{false, true} {
		args := append([]string{}, diff...)
		if staged {
			args = append(args, "--cached")
		}
		patch, clipped, err := git(ctx, root, append(args, "--")...)
		if err != nil {
			s.Error = err.Error()
			return
		}
		if clipped {
			patch += "\n[Diff truncated at 256 KiB]"
		}
		if staged {
			s.Staged = patch
		} else {
			s.Unstaged = patch
		}
	}
	names, clipped, err := git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		s.Error = err.Error()
		return
	}
	if end := strings.LastIndexByte(names, 0); end >= 0 {
		budget := maxUntrackedTotalBytes
		for _, name := range strings.Split(names[:end], "\x00") {
			file, used := readUntrackedFile(root, name, budget)
			budget -= used
			s.Untracked = append(s.Untracked, file)
		}
	}
	if clipped {
		s.Untracked = append(s.Untracked, untrackedFile{Path: "[File list truncated at 256 KiB]", Note: "Additional untracked files were omitted."})
	}
	return
}
func (p *project) refresh(ctx context.Context) {
	state := scan(ctx, p.Root)
	data, _ := json.Marshal(state)
	version := fmt.Sprintf("%x", sha256.Sum256(data))
	p.mu.Lock()
	defer p.mu.Unlock()
	if version != p.version {
		p.contextBlobs = patchBlobs(state)
		p.data = data
		p.version = version
		close(p.changed)
		p.changed = make(chan struct{})
	}
}
func (p *project) watch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		p.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (p *project) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	last := ""
	for {
		p.mu.RLock()
		data, version, changed := p.data, p.version, p.changed
		p.mu.RUnlock()
		if version != last {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			last = version
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
		}
	}
}
func main() {
	port := flag.Int("port", 8787, "loopback HTTP port")
	interval := flag.Duration("interval", 2*time.Second, "repository polling interval")
	flag.Parse()
	if *interval <= 0 {
		log.Fatal("interval must be positive")
	}
	paths := flag.Args()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var mu sync.RWMutex
	projects := []*project{}
	addProject := func(requestCtx context.Context, path string) (int, error) {
		checkCtx, cancel := context.WithTimeout(requestCtx, 5*time.Second)
		defer cancel()
		root, _, err := git(checkCtx, path, "rev-parse", "--show-toplevel")
		if err != nil {
			return -1, err
		}
		root = strings.TrimSuffix(root, "\n")
		mu.Lock()
		defer mu.Unlock()
		for i, p := range projects {
			if p.Root == root {
				return i, nil
			}
		}
		p := &project{Name: filepath.Base(root), Root: root, changed: make(chan struct{})}
		projects = append(projects, p)
		go p.watch(ctx, *interval)
		return len(projects) - 1, nil
	}
	if len(paths) == 0 {
		if _, err := addProject(ctx, "."); err != nil && errors.Is(err, exec.ErrNotFound) {
			log.Fatalf("git: %v", err)
		}
	} else {
		for _, path := range paths {
			if _, err := addProject(ctx, path); err != nil {
				log.Fatalf("%s: %v", path, err)
			}
		}
	}
	mux := http.NewServeMux()
	web, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(web)))
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.RLock()
		list := slices.Clone(projects)
		mu.RUnlock()
		json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "Local access only", 403)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "Expected application/json", 415)
			return
		}
		var input struct{ Path string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&input); err != nil || !filepath.IsAbs(input.Path) {
			http.Error(w, "Enter an absolute repository path.", 400)
			return
		}
		id, err := addProject(r.Context(), input.Path)
		if err != nil {
			http.Error(w, "Could not open a Git working tree at that path.", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(id)
	})
	withProject := func(handler func(*project, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.Atoi(r.PathValue("id"))
			mu.RLock()
			if err != nil || id < 0 || id >= len(projects) {
				mu.RUnlock()
				http.NotFound(w, r)
				return
			}
			p := projects[id]
			mu.RUnlock()
			handler(p, w, r)
		}
	}
	mux.HandleFunc("GET /api/events/{id}", withProject((*project).stream))
	mux.HandleFunc("GET /api/context/{id}/{blob}", withProject((*project).serveContext))
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	server := http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if host != "localhost" && host != "127.0.0.1" {
			http.Error(w, "Local access only", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; object-src 'none'")
		mux.ServeHTTP(w, r)
	})}
	go func() { <-ctx.Done(); server.Close() }()
	log.Printf("Watching %d repositories at http://%s", len(projects), listener.Addr())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
