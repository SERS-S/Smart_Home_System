package openclaw

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"smart-home/ai-assistant/internal/config"
	"smart-home/ai-assistant/internal/prompt"
	"smart-home/ai-assistant/internal/protocol"
)

type Client struct {
	baseURL, token string
	http           *http.Client
	ready          atomic.Bool
}

func New(c config.Config) *Client {
	return &Client{baseURL: "http://127.0.0.1:" + strconv.Itoa(c.GatewayPort), token: c.GatewayToken,
		http: &http.Client{Timeout: c.AgentTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Ready() bool         { return c.ready.Load() }
func (c *Client) SetReady(ready bool) { c.ready.Store(ready) }

func (c *Client) Probe(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("gateway is unavailable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 65536))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway readiness HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *Client) Complete(ctx context.Context, request protocol.Request) (string, error) {
	session := sha256.Sum256([]byte(request.SessionID))
	payload := struct {
		Model    string           `json:"model"`
		User     string           `json:"user"`
		Stream   bool             `json:"stream"`
		Messages []prompt.Message `json:"messages"`
	}{"openclaw/default", fmt.Sprintf("ivus:%x", session), false, prompt.Messages(request)}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("cannot encode gateway request")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.token)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("gateway request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1048577))
	if err != nil || len(body) > 1048576 {
		return "", fmt.Errorf("invalid gateway response size")
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Choices) == 0 {
		return "", fmt.Errorf("invalid gateway response")
	}
	var answer string
	if err := json.Unmarshal(result.Choices[0].Message.Content, &answer); err != nil {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(result.Choices[0].Message.Content, &parts); err != nil {
			return "", fmt.Errorf("missing final gateway text")
		}
		for _, part := range parts {
			if part.Type == "text" {
				answer += part.Text
			}
		}
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", fmt.Errorf("empty final gateway text")
	}
	return answer, nil
}

func ProbeUntil(ctx context.Context, client *Client) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := client.Probe(probeCtx)
		cancel()
		if err == nil {
			client.SetReady(true)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("gateway startup timeout")
		case <-tick.C:
		}
	}
}
