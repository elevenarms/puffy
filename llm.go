package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is one OpenAI-style chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage is the token count the endpoint reports.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ChatClient talks to an OpenAI-compatible /chat/completions endpoint.
// It streams the reply and stops reading once a line is exactly "# end",
// so a model that keeps talking past its action costs nothing extra.
type ChatClient struct {
	base, key, model string
	maxTokens        int
	http             *http.Client
}

func newChatClient(s Settings) *ChatClient {
	return &ChatClient{
		base:      strings.TrimRight(s.APIBase, "/"),
		key:       s.APIKey,
		model:     s.Model,
		maxTokens: s.MaxTokens,
		http:      &http.Client{Timeout: 10 * time.Minute},
	}
}

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage          `json:"usage"`
	Model string          `json:"model"`
	Error json.RawMessage `json:"error"`
}

// Complete sends the conversation and returns the reply text. onText gets
// each piece of text as it arrives (for live display); it may be nil.
func (c *ChatClient) Complete(messages []Message, onText func(string),
	onNotice func(string)) (
	reply string, usage Usage, servedBy string, err error) {
	body, _ := json.Marshal(chatRequest{
		Model: c.model, Messages: messages,
		MaxTokens: c.maxTokens, Stream: true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	var response *http.Response
	for attempt := 0; ; attempt++ {
		response, err = c.post(body)
		if err == nil && response.StatusCode == http.StatusOK {
			break
		}
		retry := attempt < 4
		if isNetworkError(err) {
			retry = attempt < 2 // likely offline; do not wait long
		}
		if err == nil {
			detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			response.Body.Close()
			err = fmt.Errorf("%s: %s", response.Status,
				strings.TrimSpace(string(detail)))
			// 402: the router reserves credit for the worst case of
			// max_tokens. Ask for less instead of failing the run.
			if response.StatusCode == http.StatusPaymentRequired &&
				c.maxTokens > 1024 {
				c.maxTokens /= 2
				body, _ = json.Marshal(chatRequest{
					Model: c.model, Messages: messages,
					MaxTokens: c.maxTokens, Stream: true,
					StreamOptions: &streamOptions{IncludeUsage: true},
				})
				if onNotice != nil {
					onNotice(fmt.Sprintf("low credit: max_tokens now %d",
						c.maxTokens))
				}
				attempt--
				continue
			}
			retry = retry && (response.StatusCode == 429 ||
				response.StatusCode >= 500)
		}
		if !retry {
			return "", usage, "", err
		}
		time.Sleep(time.Duration(2<<attempt) * time.Second)
	}
	defer response.Body.Close()
	return readStream(response.Body, onText)
}

func (c *ChatClient) post(body []byte) (*http.Response, error) {
	request, err := http.NewRequest("POST", c.base+"/chat/completions",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("X-Title", "puffy")
	return c.http.Do(request)
}

// readStream reads server-sent events. It also accepts a plain JSON body,
// for endpoints that ignore "stream": true.
func readStream(body io.Reader, onText func(string)) (
	string, Usage, string, error) {
	var text strings.Builder
	var usage Usage
	var servedBy string
	line := ""
	emit := func(piece string) bool {
		text.WriteString(piece)
		if onText != nil {
			onText(piece)
		}
		// Stop at the end marker; the rest of the stream is not needed.
		line += piece
		for {
			cut := strings.IndexByte(line, '\n')
			if cut < 0 {
				break
			}
			if isEndLine(line[:cut]) {
				return true
			}
			line = line[cut+1:]
		}
		return false
	}

	reader := bufio.NewReaderSize(body, 64*1024)
	for {
		raw, err := reader.ReadString('\n')
		raw = strings.TrimRight(raw, "\r\n")
		if strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "{") {
			data := strings.TrimSpace(strings.TrimPrefix(raw, "data:"))
			if data == "[DONE]" {
				break
			}
			var chunk streamChunk
			if json.Unmarshal([]byte(data), &chunk) == nil {
				if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
					return text.String(), usage, servedBy,
						errors.New("endpoint error: " + string(chunk.Error))
				}
				if chunk.Usage != nil {
					usage = *chunk.Usage
				}
				if chunk.Model != "" {
					servedBy = chunk.Model
				}
				stop := false
				for _, choice := range chunk.Choices {
					piece := choice.Delta.Content + choice.Message.Content
					if piece != "" && emit(piece) {
						stop = true
					}
				}
				if stop {
					break
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return text.String(), usage, servedBy, err
		}
	}
	return text.String(), usage, servedBy, nil
}
