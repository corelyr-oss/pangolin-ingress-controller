package pangolin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func envelope(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data": data, "success": true, "error": false, "message": "", "status": 200,
	})
}

func TestCreateSiteResource_SendsRequiredArraysAndPath(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		envelope(w, map[string]interface{}{"siteResourceId": 42, "niceId": "x", "alias": "x.internal"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	res, err := c.CreateSiteResource(context.Background(), &CreateSiteResourceRequest{
		Name: "x", NiceID: "x", Mode: "host", SiteID: 1, Destination: "svc.ns.svc.cluster.local",
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotMethod != http.MethodPut {
		t.Fatalf("method = %s want PUT", gotMethod)
	}
	if gotPath != "/v1/org/org1/site-resource" {
		t.Fatalf("path = %s", gotPath)
	}
	if res.ID != 42 || res.Alias != "x.internal" {
		t.Fatalf("decoded %+v", res)
	}

	// The API requires these arrays to be present even when empty; omitting
	// them is rejected.
	for _, key := range []string{"userIds", "roleIds", "clientIds"} {
		if _, ok := gotBody[key]; !ok {
			t.Fatalf("request body is missing required field %q: %v", key, gotBody)
		}
	}
}

func TestUpdateSiteResource_AlwaysSendsDestinationPort(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		envelope(w, map[string]interface{}{"siteResourceId": 42})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	if _, err := c.UpdateSiteResource(context.Background(), "42", &UpdateSiteResourceRequest{Name: "x"}); err != nil {
		t.Fatal(err)
	}

	// A nil DestinationPort must reach Pangolin as an explicit null so a
	// previously set port is cleared; omitting it would leave the controller
	// diffing against a value it can never change.
	v, ok := gotBody["destinationPort"]
	if !ok {
		t.Fatalf("destinationPort must be sent, body = %v", gotBody)
	}
	if v != nil {
		t.Fatalf("destinationPort = %v want null", v)
	}
}

// siteListing serves the site-resource listing in the envelope a real instance
// uses: one element per resource, the resource nested under a repeated
// "siteResources" key, paged by limit and offset.
func siteListing(t *testing.T, resources []SiteResource, pageCap int) (*httptest.Server, *int) {
	t.Helper()
	var requests int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/org/org1/site/1/resources" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		requests++

		offset := 0
		fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
		if offset > len(resources) {
			offset = len(resources)
		}
		end := len(resources)
		if pageCap > 0 && offset+pageCap < end {
			end = offset + pageCap
		}

		entries := make([]map[string]interface{}, 0, end-offset)
		for _, res := range resources[offset:end] {
			// The resource object in a real listing has no siteId; the site
			// association is the sibling siteNetworks row.
			raw, _ := json.Marshal(res)
			var m map[string]interface{}
			_ = json.Unmarshal(raw, &m)
			delete(m, "siteId")
			entries = append(entries, map[string]interface{}{
				"siteNetworks":  map[string]interface{}{"siteId": 1, "networkId": 3},
				"siteResources": m,
			})
		}
		envelope(w, map[string]interface{}{"siteResources": entries})
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// A complete listing that holds no match is the only thing that means absent.
func TestGetSiteResourceByNiceID_AbsentFromListingIsNotFound(t *testing.T) {
	srv, _ := siteListing(t, []SiteResource{{ID: 7, NiceID: "someone-else"}}, 0)

	c := NewClient(srv.URL, "key", "org1")
	_, err := c.GetSiteResourceByNiceID(context.Background(), "1", "my-endpoint")
	if !IsNotFound(err) {
		t.Fatalf("got %v want *NotFoundError", err)
	}
	if IsNotImplemented(err) {
		t.Fatal("an identity lookup must report absence, not an unsupported server")
	}
}

func TestGetSiteResourceByNiceID_FindsMatchAndUnwrapsEnvelope(t *testing.T) {
	srv, _ := siteListing(t, []SiteResource{
		{ID: 7, NiceID: "someone-else"},
		{ID: 42, NiceID: "my-endpoint", Alias: "x.internal", AliasAddress: "100.96.128.12"},
	}, 0)

	c := NewClient(srv.URL, "key", "org1")
	got, err := c.GetSiteResourceByNiceID(context.Background(), "1", "my-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.Alias != "x.internal" || got.AliasAddress != "100.96.128.12" {
		t.Fatalf("decoded %+v", got)
	}
}

// A resource beyond the first page is still present. Stopping early here is how
// an identity lookup misses its own resource and creates a duplicate.
func TestGetSiteResourceByNiceID_FollowsPaginationPastAShortPage(t *testing.T) {
	resources := make([]SiteResource, 0, 5)
	for i := 0; i < 4; i++ {
		resources = append(resources, SiteResource{ID: i + 1, NiceID: fmt.Sprintf("other-%d", i)})
	}
	resources = append(resources, SiteResource{ID: 99, NiceID: "my-endpoint"})

	// A server capping every page at 2, well below the requested limit.
	srv, requests := siteListing(t, resources, 2)

	c := NewClient(srv.URL, "key", "org1")
	got, err := c.GetSiteResourceByNiceID(context.Background(), "1", "my-endpoint")
	if err != nil {
		t.Fatalf("a resource on a later page must still be found: %v", err)
	}
	if got.ID != 99 {
		t.Fatalf("found id %d want 99", got.ID)
	}
	if *requests < 3 {
		t.Fatalf("made %d requests: pagination was not followed", *requests)
	}
}

// A listing that fails must surface as itself. Reporting it as not-found would
// tell the caller to create a resource that already exists.
func TestGetSiteResourceByNiceID_ListingFailureIsNotAbsence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	_, err := c.GetSiteResourceByNiceID(context.Background(), "1", "my-endpoint")
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsNotFound(err) {
		t.Fatal("a failed listing must not be reported as absence: the caller would create a duplicate")
	}
}

// The point read the client used to depend on is rejected by every real
// instance, so the listing is what GetSiteResource must use.
func TestGetSiteResource_ReadsThroughListing(t *testing.T) {
	srv, _ := siteListing(t, []SiteResource{{ID: 42, NiceID: "mine", TCPPortRange: "5432"}}, 0)

	c := NewClient(srv.URL, "key", "org1")
	got, err := c.GetSiteResource(context.Background(), "1", "42")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.TCPPortRange != "5432" {
		t.Fatalf("decoded %+v", got)
	}

	if _, err := c.GetSiteResource(context.Background(), "1", "404"); !IsNotFound(err) {
		t.Fatalf("got %v want *NotFoundError", err)
	}
}

// Pangolin refills an absent port range with "*" -- every port of that
// protocol. Both ranges must therefore appear on the wire even when empty.
func TestSiteResourceRequests_AlwaysSendBothPortRanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(*Client) error
	}{
		{"create", func(c *Client) error {
			_, err := c.CreateSiteResource(context.Background(), &CreateSiteResourceRequest{
				Name: "x", Mode: "host", SiteID: 1, TCPPortRange: "8080",
			})
			return err
		}},
		{"update", func(c *Client) error {
			_, err := c.UpdateSiteResource(context.Background(), "42", &UpdateSiteResourceRequest{
				Name: "x", TCPPortRange: "8080",
			})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]interface{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				envelope(w, map[string]interface{}{"siteResourceId": 42})
			}))
			defer srv.Close()

			if err := tc.send(NewClient(srv.URL, "key", "org1")); err != nil {
				t.Fatal(err)
			}

			udp, ok := gotBody["udpPortRangeString"]
			if !ok {
				t.Fatalf("udpPortRangeString omitted: Pangolin would fill it with \"*\", body = %v", gotBody)
			}
			if udp != "" {
				t.Fatalf("udpPortRangeString = %v want empty", udp)
			}
			if _, ok := gotBody["tcpPortRangeString"]; !ok {
				t.Fatalf("tcpPortRangeString omitted, body = %v", gotBody)
			}
		})
	}
}

func TestCreateSiteResource_404IsNotImplemented(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	_, err := c.CreateSiteResource(context.Background(), &CreateSiteResourceRequest{Name: "x", Mode: "host"})
	if !IsNotImplemented(err) {
		t.Fatalf("got %v want *NotImplementedError", err)
	}
}

func TestDeleteSiteResource_404IsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"gone"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	if err := c.DeleteSiteResource(context.Background(), "42"); !IsNotFound(err) {
		t.Fatalf("got %v want *NotFoundError", err)
	}
}

// Pangolin defaults these list endpoints to 20 items per page. Without
// pagination the 21st role would be invisible and would resolve as "not
// found", silently denying access.
func TestListRoles_FollowsPagination(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)

		roles := make([]Role, 0, listPageSize)
		if page == "1" {
			for i := 0; i < listPageSize; i++ {
				roles = append(roles, Role{ID: i + 1, Name: fmt.Sprintf("role-%d", i+1)})
			}
		} else {
			roles = append(roles, Role{ID: 9999, Name: "last-role"})
		}
		envelope(w, map[string]interface{}{"roles": roles})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	roles, err := c.ListRoles(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(roles) != listPageSize+1 {
		t.Fatalf("got %d roles want %d", len(roles), listPageSize+1)
	}
	if roles[len(roles)-1].Name != "last-role" {
		t.Fatalf("last role = %q; the second page was not followed", roles[len(roles)-1].Name)
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Fatalf("pages requested = %v", pages)
	}
}

func TestGetUserByUsername_EscapesQuery(t *testing.T) {
	var gotUsername string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUsername = r.URL.Query().Get("username")
		envelope(w, map[string]interface{}{"userId": "u-1", "username": gotUsername})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	user, err := c.GetUserByUsername(context.Background(), "office+test@corelyr.com")
	if err != nil {
		t.Fatal(err)
	}
	if gotUsername != "office+test@corelyr.com" {
		t.Fatalf("username round-tripped as %q", gotUsername)
	}
	if user.ID != "u-1" {
		t.Fatalf("user = %+v", user)
	}
}

func TestSiteResourceRoles_ListAndSet(t *testing.T) {
	var setBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/site-resource/42/roles" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method == http.MethodGet {
			// The admin role is what the live instance attaches by itself; the
			// isAdmin flag is the only thing that distinguishes it.
			envelope(w, map[string]interface{}{"roles": []map[string]interface{}{
				{"roleId": 1, "name": "Admin", "isAdmin": true},
				{"roleId": 3, "name": "developers", "isAdmin": false},
				{"roleId": 7, "name": "ops", "isAdmin": false},
			}})
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&setBody)
		envelope(w, map[string]interface{}{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	roles, err := c.ListSiteResourceRoles(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 3 || roles[1].ID != 3 || roles[2].ID != 7 {
		t.Fatalf("got %v want ids [1 3 7]", roles)
	}
	// Without this flag the caller cannot tell a server-granted role from one
	// an operator named, and writes the difference back on every reconcile.
	if !roles[0].IsAdmin {
		t.Fatal("isAdmin was dropped when decoding the role list")
	}
	if roles[1].IsAdmin || roles[2].IsAdmin {
		t.Fatalf("non-admin roles decoded as admin: %v", roles)
	}
	if roles[1].Name != "developers" {
		t.Fatalf("role name = %q", roles[1].Name)
	}

	if err := c.SetSiteResourceRoles(context.Background(), "42", nil); err != nil {
		t.Fatal(err)
	}
	if setBody["roleIds"] == nil {
		t.Fatalf("a nil slice must be sent as an empty array, got %v", setBody)
	}
}

// The site association lives in the sibling siteNetworks row, not on the
// resource. Losing it leaves SiteID 0, which never matches the site the
// controller asked for -- so the controller sees a difference on every
// reconcile and updates forever. Found in a live run, not by the fixtures.
func TestListSiteResources_TakesSiteIDFromSiteNetworks(t *testing.T) {
	srv, _ := siteListing(t, []SiteResource{{ID: 42, NiceID: "mine"}}, 0)

	c := NewClient(srv.URL, "key", "org1")
	got, err := c.GetSiteResource(context.Background(), "1", "42")
	if err != nil {
		t.Fatal(err)
	}
	if got.SiteID != 1 {
		t.Fatalf("siteId = %d want 1: an unset site makes every reconcile see a difference", got.SiteID)
	}
}

// Pangolin accepts a duplicate niceId (201) while refusing a duplicate alias
// (409), so a listing can hold two resources with the identity the controller
// recovers by. Returning either would reprogram a resource that may belong to
// something else.
func TestGetSiteResourceByNiceID_DuplicateIsAmbiguousNotAGuess(t *testing.T) {
	srv, _ := siteListing(t, []SiteResource{
		{ID: 11, NiceID: "shared"},
		{ID: 16, NiceID: "shared"},
	}, 0)

	c := NewClient(srv.URL, "key", "org1")
	got, err := c.GetSiteResourceByNiceID(context.Background(), "1", "shared")
	if got != nil {
		t.Fatalf("a candidate was returned (%d): the lookup guessed between two matches", got.ID)
	}
	if !IsAmbiguous(err) {
		t.Fatalf("got %v want *AmbiguousError", err)
	}
	if IsNotFound(err) {
		t.Fatal("an ambiguous identity must not read as absence: the caller would create a third resource")
	}
	// The message has to name the candidates, or an operator cannot resolve it.
	for _, want := range []string{"11", "16"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name the colliding ids, got %q", err.Error())
		}
	}
}

// A single match is still a match.
func TestGetSiteResourceByNiceID_SingleMatchIsNotAmbiguous(t *testing.T) {
	srv, _ := siteListing(t, []SiteResource{
		{ID: 11, NiceID: "mine"},
		{ID: 16, NiceID: "someone-else"},
	}, 0)

	c := NewClient(srv.URL, "key", "org1")
	got, err := c.GetSiteResourceByNiceID(context.Background(), "1", "mine")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 11 {
		t.Fatalf("got id %d want 11", got.ID)
	}
}
