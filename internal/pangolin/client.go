package pangolin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultTimeout = 30 * time.Second
)

// Client represents a Pangolin API client
type Client struct {
	baseURL    string
	apiKey     string
	orgID      string
	httpClient *http.Client
}

// NewClient creates a new Pangolin API client
func NewClient(baseURL, apiKey, orgID string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		orgID:   orgID,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// OrgID returns the configured Pangolin organization identifier
func (c *Client) OrgID() string {
	return c.orgID
}

// doRequest performs an HTTP request with authentication
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		log.FromContext(ctx).V(1).Info("Pangolin API request", "method", method, "path", path, "body", string(jsonData))
		reqBody = bytes.NewBuffer(jsonData)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	return resp, nil
}

// checkResponse checks the HTTP response for errors
func checkResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
}
