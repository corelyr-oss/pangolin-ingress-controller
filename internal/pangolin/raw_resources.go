package pangolin

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// CreateRawResourceRequest is the raw (non-HTTP) variant of the public
// resource create body. Pangolin accepts no niceId here; the caller names the
// resource afterwards with UpdateRawResource.
type CreateRawResourceRequest struct {
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	ProxyPort int    `json:"proxyPort"`
}

// UpdateRawResourceRequest is the raw variant of the public resource update
// body. Only set fields are sent.
type UpdateRawResourceRequest struct {
	Name      string `json:"name,omitempty"`
	NiceID    string `json:"niceId,omitempty"`
	ProxyPort *int   `json:"proxyPort,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

// CreateRawResource creates a raw TCP or UDP public resource.
func (c *Client) CreateRawResource(ctx context.Context, req *CreateRawResourceRequest) (*Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodPut, fmt.Sprintf("/v1/org/%s/resource", c.orgID), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	return decodeResource(resp)
}

// UpdateRawResource updates a raw public resource. Setting a niceId another
// resource already carries is refused by Pangolin with a *ConflictError.
func (c *Client) UpdateRawResource(ctx context.Context, resourceID string, req *UpdateRawResourceRequest) (*Resource, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/resource/%s", resourceID), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	return decodeResource(resp)
}

// ListResources lists every public resource in the organization, following
// pagination to the end. It returns everything or an error, never a partial
// list: callers use it to decide that a resource does not exist (identity
// recovery, the proxy-port check, Ingress adoption), and Pangolin's default
// page size of 20 would otherwise hide most of a larger organization.
//
// The endpoint paginates by page and pageSize and rejects limit/offset with a
// 400, unlike the private-resource listings. Its entries also differ from the
// resource object: they carry fullDomain and domainId but no subdomain.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	const maxPages = 1000

	var out []Resource
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/v1/org/%s/resources?pageSize=%d&page=%d", c.orgID, listPageSize, page)
		resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if err := checkResponse(resp); err != nil {
			resp.Body.Close()
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		var payload struct {
			Resources  []Resource `json:"resources"`
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
		}
		if err := decodeData(body, &payload); err != nil {
			return nil, err
		}

		out = append(out, payload.Resources...)
		if len(payload.Resources) == 0 {
			return out, nil
		}
		// Prefer the reported total. Without one, a short page is the end.
		if total := payload.Pagination.Total; total > 0 {
			if len(out) >= total {
				return out, nil
			}
		} else if len(payload.Resources) < listPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("resource listing did not terminate after %d pages: the server may be ignoring the page parameter", maxPages)
}

func decodeResource(resp *http.Response) (*Resource, error) {
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
