package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

//go:embed web
var webFS embed.FS

const (
	holdingTimeout = 5 * time.Second
	maxBodyBytes   = 64 << 10
)

type holdingRegisterJSON struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Unit        string       `json:"unit"`
	Type        RegisterType `json:"type"`
	Min         *float64     `json:"min"`
	Max         *float64     `json:"max"`
	Value       *float64     `json:"value"`
}

type holdingResponseJSON struct {
	Device    string                `json:"device"`
	Host      string                `json:"host"`
	Registers []holdingRegisterJSON `json:"registers,omitempty"`
	Error     string                `json:"error,omitempty"`
}

type holdingWriteJSON struct {
	Values map[string]float64 `json:"values"`
}

type server struct {
	device   string
	host     string
	regs     []*Register
	requests chan<- holdingRequest
}

func newServer(cfg *Config, requests chan<- holdingRequest) *server {
	s := &server{device: cfg.Device, host: cfg.Address(), requests: requests}
	for i := range cfg.Registers {
		if cfg.Registers[i].Kind == KindHolding {
			s.regs = append(s.regs, &cfg.Registers[i])
		}
	}
	return s
}

func startServer(cfg *Config, requests chan<- holdingRequest) (*http.Server, string, error) {
	ln, err := net.Listen("tcp", cfg.HTTPListen)
	if err != nil {
		return nil, "", err
	}
	srv := &http.Server{Handler: newServer(cfg, requests).handler()}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()
	return srv, localURL(ln.Addr()), nil
}

func localURL(addr net.Addr) string {
	host := "localhost"
	port := ""
	if tcp, ok := addr.(*net.TCPAddr); ok {
		port = strconv.Itoa(tcp.Port)
		if !tcp.IP.IsUnspecified() {
			host = tcp.IP.String()
		}
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func (s *server) handler() http.Handler {
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/holding", s.getHolding)
	mux.HandleFunc("POST /api/holding", s.postHolding)
	return mux
}

func (s *server) getHolding(w http.ResponseWriter, r *http.Request) {
	resp, err := s.do(r.Context(), nil)
	s.reply(w, resp, err)
}

func (s *server) postHolding(w http.ResponseWriter, r *http.Request) {
	var body holdingWriteJSON
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		s.fail(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if len(body.Values) == 0 {
		s.fail(w, http.StatusBadRequest, "no values to write")
		return
	}

	known := make(map[string]bool, len(s.regs))
	for _, reg := range s.regs {
		known[reg.Name] = true
	}
	for name := range body.Values {
		if !known[name] {
			s.fail(w, http.StatusBadRequest, fmt.Sprintf("unknown holding register %q", name))
			return
		}
	}

	var writes []holdingWrite
	for _, reg := range s.regs {
		v, ok := body.Values[reg.Name]
		if !ok {
			continue
		}
		if err := reg.checkValue(v); err != nil {
			s.fail(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", reg.Name, err))
			return
		}
		writes = append(writes, holdingWrite{reg: reg, value: v})
	}

	resp, err := s.do(r.Context(), writes)
	s.reply(w, resp, err)
}

func (s *server) do(ctx context.Context, writes []holdingWrite) (holdingResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, holdingTimeout)
	defer cancel()

	req := holdingRequest{writes: writes, reply: make(chan holdingResponse, 1)}
	select {
	case s.requests <- req:
	case <-ctx.Done():
		return holdingResponse{}, errors.New("collector is busy, request not sent")
	}
	select {
	case resp := <-req.reply:
		return resp, nil
	case <-ctx.Done():
		return holdingResponse{}, errors.New("timeout waiting for the device, result unknown")
	}
}

func (s *server) reply(w http.ResponseWriter, resp holdingResponse, err error) {
	if err != nil {
		s.fail(w, http.StatusGatewayTimeout, err.Error())
		return
	}
	out := holdingResponseJSON{Device: s.device, Host: s.host, Registers: make([]holdingRegisterJSON, 0, len(s.regs))}
	for _, reg := range s.regs {
		item := holdingRegisterJSON{
			Name:        reg.Name,
			Description: reg.Description,
			Unit:        reg.Unit,
			Type:        reg.Type,
			Min:         reg.Min,
			Max:         reg.Max,
		}
		if v, ok := resp.values[reg.Name]; ok {
			item.Value = &v
		}
		out.Registers = append(out.Registers, item)
	}
	status := http.StatusOK
	if resp.err != nil {
		out.Error = resp.err.Error()
		status = http.StatusBadGateway
	}
	writeJSON(w, status, out)
}

func (s *server) fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, holdingResponseJSON{Device: s.device, Host: s.host, Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
