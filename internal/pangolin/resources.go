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
	ID            int    `json:"resourceId"`
	GUID          string `json:"resourceGuid"`
	OrgID         string `json:"orgId"`
	NiceID        string `json:"niceId"`
	Name          string `json:"name"`
	Subdomain     string `json:"subdomain"`
	FullDomain    string `json:"fullDomain"`
	DomainID      string `json:"domainId"`
	HTTP          bool   `json:"http"`
	Protocol      string `json:"protocol"`
	Enabled       bool   `json:"enabled"`
	StickySession bool   `json:"stickySession"`
}

// Target represents a backend target for a resource
type Target struct {
	ID           int    `json:"targetId"`
	SiteID       int    `json:"siteId"`
	IP           string `json:"ip"`
	Method       string `json:"method"`
	Port         int    `json:"port"`
	Enabled      bool   `json:"enabled"`
	HealthStatus string `json:"healthStatus"`
}

// CreateResourceRequest represents the request to create a resource
type CreateResourceRequest struct {
	Name          string `json:"name"`
	Subdomain     string `json:"subdomain,omitempty"`
	HTTP          bool   `json:"http"`
	Protocol      string `json:"protocol"`
	DomainID      string `json:"domainId"`
	StickySession bool   `json:"stickySession,omitempty"`
	PostAuthPath  string `json:"postAuthPath,omitempty"`
}

// Header represents a custom proxy header
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// UpdateResourceRequest represents the request to update a resource
type UpdateResourceRequest struct {
	Name                  string   `json:"name,omitempty"`
	Subdomain             string   `json:"subdomain,omitempty"`
	DomainID              string   `json:"domainId,omitempty"`
	Enabled               *bool    `json:"enabled,omitempty"`
	SSO                   *bool    `json:"sso,omitempty"`
	SSL                   *bool    `json:"ssl,omitempty"`
	BlockAccess           *bool    `json:"blockAccess,omitempty"`
	EmailWhitelistEnabled *bool    `json:"emailWhitelistEnabled,omitempty"`
	ApplyRules            *bool    `json:"applyRules,omitempty"`
	StickySession         *bool    `json:"stickySession,omitempty"`
	TLSServerName         *string  `json:"tlsServerName,omitempty"`
	SetHostHeader         *string  `json:"setHostHeader,omitempty"`
	Headers               []Header `json:"headers,omitempty"`
	PostAuthPath          *string  `json:"postAuthPath,omitempty"`
	SkipToIdpID           *int     `json:"skipToIdpId,omitempty"`
}

// CreateTargetRequest represents the request to create a target
type CreateTargetRequest struct {
	SiteID              int      `json:"siteId"`
	IP                  string   `json:"ip"`
	Method              string   `json:"method,omitempty"`
	Port                int      `json:"port"`
	Enabled             bool     `json:"enabled"`
	Path                string   `json:"path,omitempty"`
	PathMatchType       string   `json:"pathMatchType,omitempty"`
	RewritePath         string   `json:"rewritePath,omitempty"`
	RewritePathType     string   `json:"rewritePathType,omitempty"`
	Priority            int      `json:"priority,omitempty"`
	HCEnabled           *bool    `json:"hcEnabled,omitempty"`
	HCPath              *string  `json:"hcPath,omitempty"`
	HCScheme            *string  `json:"hcScheme,omitempty"`
	HCMode              *string  `json:"hcMode,omitempty"`
	HCHostname          *string  `json:"hcHostname,omitempty"`
	HCPort              *int     `json:"hcPort,omitempty"`
	HCInterval          *int     `json:"hcInterval,omitempty"`
	HCUnhealthyInterval *int     `json:"hcUnhealthyInterval,omitempty"`
	HCTimeout           *int     `json:"hcTimeout,omitempty"`
	HCHeaders           []Header `json:"hcHeaders,omitempty"`
	HCFollowRedirects   *bool    `json:"hcFollowRedirects,omitempty"`
	HCMethod            *string  `json:"hcMethod,omitempty"`
	HCStatus            *int     `json:"hcStatus,omitempty"`
	HCTLSServerName     *string  `json:"hcTlsServerName,omitempty"`
}

// Site represents a Pangolin site (proxy location)
type Site struct {
	ID      int    `json:"siteId"`
	NiceID  string `json:"niceId"`
	Name    string `json:"name"`
	Address string `json:"address"`
	ProxyIP string `json:"proxyIp"`
	Online  bool   `json:"online"`
	Type    string `json:"type"`
}

// Domain represents a Pangolin domain
type Domain struct {
	ID         string `json:"domainId"`
	BaseDomain string `json:"baseDomain"`
}

// CreateResource creates a new resource in Pangolin proxy
func (c *Client) CreateResource(ctx context.Context, req *CreateResourceRequest) (*Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodPut, fmt.Sprintf("/v1/org/%s/resource", c.orgID), req)
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
	if err := decodeData(body, &resource); err != nil {
		return nil, err
	}

	return &resource, nil
}

// GetResource retrieves a resource by ID
func (c *Client) GetResource(ctx context.Context, resourceID string) (*Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/resource/%s", resourceID), nil)
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
	if err := decodeData(body, &resource); err != nil {
		return nil, err
	}

	return &resource, nil
}

// ListResources lists all resources for the configured organization
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/org/%s/resources", c.orgID), nil)
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

	var list struct {
		Resources []Resource `json:"resources"`
	}
	if err := decodeData(body, &list); err != nil {
		return nil, err
	}

	return list.Resources, nil
}

// UpdateResource updates an existing resource
func (c *Client) UpdateResource(ctx context.Context, resourceID string, req *UpdateResourceRequest) (*Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/resource/%s", resourceID), req)
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
	if err := decodeData(body, &resource); err != nil {
		return nil, err
	}

	return &resource, nil
}

// DeleteResource deletes a resource by ID
func (c *Client) DeleteResource(ctx context.Context, resourceID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/v1/resource/%s", resourceID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return checkResponse(resp)
}

// CreateTarget creates a new target for a resource
func (c *Client) CreateTarget(ctx context.Context, resourceID string, req *CreateTargetRequest) (*Target, error) {
	resp, err := c.doRequest(ctx, http.MethodPut, fmt.Sprintf("/v1/resource/%s/target", resourceID), req)
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
	if err := decodeData(body, &target); err != nil {
		return nil, err
	}

	return &target, nil
}

// UpdateTarget updates an existing target by ID
func (c *Client) UpdateTarget(ctx context.Context, targetID string, req *CreateTargetRequest) (*Target, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/target/%s", targetID), req)
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
	if err := decodeData(body, &target); err != nil {
		return nil, err
	}

	return &target, nil
}

// ListTargets lists all targets for a resource
func (c *Client) ListTargets(ctx context.Context, resourceID string) ([]Target, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/resource/%s/targets", resourceID), nil)
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

	var list struct {
		Targets []Target `json:"targets"`
	}
	if err := decodeData(body, &list); err != nil {
		return nil, err
	}

	return list.Targets, nil
}

// DeleteTarget deletes a target by ID
func (c *Client) DeleteTarget(ctx context.Context, targetID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/v1/target/%s", targetID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return checkResponse(resp)
}

// GetSite retrieves site information by ID
func (c *Client) GetSite(ctx context.Context, siteID string) (*Site, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/site/%s", siteID), nil)
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
	if err := decodeData(body, &site); err != nil {
		return nil, err
	}

	return &site, nil
}

// GetSiteByNiceID retrieves a site scoped to the organization using its nice ID
func (c *Client) GetSiteByNiceID(ctx context.Context, niceID string) (*Site, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/org/%s/site/%s", c.orgID, niceID), nil)
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
	if err := decodeData(body, &site); err != nil {
		return nil, err
	}

	return &site, nil
}

// ListSites lists all available sites for the organization
func (c *Client) ListSites(ctx context.Context) ([]Site, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/org/%s/sites", c.orgID), nil)
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
	if err := decodeData(body, &sites); err != nil {
		return nil, err
	}

	return sites.Sites, nil
}

// ListDomains lists all domains available to the organization
func (c *Client) ListDomains(ctx context.Context) ([]Domain, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/org/%s/domains", c.orgID), nil)
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

	var domains struct {
		Domains []Domain `json:"domains"`
	}
	if err := decodeData(body, &domains); err != nil {
		return nil, err
	}

	return domains.Domains, nil
}

// GetDomain retrieves a domain configuration by ID
func (c *Client) GetDomain(ctx context.Context, domainID string) (*Domain, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/org/%s/domain/%s", c.orgID, domainID), nil)
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

	var domain Domain
	if err := decodeData(body, &domain); err != nil {
		return nil, err
	}

	return &domain, nil
}

func decodeData(body []byte, target interface{}) error {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("failed to parse response envelope: %w", err)
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("response missing data field")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("failed to parse response data: %w", err)
	}
	return nil
}

// --- Auth sub-resource endpoints --------------------------------------------
//
// These endpoints are optional in older Pangolin versions; callers should
// treat *NotImplementedError (HTTP 404/405) as "skip this auth method".

// SetResourcePassword sets (password != nil) or clears (password == nil) the
// resource shared password. Pangolin requires 4..100 chars when setting.
func (c *Client) SetResourcePassword(ctx context.Context, resourceID string, password *string) error {
	body := struct {
		Password *string `json:"password"`
	}{Password: password}
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/resource/%s/password", resourceID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponseWithNotImplemented(resp)
}

// SetResourcePincode sets (pincode != nil) or clears (pincode == nil) the
// resource pincode. Pangolin requires exactly 6 digits when setting.
func (c *Client) SetResourcePincode(ctx context.Context, resourceID string, pincode *string) error {
	body := struct {
		Pincode *string `json:"pincode"`
	}{Pincode: pincode}
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/resource/%s/pincode", resourceID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponseWithNotImplemented(resp)
}

// GetResourceWhitelist returns the email entries currently on the resource's
// whitelist. Items are lowercased by Pangolin.
func (c *Client) GetResourceWhitelist(ctx context.Context, resourceID string) ([]string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/resource/%s/whitelist", resourceID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotImplemented(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var payload struct {
		Whitelist []struct {
			Email string `json:"email"`
		} `json:"whitelist"`
	}
	if err := decodeData(body, &payload); err != nil {
		return nil, err
	}
	emails := make([]string, 0, len(payload.Whitelist))
	for _, e := range payload.Whitelist {
		emails = append(emails, e.Email)
	}
	return emails, nil
}

// SetResourceWhitelist replaces the resource's email whitelist. Pangolin
// enforces max 50 entries and lowercases all values.
func (c *Client) SetResourceWhitelist(ctx context.Context, resourceID string, emails []string) error {
	if emails == nil {
		emails = []string{}
	}
	body := struct {
		Emails []string `json:"emails"`
	}{Emails: emails}
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/resource/%s/whitelist", resourceID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponseWithNotImplemented(resp)
}

// ListResourceRoles returns the role IDs currently assigned to the resource.
func (c *Client) ListResourceRoles(ctx context.Context, resourceID string) ([]int, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/resource/%s/roles", resourceID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotImplemented(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var payload struct {
		Roles []struct {
			RoleID int `json:"roleId"`
		} `json:"roles"`
	}
	if err := decodeData(body, &payload); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(payload.Roles))
	for _, r := range payload.Roles {
		ids = append(ids, r.RoleID)
	}
	return ids, nil
}

// SetResourceRoles replaces the resource's role assignments with the given
// integer role IDs.
func (c *Client) SetResourceRoles(ctx context.Context, resourceID string, roleIDs []int) error {
	if roleIDs == nil {
		roleIDs = []int{}
	}
	body := struct {
		RoleIDs []int `json:"roleIds"`
	}{RoleIDs: roleIDs}
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/resource/%s/roles", resourceID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponseWithNotImplemented(resp)
}

// ListResourceUsers returns the user IDs currently assigned to the resource.
func (c *Client) ListResourceUsers(ctx context.Context, resourceID string) ([]string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/resource/%s/users", resourceID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotImplemented(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var payload struct {
		Users []struct {
			UserID string `json:"userId"`
		} `json:"users"`
	}
	if err := decodeData(body, &payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Users))
	for _, u := range payload.Users {
		ids = append(ids, u.UserID)
	}
	return ids, nil
}

// SetResourceUsers replaces the resource's user assignments with the given
// Pangolin user ID strings.
func (c *Client) SetResourceUsers(ctx context.Context, resourceID string, userIDs []string) error {
	if userIDs == nil {
		userIDs = []string{}
	}
	body := struct {
		UserIDs []string `json:"userIds"`
	}{UserIDs: userIDs}
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/resource/%s/users", resourceID), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkResponseWithNotImplemented(resp)
}
