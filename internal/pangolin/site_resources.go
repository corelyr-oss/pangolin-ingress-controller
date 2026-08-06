package pangolin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
// The field names were confirmed against a live instance on 2026-08-06 by
// capturing the site listing; see the change `fix-private-endpoint-live-defects`
// (design.md, Context) for the full payload.
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

	// AliasAddress is the mesh address Pangolin assigns to the alias. It is
	// what a client actually dials; the alias is what an operator configures.
	AliasAddress string `json:"aliasAddress"`

	// Status is Pangolin's own approval state for the resource, e.g. "approved".
	Status string `json:"status"`
}

// siteResourceListEntry is one element of a site listing. Each element wraps
// the resource under a repeated `siteResources` key, alongside the network
// rows the join returns.
//
// The site association is one of those sibling rows: the resource object in a
// listing carries no siteId of its own. Reading only the nested resource
// therefore yields SiteID 0, which never matches the site the controller asked
// for -- so every reconcile would see a difference and update forever.
type siteResourceListEntry struct {
	SiteResource SiteResource `json:"siteResources"`
	SiteNetworks struct {
		SiteID int `json:"siteId"`
	} `json:"siteNetworks"`
}

// CreateSiteResourceRequest is the payload for creating a private resource.
//
// UserIDs, RoleIDs and ClientIDs are required by the API and are therefore not
// omitempty: an endpoint that grants access to nobody must still send empty
// arrays rather than omitting the fields.
//
// Neither port range is omitempty either, for a sharper reason: Pangolin
// substitutes "*" -- every port of that protocol -- for a range it is not sent.
// A TCP-only endpoint that omits the UDP range is therefore created with all
// UDP ports open to every principal granted access. The empty string is the
// representation of "no ports"; null is rejected outright, so the field has to
// be present and empty rather than a nil pointer.
type CreateSiteResourceRequest struct {
	Name            string   `json:"name"`
	NiceID          string   `json:"niceId,omitempty"`
	Mode            string   `json:"mode"`
	SiteID          int      `json:"siteId,omitempty"`
	SiteIDs         []int    `json:"siteIds,omitempty"`
	Destination     string   `json:"destination,omitempty"`
	DestinationPort int      `json:"destinationPort,omitempty"`
	Alias           string   `json:"alias,omitempty"`
	TCPPortRange    string   `json:"tcpPortRangeString"`
	UDPPortRange    string   `json:"udpPortRangeString"`
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
//
// The port ranges are plain strings rather than pointers for a related reason.
// They must always be sent -- an absent range is refilled with "*" -- and an
// explicit null is rejected, so neither "omit" nor "null" is ever the right
// wire form. A *string with omitempty leaves omission representable, and the
// only way to reach it is the mistake this type exists to prevent.
type UpdateSiteResourceRequest struct {
	Name            string   `json:"name,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	SiteID          int      `json:"siteId,omitempty"`
	SiteIDs         []int    `json:"siteIds,omitempty"`
	Destination     *string  `json:"destination,omitempty"`
	DestinationPort *int     `json:"destinationPort"`
	Alias           *string  `json:"alias,omitempty"`
	TCPPortRange    string   `json:"tcpPortRangeString"`
	UDPPortRange    string   `json:"udpPortRangeString"`
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

	// IsAdmin marks the organisation's administrator role. Pangolin attaches
	// it to every private resource on its own and keeps it there through any
	// attempt to remove it, so it is the server's to manage and not the
	// controller's. Comparing it as though the controller owned it produces a
	// write on every reconcile that changes nothing.
	IsAdmin bool `json:"isAdmin"`
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

// ListSiteResources returns every private resource attached to a site,
// following pagination.
//
// This is the only working read for private resources. Both documented point
// reads -- GET /site-resource/{id} and GET /private-resource/{id} -- reject
// every request with `expected string, received undefined at "orgId"`, and
// there is no path form that supplies it. The listing is therefore the base
// primitive that GetSiteResource and GetSiteResourceByNiceID are built on.
func (c *Client) ListSiteResources(ctx context.Context, siteID string) ([]SiteResource, error) {
	path := fmt.Sprintf("/v1/org/%s/site/%s/resources", c.orgID, url.PathEscape(siteID))

	var out []SiteResource
	err := c.listOffsetPaginated(ctx, path, func(body []byte) (int, error) {
		var payload struct {
			SiteResources []siteResourceListEntry `json:"siteResources"`
		}
		if err := decodeData(body, &payload); err != nil {
			return 0, err
		}
		for _, entry := range payload.SiteResources {
			resource := entry.SiteResource
			if resource.SiteID == 0 {
				resource.SiteID = entry.SiteNetworks.SiteID
			}
			out = append(out, resource)
		}
		return len(payload.SiteResources), nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetSiteResource retrieves a private resource by its Pangolin ID, by selecting
// it out of the site listing. A resource absent from a complete listing is
// reported as *NotFoundError.
func (c *Client) GetSiteResource(ctx context.Context, siteID, siteResourceID string) (*SiteResource, error) {
	return c.findInSiteListing(ctx, siteID, func(sr *SiteResource) bool {
		return strconv.Itoa(sr.ID) == siteResourceID
	}, fmt.Sprintf("private resource %s", siteResourceID))
}

// GetSiteResourceByNiceID retrieves a private resource by its caller-supplied
// nice ID. This is the identity lookup the controller uses to decide between
// create and update, so the distinction it draws is load-bearing: a
// *NotFoundError means the listing was retrieved in full and held no match,
// and nothing else does. A failure to list is returned as itself, so a caller
// can never read "the server did not answer" as "the resource does not exist"
// and create a duplicate.
func (c *Client) GetSiteResourceByNiceID(ctx context.Context, siteID, niceID string) (*SiteResource, error) {
	return c.findInSiteListing(ctx, siteID, func(sr *SiteResource) bool {
		return sr.NiceID == niceID
	}, fmt.Sprintf("private resource with nice ID %q", niceID))
}

func (c *Client) findInSiteListing(ctx context.Context, siteID string, match func(*SiteResource) bool, describe string) (*SiteResource, error) {
	resources, err := c.ListSiteResources(ctx, siteID)
	if err != nil {
		return nil, err
	}

	var found []*SiteResource
	for i := range resources {
		if match(&resources[i]) {
			found = append(found, &resources[i])
		}
	}

	switch len(found) {
	case 0:
		return nil, &NotFoundError{Message: fmt.Sprintf("%s not found on site %s", describe, siteID)}
	case 1:
		return found[0], nil
	default:
		// Reported rather than resolved: the candidates are indistinguishable
		// by the thing being looked up, so any pick is arbitrary.
		ids := make([]string, 0, len(found))
		for _, resource := range found {
			ids = append(ids, strconv.Itoa(resource.ID))
		}
		return nil, &AmbiguousError{Message: fmt.Sprintf(
			"%s matches %d private resources on site %s (ids %s)",
			describe, len(found), siteID, strings.Join(ids, ", "))}
	}
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

// ListSiteResourceRoles returns the roles assigned to a private resource.
//
// It returns whole roles rather than identifiers because the caller has to be
// able to tell a role Pangolin granted itself from one an operator asked for --
// which only the IsAdmin flag says.
func (c *Client) ListSiteResourceRoles(ctx context.Context, siteResourceID string) ([]Role, error) {
	var payload struct {
		Roles []Role `json:"roles"`
	}
	if err := c.getSiteResourceSub(ctx, siteResourceID, "roles", &payload); err != nil {
		return nil, err
	}
	return payload.Roles, nil
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

// listOffsetPaginated walks a list endpoint that pages by limit and offset,
// stopping only on an empty page.
//
// The site-resource listing rejects the page/pageSize pair that listPaginated
// sends ("Unrecognized keys"), and reports no total or page count, so
// truncation cannot be detected from the response body. It stops on an empty
// page rather than on a short one because a server is free to cap a page below
// the requested limit: treating a short page as the last page would silently
// end the walk early, and an identity lookup that misses its own resource
// creates a duplicate alongside it. The cost is one extra empty request per
// listing, which is the cheaper side of that trade.
//
// maxListedItems bounds the walk so a server that ignores offset -- and
// therefore returns the same page forever -- fails loudly instead of hanging
// the reconcile.
func (c *Client) listOffsetPaginated(ctx context.Context, path string, collect func(body []byte) (int, error)) error {
	const maxListedItems = 100_000

	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}

	for offset := 0; ; {
		pagePath := fmt.Sprintf("%s%slimit=%d&offset=%d", path, sep, listPageSize, offset)
		resp, err := c.doRequest(ctx, http.MethodGet, pagePath, nil)
		if err != nil {
			return err
		}

		if err := checkResponseWithNotImplemented(resp); err != nil {
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
		if n == 0 {
			return nil
		}

		offset += n
		if offset > maxListedItems {
			return fmt.Errorf("listing %s did not terminate after %d items: the server may be ignoring the offset parameter", path, maxListedItems)
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
