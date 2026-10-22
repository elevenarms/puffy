package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Embedder turns a query into a vector with the same model that embedded
// the knowledge base (nomic-embed-text-v1.5). It uses PUFFY_EMBED_URL when
// set; otherwise it starts llama-server on 127.0.0.1 the first time a
// search needs it and stops it when puffy exits. Nothing leaves the host.
type Embedder struct {
	settings Settings
	url      string
	process  *exec.Cmd
	failed   error
	once     sync.Once
	client   *http.Client
}

func newEmbedder(s Settings) *Embedder {
	return &Embedder{settings: s, url: s.EmbedURL,
		client: &http.Client{Timeout: 2 * time.Minute}}
}

func (e *Embedder) Embed(text string) ([]float32, error) {
	e.once.Do(func() {
		if e.url == "" {
			e.failed = e.start()
		}
	})
	if e.failed != nil {
		return nil, e.failed
	}
	body, _ := json.Marshal(map[string]any{"input": []string{text}})
	response, err := e.client.Post(e.url+"/v1/embeddings",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, errors.New("embedder returned no vector")
	}
	return normalize(result.Data[0].Embedding), nil
}

func (e *Embedder) start() error {
	if _, err := os.Stat(e.settings.EmbedModel); err != nil {
		return fmt.Errorf("embedding model %s not found; set "+
			"PUFFY_EMBED_MODEL", e.settings.EmbedModel)
	}
	server, err := exec.LookPath(e.settings.EmbedServer)
	if err != nil {
		return fmt.Errorf("%s not on PATH; pkg_add llama.cpp",
			e.settings.EmbedServer)
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	e.url = "http://127.0.0.1:" + strconv.Itoa(port)
	e.process = exec.Command(server, "-m", e.settings.EmbedModel,
		"--embedding", "--pooling", "mean", "-c", "2048",
		"-b", "2048", "-ub", "2048", "-np", "1",
		"--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--log-disable")
	// Own process group: Ctrl-C meant for a script must not kill it.
	e.process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := e.process.Start(); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		response, err := e.client.Get(e.url + "/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	e.close()
	return errors.New("llama-server did not become ready")
}

func (e *Embedder) close() {
	if e.process != nil && e.process.Process != nil {
		e.process.Process.Kill()
		e.process.Wait()
		e.process = nil
	}
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func normalize(vector []float32) []float32 {
	var sum float64
	for _, x := range vector {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return vector
	}
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / norm)
	}
	return vector
}
