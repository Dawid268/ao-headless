// Package agentbrowser gives a cloud session's coding agent a browser the
// person watches and uses in AO's Browser panel. The browser service (package
// host) runs as a child of the worker, started on first use; the agent reaches
// it on loopback and the desktop through the control plane, over sockets the
// worker dials out (relay.go).
package agentbrowser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// HostCommand is the worker subcommand that runs the browser service: the
// worker re-executes itself with it rather than ship a second binary.
const HostCommand = "agent-browser-host"

const (
	startTimeout = time.Minute
	// The service closes Chromium within 5 s of SIGTERM, then exits.
	stopGrace = 6 * time.Second
)

var readyClient = &http.Client{Timeout: 2 * time.Second}

// Service is the session's agent browser: one browser service, started on
// first use and again after it exits, and stopped when Run's context ends.
type Service struct {
	token  string       // the service's token, the agent's RB_TOKEN
	agent  net.Listener // the agent's loopback listener, its RB_URL
	logger *slog.Logger
	// start launches the service and returns once it answers; tests fake it.
	start func(ctx context.Context) (*instance, error)

	startMu sync.Mutex // one start at a time
	current atomic.Pointer[instance]
}

// instance is one run of the browser service.
type instance struct {
	url  *url.URL
	done <-chan struct{} // closed once it has exited
}

// New prepares the agent browser with its files in dir. Nothing starts until
// the agent or the desktop uses it. It fails when the sandbox has no Chromium
// or agent-browser.
func New(dir string, logger *slog.Logger) (*Service, error) {
	chrome, err := find("chromium", "/usr/bin/chromium")
	if err != nil {
		return nil, err
	}
	ab, err := find("agent-browser", "/usr/local/bin/agent-browser")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	agent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var token [32]byte
	_, _ = rand.Read(token[:])
	s := &Service{token: hex.EncodeToString(token[:]), agent: agent, logger: logger}
	s.start = func(ctx context.Context) (*instance, error) { return s.startHost(ctx, dir, chrome, ab) }
	return s, nil
}

// find is the binary name on PATH, else fallback.
func find(name, fallback string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		path, err = exec.LookPath(fallback)
	}
	return path, err
}

// Env is how the agent reaches the browser: RB_URL, its loopback listener, and
// RB_TOKEN, the service's token.
func (s *Service) Env() map[string]string {
	return map[string]string{"RB_URL": "http://" + s.agent.Addr().String(), "RB_TOKEN": s.token}
}

// Run serves the agent's listener and the control plane's relay until ctx
// ends, then waits for the browser service to exit.
func (s *Service) Run(ctx context.Context, cp ControlPlane) {
	agent := &http.Server{Handler: s.agentHandler(ctx)}
	go func() { _ = agent.Serve(s.agent) }()
	streams := newStreamListener()
	relay := &http.Server{Handler: s.relayHandler(ctx)}
	go func() { _ = relay.Serve(streams) }()
	go s.relayLoop(ctx, cp, streams)
	<-ctx.Done()
	_ = agent.Close()
	_ = relay.Close()
	// A start under way fails now that ctx is done; a started service got
	// SIGTERM with it.
	s.startMu.Lock()
	inst := s.current.Load()
	s.startMu.Unlock()
	if inst != nil {
		<-inst.done
	}
}

// live is the running service, if any.
func (s *Service) live() *instance {
	inst := s.current.Load()
	if inst == nil {
		return nil
	}
	select {
	case <-inst.done:
		return nil
	default:
		return inst
	}
}

// ensure returns the running service's URL, starting it if it is not running.
// Services it starts live until ctx ends.
func (s *Service) ensure(ctx context.Context) (*url.URL, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if inst := s.live(); inst != nil {
		return inst.url, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inst, err := s.start(ctx)
	if err != nil {
		s.logger.Warn("start agent browser", "error", err)
		return nil, err
	}
	s.current.Store(inst)
	return inst.url, nil
}

// startHost runs the browser service, this binary under HostCommand, and
// waits until it answers. When ctx ends it gets SIGTERM, which lets it close
// Chromium cleanly, and is killed with its process group stopGrace later.
func (s *Service) startHost(ctx context.Context, dir, chrome, ab string) (*instance, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// Free ports, not fixed ones: dev servers in the sandbox use 8080 and 9222.
	// ponytail: another process may take a port before the service binds it;
	// that start fails and the next request starts again on new ports.
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	cdpPort, err := freePort()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, self, HostCommand,
		"-addr", addr, "-data", dir, "-chrome", chrome, "-ab", ab, "-cdp-port", strconv.Itoa(cdpPort))
	// The token goes in the environment: any process can read argv.
	cmd.Env = append(os.Environ(), "RB_TOKEN="+s.token)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	dieWithWorker(cmd.SysProcAttr)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stopGrace
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		// Chromium and agent-browser, if the service died without closing them.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cancel()
		close(done)
	}()
	inst := &instance{url: &url.URL{Scheme: "http", Host: addr}, done: done}
	if err := s.waitReady(ctx, inst); err != nil {
		cancel()
		<-done
		return nil, err
	}
	return inst, nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// waitReady returns once the service answers with its token.
func (s *Service) waitReady(ctx context.Context, inst *instance) error {
	stats := inst.url.JoinPath("stats")
	stats.RawQuery = url.Values{"token": {s.token}}.Encode()
	deadline := time.After(startTimeout)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, stats.String(), nil)
		if err != nil {
			return err
		}
		if response, err := readyClient.Do(request); err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-inst.done:
			return errors.New("the browser service exited while starting")
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("the browser service did not answer within %s", startTimeout)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// agentHandler passes the agent's requests through to the service unchanged,
// starting it first if need be. The agent sends the token itself.
func (s *Service) agentHandler(ctx context.Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, err := s.ensure(ctx)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "the browser did not start: " + err.Error()})
			return
		}
		proxy := &httputil.ReverseProxy{
			Rewrite:      func(req *httputil.ProxyRequest) { req.SetURL(target) },
			ErrorHandler: unavailable,
		}
		proxy.ServeHTTP(w, r)
	})
}

// downloadGUID matches the browser's download GUIDs. Anything else, "." and
// ".." above all, reaches a path the browser's mux cleans with a redirect that
// repeats the query, token included.
var downloadGUID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// relayRoute reports whether the desktop may reach this path: the stream
// viewer's page, its socket, and its file transfers. The agent's command
// route and the browser's debugging routes stay in the sandbox.
func relayRoute(tail string) bool {
	switch tail {
	case "v/cdp", "ws/cdp", "stats", "upload":
		return true
	}
	guid, ok := strings.CutPrefix(tail, "download/")
	return ok && downloadGUID.MatchString(guid)
}

// relayHandler serves the desktop's requests, which arrive from the control
// plane, as the daemon's agent browser route does on a self-hosted host. The
// service's token is added here: the desktop never holds it.
func (s *Service) relayHandler(ctx context.Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tail := strings.TrimPrefix(r.URL.Path, "/")
		if !relayRoute(tail) {
			http.NotFound(w, r)
			return
		}
		// The desktop probes stats to decide whether to show an agent tab: a
		// probe must not start a browser nobody has used.
		if tail == "stats" && s.live() == nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"running":false}`)
			return
		}
		target, err := s.ensure(ctx)
		if err != nil {
			unavailable(w, r, err)
			return
		}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(req *httputil.ProxyRequest) {
				req.SetURL(target)
				req.Out.URL.Path = "/" + tail
				req.Out.URL.RawPath = ""
				query := req.In.URL.Query()
				query.Set("token", s.token)
				req.Out.URL.RawQuery = query.Encode()
				// AO's credentials and cookies are not the browser's. The
				// desktop's origin is not the browser's either, whose socket
				// check would refuse the stream.
				for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Origin", "X-AO-Browser-Capability"} {
					req.Out.Header.Del(name)
				}
			},
			// A redirect repeats the request's query, the token with it: keep
			// the token out of the Location and drop the body, which links it.
			ModifyResponse: func(res *http.Response) error {
				if location, err := url.Parse(res.Header.Get("Location")); err == nil && location.Query().Has("token") {
					query := location.Query()
					query.Del("token")
					location.RawQuery = query.Encode()
					res.Header.Set("Location", location.String())
					_ = res.Body.Close()
					res.Body = http.NoBody
					res.ContentLength = 0
					res.Header.Del("Content-Length")
				}
				return nil
			},
			ErrorHandler: unavailable,
		}
		proxy.ServeHTTP(w, r)
	})
}

func unavailable(w http.ResponseWriter, _ *http.Request, _ error) {
	http.Error(w, "Agent browser unavailable", http.StatusBadGateway)
}
