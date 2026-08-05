package pangolin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// siteResourcePath is the path segment for Pangolin's private (mesh-only)
// resources.
//
// Pangolin is mid-rename: `site-resource` and `private-resource` are live
// aliases for the same object, and both still take a siteResourceId path
// parameter. The old name is used here because it is the strictly wider
// compatibility set -- it exists in the current API and on older self-hosted
// builds, whereas the new alias may not. Switching is a one-line change.
const siteResourcePath = "site-resource"

// listPageSize is requested on paginated list endpoints. Pangolin defaults
// these to 20 items, which would silently hide principals from name
// resolution and turn "role 21" into a spurious "role not found".
const listPageSize = 500

// SiteResource is a Pangolin private resource: reachable only by clients
// connected to the mesh, addressed by an internal alias, with no public
// entrypoint.
//
// SPIKE (tasks 1.2): Pangolin's OpenAPI document types every 2xx body as a
// generic envelope with an untyped `data` object, so these field names are
// inferred from the documented *request* schemas and must be confirmed against
// a live instance. Decoding is confined to this file so a correction is local.
type SiteResource struct {
	ID              int    `json:"siteResourceId"`
	NiceID          string `json:"niceId"`
	Name            string `json:"name"`
	Mode            string `json:"mode"`
	SiteID          int    `json:"siteId"`
	Destination     string `json:"destination"`
	DestinationPort int    `json:"destinationPort"`
	Alias           string `json:"alias"`
	TCPPortRange    string `json:"tcpPortRangeString"`
	UDPPortRange    string `json:"udpPortRangeString"`
	DisableICMP     bool   `json:"disableIcmp"`
	Enabled         bool   `json:"enabled"`
}

// CreateSiteResourceRequest is the payload for creating a private resource.
//
// UserIDs, RoleIDs and ClientIDs are required by the API and are therefore not
// omitempty: an endpoint that grants access to nobody must still send empty
// arrays rather than omitting the fields.
type CreateSiteResourceRequest struct {
	Name            string   `json:"name"`
	NiceID          string   `json:"niceId,omitempty"`
	Mode            string   `json:"mode"`
	SiteID          int      `json:"siteId,omitempty"`
	SiteIDs         []int    `json:"siteIds,omitempty"`
	Destination     string   `json:"destination,omitempty"`
	DestinationPort int      `json:"destinationPort,omitempty"`
	Alias           string   `json:"alias,omitempty"`
	TCPPortRange    string   `json:"tcpPortRangeString,omitempty"`
	UDPPortRange    string   `json:"udpPortRangeString,omitempty"`
	DisableICMP     bool     `json:"disableIcmp,omitempty"`
	UserIDs         []string `json:"userIds"`
	RoleIDs         []int    `json:"roleIds"`
	ClientIDs       []int    `json:"clientIds"`
}

// UpdateSiteResourceRequest is the payload for updating a private resource.
//
// DestinationPort is deliberately not omitempty: the field is nullable, and a
// nil pointer must reach Pangolin as an explicit null so a previously set
// destination port is cleared. Omitting it would leave the old value in place,
// and the controller would then see a difference on every reconcile and update
// forever.
type UpdateSiteResourceRequest struct {
	Name            string   `json:"name,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	SiteID          int      `json:"siteId,omitempty"`
	SiteIDs         []int    `json:"siteIds,omitempty"`
	Destination     *string  `json:"destination,omitempty"`
	DestinationPort *int     `json:"destinationPort"`
	Alias           *string  `json:"alias,omitempty"`
	TCPPortRange    *string  `json:"tcpPortRangeString,omitempty"`
	UDPPortRange    *string  `json:"udpPortRangeString,omitempty"`
	DisableICMP     *bool    `json:"disableIcmp,omitempty"`
	Enabled         *bool    `json:"enabled,omitempty"`
	UserIDs         []string `json:"userIds,omitempty"`
	RoleIDs         []int    `json:"roleIds,omitempty"`
	ClientIDs       []int    `json:"clientIds,omitempty"`
}

// Role is a Pangolin role.
type Role struct {
	ID   int    `json:"roleId"`
	Name string `json:"name"`
}

// PangolinClient is a Pangolin mesh client (an Olm device), not a Kubernetes
// client. Named for the Pangolin concept to keep the API surface honest.
type PangolinClient struct {
	ID     int    `json:"clientId"`
	NiceID string `json:"niceId"`
	Name   string `json:"name"`
}

// User is a Pangolin user.
type User struct {
	ID       string `json:"userId"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// CreateSiteResource creates a private resource.
func (c *Client) CreateSiteResource(ctx context.Context, req *CreateSiteResourceRequest) (*SiteResource, error) {
	if req.UserIDs == nil {
		req.UserIDs = []string{}
	}
	if req.RoleIDs == nil {
		req.RoleIDs = []int{}
	}
	if req.ClientIDs == nil {
		req.ClientIDs = []int{}
	}

	resp, err := c.doRequest(ctx, http.MethodPut, fmt.Sprintf("/v1/org/%s/%s", c.orgID, siteResourcePath), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotImplemented(resp); err != nil {
		return nil, err
	}
	return decodeSiteResource(resp)
}

// GetSiteResource retrieves a private resource by its Pangolin ID.
func (c *Client) GetSiteResource(ctx context.Context, siteResourceID string) (*SiteResource, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/%s/%s", siteResourcePath, siteResourceID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotFound(resp); err != nil {
		return nil, err
	}
	return decodeSiteResource(resp)
}

// GetSiteResourceByNiceID retrieves a private resource by its caller-supplied
// nice ID. A 404 is reported as *NotFoundError, because this is the identity
// lookup the controller uses to decide between create and update.
func (c *Client) GetSiteResourceByNiceID(ctx context.Context, siteID, niceID string) (*SiteResource, error) {
	path := fmt.Sprintf("/v1/org/%s/site/%s/resource/nice/%s", c.orgID, url.PathEscape(siteID), url.PathEscape(niceID))
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotFound(resp); err != nil {
		return nil, err
	}
	return decodeSiteResource(resp)
}

// UpdateSiteResource updates a private resource.
func (c *Client) UpdateSiteResource(ctx context.Context, siteResourceID string, req *UpdateSiteResourceRequest) (*SiteResource, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/%s/%s", siteResourcePath, siteResourceID), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotImplemented(resp); err != nil {
		return nil, err
	}
	return decodeSiteResource(resp)
}

// DeleteSiteResource deletes a private resource. An already-absent resource is
// reported as *NotFoundError so callers can treat deletion as done.
func (c *Client) DeleteSiteResource(ctx context.Context, siteResourceID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, fmt.Sprintf("/v1/%s/%s", siteResourcePath, siteResourceID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return checkResponseWithNotFound(resp)
}

// ListRoles lists all roles in the organization, following pagination.
func (c *Client) ListRoles(ctx context.Context) ([]Role, error) {
	var out []Role
	err := c.listPaginated(ctx, fmt.Sprintf("/v1/org/%s/roles", c.orgID), func(body []byte) (int, error) {
		var payload struct {
			Roles []Role `json:"roles"`
		}
		if err := decodeData(body, &payload); err != nil {
			return 0, err
		}
		out = append(out, payload.Roles...)
		return len(payload.Roles), nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListClients lists all mesh clients in the organization, following pagination.
func (c *Client) ListClients(ctx context.Context) ([]PangolinClient, error) {
	var out []PangolinClient
	err := c.listPaginated(ctx, fmt.Sprintf("/v1/org/%s/clients", c.orgID), func(body []byte) (int, error) {
		var payload struct {
			Clients []PangolinClient `json:"clients"`
		}
		if err := decodeData(body, &payload); err != nil {
			return 0, err
		}
		out = append(out, payload.Clients...)
		return len(payload.Clients), nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetUserByUsername looks a user up by username. An unknown username is
// reported as *NotFoundError.
func (c *Client) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	path := fmt.Sprintf("/v1/org/%s/user-by-username?username=%s", c.orgID, url.QueryEscape(username))
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotFound(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var user User
	if err := decodeData(body, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// ListSiteResourceRoles returns the role IDs assigned to a private resource.
func (c *Client) ListSiteResourceRoles(ctx context.Context, siteResourceID string) ([]int, error) {
	var payload struct {
		Roles []struct {
			RoleID int `json:"roleId"`
		} `json:"roles"`
	}
	if err := c.getSiteResourceSub(ctx, siteResourceID, "roles", &payload); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(payload.Roles))
	for _, r := range payload.Roles {
		ids = append(ids, r.RoleID)
	}
	return ids, nil
}

// SetSiteResourceRoles replaces the roles assigned to a private resource.
func (c *Client) SetSiteResourceRoles(ctx context.Context, siteResourceID string, roleIDs []int) error {
	if roleIDs == nil {
		roleIDs = []int{}
	}
	body := struct {
		RoleIDs []int `json:"roleIds"`
	}{RoleIDs: roleIDs}
	return c.postSiteResourceSub(ctx, siteResourceID, "roles", body)
}

// ListSiteResourceUsers returns the user IDs assigned to a private resource.
func (c *Client) ListSiteResourceUsers(ctx context.Context, siteResourceID string) ([]string, error) {
	var payload struct {
		Users []struct {
			UserID string `json:"userId"`
		} `json:"users"`
	}
	if err := c.getSiteResourceSub(ctx, siteResourceID, "users", &payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Users))
	for _, u := range payload.Users {
		ids = append(ids, u.UserID)
	}
	return ids, nil
}

// SetSiteResourceUsers replaces the users assigned to a private resource.
func (c *Client) SetSiteResourceUsers(ctx context.Context, siteResourceID string, userIDs []string) error {
	if userIDs == nil {
		userIDs = []string{}
	}
	body := struct {
		UserIDs []string `json:"userIds"`
	}{UserIDs: userIDs}
	return c.postSiteResourceSub(ctx, siteResourceID, "users", body)
}

// ListSiteResourceClients returns the client IDs assigned to a private
// resource.
func (c *Client) ListSiteResourceClients(ctx context.Context, siteResourceID string) ([]int, error) {
	var payload struct {
		Clients []struct {
			ClientID int `json:"clientId"`
		} `json:"clients"`
	}
	if err := c.getSiteResourceSub(ctx, siteResourceID, "clients", &payload); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(payload.Clients))
	for _, cl := range payload.Clients {
		ids = append(ids, cl.ClientID)
	}
	return ids, nil
}

// SetSiteResourceClients replaces the clients assigned to a private resource.
func (c *Client) SetSiteResourceClients(ctx context.Context, siteResourceID string, clientIDs []int) error {
	if clientIDs == nil {
		clientIDs = []int{}
	}
	body := struct {
		ClientIDs []int `json:"clientIds"`
	}{ClientIDs: clientIDs}
	return c.postSiteResourceSub(ctx, siteResourceID, "clients", body)
}

func (c *Client) getSiteResourceSub(ctx context.Context, siteResourceID, sub string, out interface{}) error {
	resp, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/%s/%s/%s", siteResourcePath, siteResourceID, sub), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := checkResponseWithNotImplemented(resp); err != nil {
		return err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}
	return decodeData(body, out)
}

func (c *Client) postSiteResourceSub(ctx context.Context, siteResourceID, sub string, body interface{}) error {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/%s/%s/%s", siteResourcePath, siteResourceID, sub), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return checkResponseWithNotImplemented(resp)
}

// listPaginated walks a paginated list endpoint until a short page is
// returned, handing each page body to collect. Pangolin defaults these
// endpoints to 20 items per page, so a single unpaginated request would
// silently truncate the result.
func (c *Client) listPaginated(ctx context.Context, path string, collect func(body []byte) (int, error)) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}

	for page := 1; ; page++ {
		pagePath := fmt.Sprintf("%s%spage=%d&pageSize=%d", path, sep, page, listPageSize)
		resp, err := c.doRequest(ctx, http.MethodGet, pagePath, nil)
		if err != nil {
			return err
		}

		if err := checkResponse(resp); err != nil {
			resp.Body.Close()
			return err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		n, err := collect(body)
		if err != nil {
			return err
		}
		if n < listPageSize {
			return nil
		}
	}
}

func decodeSiteResource(resp *http.Response) (*SiteResource, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var sr SiteResource
	if err := decodeData(body, &sr); err != nil {
		return nil, err
	}
	return &sr, nil
}
