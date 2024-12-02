package webra

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	_ "embed"

	"golang.org/x/sync/errgroup"
)

//go:embed index.html
var indexHTML []byte

var NoLabelGeneratedError = errors.New("no label generated")

const labelDelimiter = "^XZ"

type Server struct {
	Logger             *slog.Logger
	HTTPAddr           string
	TCPAddr            string
	DelayBetweenPrints time.Duration
	labelsCh           chan string
	clients            map[chan string]struct{} // track connected clients
	clientsMutex       sync.Mutex
}

var DefaultServer = &Server{}

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

	server.labelsCh = make(chan string)
	server.clients = make(map[chan string]struct{})
}

// Add a new client to the clients map
func (server *Server) addClient(client chan string) {
	server.clientsMutex.Lock()
	defer server.clientsMutex.Unlock()

	server.clients[client] = struct{}{}
}

// Remove a client from the clients map
func (server *Server) removeClient(client chan string) {
	server.clientsMutex.Lock()
	defer server.clientsMutex.Unlock()

	delete(server.clients, client)
	close(client)
}

// Broadcast label to all connected clients
func (server *Server) broadcastBase64Image(base64Image string) {
	server.clientsMutex.Lock()
	defer server.clientsMutex.Unlock()

	for client := range server.clients {
		client <- base64Image
	}
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

	http.HandleFunc("/sse", server.serveSSE)

	logger.Info("serving fake printer webpage")

	err := http.ListenAndServe(server.HTTPAddr, nil)
	if err != nil {
		return fmt.Errorf("listen and serve webra fake printer: %w", err)
	}

	return nil
}

func (server *Server) serveSSE(w http.ResponseWriter, r *http.Request) {
	logger := server.Logger.With("service", "http_handler")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	err := writeEvent(w, "open", nil)
	if err != nil {
		logger.Error(err.Error())
		return
	}

	// Create a new channel for the client
	client := make(chan string)
	server.addClient(client)

	// Remove the client channel when the connection is closed
	defer server.removeClient(client)

	for {
		select {
		case <-r.Context().Done():
			return
		case base64Image := <-client:
			err := writeEvent(w, "message", []byte(base64Image))
			if err != nil {
				logger.Error(err.Error())
				return
			}
		}
	}
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

		go server.handlePrinterConn(conn)
	}
}

func (server *Server) handlePrinterConn(conn net.Conn) {
	defer conn.Close()

	logger := server.Logger.With("service", "tcp_handler")
	reader := bufio.NewReader(conn)
	var labelBuilder strings.Builder

	for {
		str, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		labelBuilder.WriteString(str)

		if !strings.Contains(str, labelDelimiter) {
			continue
		}

		// resets buffer and builder when the zpl is complete
		parts := strings.Split(labelBuilder.String(), labelDelimiter)

		for i, part := range parts {
			if i < len(parts)-1 || (part != "" && part != "\n") {
				label := part + labelDelimiter

				// retrieve label from labelary
				base64Image, err := server.base64ImageFromLabel(label)
				if err != nil {
					if errors.Is(err, NoLabelGeneratedError) {
						logger.Warn("no label generated", "label", label)
						continue
					}

					logger.Error(err.Error(), "label", label)
					continue
				}

				logger.Info("label received", "label", label)
				server.broadcastBase64Image(base64Image)

				time.Sleep(server.DelayBetweenPrints)
			}
		}

		labelBuilder.Reset()
	}
}

func (server *Server) base64ImageFromLabel(label string) (string, error) {
	resp, err := http.Post("http://api.labelary.com/v1/printers/8dpmm/labels/4x6/0", "application/x-www-form-urlencoded", strings.NewReader(label))
	if err != nil {
		return "", fmt.Errorf("calling labelary: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return "", NoLabelGeneratedError
		}

		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	img, err := png.Decode(resp.Body)
	if err != nil {
		return "", fmt.Errorf("decoding png: %w", err)
	}

	buf := new(bytes.Buffer)

	err = png.Encode(buf, img)
	if err != nil {
		return "", fmt.Errorf("encoding png: %w", err)
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
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
