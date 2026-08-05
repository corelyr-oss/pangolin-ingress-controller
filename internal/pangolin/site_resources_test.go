package pangolin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestGetSiteResourceByNiceID_404IsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/org/org1/site/1/resource/nice/my-endpoint" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	_, err := c.GetSiteResourceByNiceID(context.Background(), "1", "my-endpoint")
	if !IsNotFound(err) {
		t.Fatalf("got %v want *NotFoundError", err)
	}
	if IsNotImplemented(err) {
		t.Fatal("an identity lookup must report absence, not an unsupported server")
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
			envelope(w, map[string]interface{}{"roles": []map[string]int{{"roleId": 3}, {"roleId": 7}}})
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&setBody)
		envelope(w, map[string]interface{}{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key", "org1")
	ids, err := c.ListSiteResourceRoles(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 3 || ids[1] != 7 {
		t.Fatalf("got %v want [3 7]", ids)
	}

	if err := c.SetSiteResourceRoles(context.Background(), "42", nil); err != nil {
		t.Fatal(err)
	}
	if setBody["roleIds"] == nil {
		t.Fatalf("a nil slice must be sent as an empty array, got %v", setBody)
	}
}
