package pangolin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Resource represents a Pangolin proxy resource
type Resource struct {
	ID        string            `json:"id,omitempty"`
	Name      string            `json:"name"`
	Subdomain string            `json:"subdomain"`
	Domain    string            `json:"domain,omitempty"`
	Type      string            `json:"type"` // "http", "tcp", "udp"
	Enabled   bool              `json:"enabled"`
	SiteID    string            `json:"site_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Target represents a backend target for a resource
type Target struct {
	ID         string            `json:"id,omitempty"`
	ResourceID string            `json:"resource_id,omitempty"`
	Host       string            `json:"host"`
	Port       int               `json:"port"`
	Method     string            `json:"method,omitempty"` // "http", "https", "h2c"
	Weight     int               `json:"weight,omitempty"`
	Enabled    bool              `json:"enabled"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ResourceRule represents a path-based routing rule
type ResourceRule struct {
	ID         string `json:"id,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
	Path       string `json:"path"`
	PathType   string `json:"path_type"` // "prefix", "exact", "regex"
	TargetID   string `json:"target_id"`
	Priority   int    `json:"priority,omitempty"`
}

// CreateResourceRequest represents the request to create a resource
type CreateResourceRequest struct {
	Name      string            `json:"name"`
	Subdomain string            `json:"subdomain"`
	Domain    string            `json:"domain,omitempty"`
	Type      string            `json:"type"`
	Enabled   bool              `json:"enabled"`
	SiteID    string            `json:"site_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// CreateTargetRequest represents the request to create a target
type CreateTargetRequest struct {
	ResourceID string            `json:"resource_id"`
	Host       string            `json:"host"`
	Port       int               `json:"port"`
	Method     string            `json:"method,omitempty"`
	Weight     int               `json:"weight,omitempty"`
	Enabled    bool              `json:"enabled"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ListResourcesResponse represents the response from listing resources
type ListResourcesResponse struct {
	Resources []Resource `json:"resources"`
}

// ListTargetsResponse represents the response from listing targets
type ListTargetsResponse struct {
	Targets []Target `json:"targets"`
}

// Site represents a Pangolin site (proxy location)
type Site struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	ProxyIP   string `json:"proxy_ip"`
	ProxyIPv6 string `json:"proxy_ipv6,omitempty"`
	Region    string `json:"region,omitempty"`
	Enabled   bool   `json:"enabled"`
}

// CreateResource creates a new resource in Pangolin proxy
func (c *Client) CreateResource(ctx context.Context, req *CreateResourceRequest) (*Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/resources", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var resource Resource
	if err := json.Unmarshal(body, &resource); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resource, nil
}

// GetResource retrieves a resource by ID
func (c *Client) GetResource(ctx context.Context, resourceID string) (*Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/resources/%s", resourceID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var resource Resource
	if err := json.Unmarshal(body, &resource); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resource, nil
}

// ListResources lists all resources
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/v1/resources", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var listResp ListResourcesResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return listResp.Resources, nil
}

// UpdateResource updates an existing resource
func (c *Client) UpdateResource(ctx context.Context, resourceID string, req *CreateResourceRequest) (*Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodPut, fmt.Sprintf("/v1/resources/%s", resourceID), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var resource Resource
	if err := json.Unmarshal(body, &resource); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resource, nil
}

// DeleteResource deletes a resource by ID
func (c *Client) DeleteResource(ctx context.Context, resourceID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/v1/resources/%s", resourceID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return checkResponse(resp)
}

// CreateTarget creates a new target for a resource
func (c *Client) CreateTarget(ctx context.Context, req *CreateTargetRequest) (*Target, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/v1/targets", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var target Target
	if err := json.Unmarshal(body, &target); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &target, nil
}

// ListTargets lists all targets for a resource
func (c *Client) ListTargets(ctx context.Context, resourceID string) ([]Target, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/resources/%s/targets", resourceID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var listResp ListTargetsResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return listResp.Targets, nil
}

// DeleteTarget deletes a target by ID
func (c *Client) DeleteTarget(ctx context.Context, targetID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/v1/targets/%s", targetID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return checkResponse(resp)
}

// GetSite retrieves site information by ID
func (c *Client) GetSite(ctx context.Context, siteID string) (*Site, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/sites/%s", siteID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var site Site
	if err := json.Unmarshal(body, &site); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &site, nil
}

// ListSites lists all available sites
func (c *Client) ListSites(ctx context.Context) ([]Site, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/v1/sites", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var sites struct {
		Sites []Site `json:"sites"`
	}
	if err := json.Unmarshal(body, &sites); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return sites.Sites, nil
}
