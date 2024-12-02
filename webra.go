package webra

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	_ "embed"

	"golang.org/x/sync/errgroup"
)

//go:embed index.html
var indexHTML []byte

type Server struct {
	// Logger is the logger used to give additional informations.
	// Default is a nop logger.
	Logger *slog.Logger
	// HTTPAddress is the address where the labels are going to be rendered.
	// Default is localhost:9101.
	HTTPAddr string
	// TCPAddress is the fake printer TCP address.
	// Default is localhost:9100.
	TCPAddr            string
	DelayBetweenPrints time.Duration
	printOrders        chan []byte
}

var DefaultServer = &Server{}

// init ensures server has reasonable defaults.
func (server *Server) init() {
	if server.Logger == nil {
		server.Logger = slog.Default()
	}

	if server.HTTPAddr == "" {
		server.HTTPAddr = ":9101"
	}

	if server.TCPAddr == "" {
		server.TCPAddr = ":9100"
	}

	if server.DelayBetweenPrints == 0 {
		server.DelayBetweenPrints = 600 * time.Millisecond
	}

	server.printOrders = make(chan []byte)
}

func (server *Server) ListenAndServe() error {
	server.init()

	errGrp := &errgroup.Group{}

	errGrp.Go(func() error {
		return server.runTCPListener()
	})

	errGrp.Go(func() error {
		return server.runHTTPListener()
	})

	return errGrp.Wait()
}

func (server *Server) runHTTPListener() error {
	logger := server.Logger.With("addr", server.HTTPAddr)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(indexHTML)
	})

	http.HandleFunc("/sse", server.renderZPL)

	logger.Info("serving fake printer webpage")

	err := http.ListenAndServe(server.HTTPAddr, nil)
	if err != nil {
		return fmt.Errorf("listen and serve webra fake printer: %w", err)
	}

	return nil
}

func (server *Server) runTCPListener() error {
	logger := server.Logger.With("addr", server.TCPAddr)

	zplListener, err := net.Listen("tcp", server.TCPAddr)
	if err != nil {
		return fmt.Errorf("listen fake printer messages: %w", err)
	}
	defer zplListener.Close()

	logger.Info("fake printer starts")

	for {
		conn, err := zplListener.Accept()
		if err != nil {
			return fmt.Errorf("accepting ZPL connection: %w", err)
		}

		go server.handleZPL(conn)
	}
}

func (server *Server) handleZPL(conn net.Conn) {
	logger := server.Logger.With("service", "tcp_handler")

	defer conn.Close()

	buf := make([]byte, 4096)

	n, err := conn.Read(buf)
	if err != nil {
		if err != io.EOF {
			logger.Error("reading zpl datal", "err", err.Error())
		}

		return
	}

	server.printOrders <- buf[:n]
}

func (server *Server) renderZPL(w http.ResponseWriter, r *http.Request) {
	logger := server.Logger.With("service", "http_handler")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	err := writeEvent(w, "open", nil)
	if err != nil {
		logger.Error(err.Error())
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case label := <-server.printOrders:
			err = server.writeLabel(w, string(label))
			if err != nil {
				logger.Error(err.Error(), "label", label)
				return
			}
		}
	}
}

func (server *Server) writeLabel(w http.ResponseWriter, label string) error {
	resp, err := http.Post("http://api.labelary.com/v1/printers/8dpmm/labels/4x6/0", "application/x-www-form-urlencoded", strings.NewReader(label))
	if err != nil {
		return fmt.Errorf("calling labelary: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	img, err := png.Decode(resp.Body)
	if err != nil {
		return fmt.Errorf("decoding png: %w", err)
	}

	buf := new(bytes.Buffer)

	err = png.Encode(buf, img)
	if err != nil {
		return fmt.Errorf("encoding png: %w", err)
	}

	base64EncodedImage := base64.StdEncoding.EncodeToString(buf.Bytes())

	err = writeEvent(w, "message", []byte(base64EncodedImage))
	if err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	// short delay before sending next label to client renderer
	time.Sleep(server.DelayBetweenPrints)

	return nil
}

func writeEvent(w http.ResponseWriter, message string, data []byte) error {
	event := fmt.Sprintf("event: %s\n", message)

	if data != nil {
		event += fmt.Sprintf("data: %s\n", data)
	}

	event += "\n"

	_, err := w.Write([]byte(event))
	if err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	w.(http.Flusher).Flush()

	return nil
}
