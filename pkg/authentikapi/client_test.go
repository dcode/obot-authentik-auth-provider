package authentikapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListGroups(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer test-token")
		}
		if got := r.URL.Query().Get("page_size"); got != "50" {
			t.Errorf("page_size = %q, want %q", got, "50")
		}
		if got := r.URL.Query().Get("search"); got != "eng" {
			t.Errorf("search = %q, want %q", got, "eng")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pagination":{"next":2},"results":[{"pk":"g1","name":"engineering"}]}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token")
	groups, next, err := client.ListGroups(context.Background(), "eng", 0, 50)
	if err != nil {
		t.Fatalf("ListGroups() error = %v", err)
	}
	if next != 2 {
		t.Errorf("next page = %d, want 2", next)
	}
	if len(groups) != 1 || groups[0].PK != "g1" || groups[0].Name != "engineering" {
		t.Errorf("groups = %+v, want one {g1 engineering}", groups)
	}
}

func TestListGroups_LastPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"pagination":{"next":0},"results":[]}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token")
	_, next, err := client.ListGroups(context.Background(), "", 3, 50)
	if err != nil {
		t.Fatalf("ListGroups() error = %v", err)
	}
	if next != 0 {
		t.Errorf("next page = %d, want 0", next)
	}
}

func TestGetGroup_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token")
	group, err := client.GetGroup(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetGroup() error = %v, want nil (not-found is not an error)", err)
	}
	if group != nil {
		t.Errorf("group = %+v, want nil", group)
	}
}

func TestGetGroup_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/core/groups/g1/" {
			t.Errorf("path = %q, want /api/v3/core/groups/g1/", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"pk":"g1","name":"engineering"}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token")
	group, err := client.GetGroup(context.Background(), "g1")
	if err != nil {
		t.Fatalf("GetGroup() error = %v", err)
	}
	if group == nil || group.Name != "engineering" {
		t.Errorf("group = %+v, want {g1 engineering}", group)
	}
}

func TestGetGroup_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token")
	if _, err := client.GetGroup(context.Background(), "g1"); err == nil {
		t.Fatal("GetGroup() error = nil, want an error for a 500 response")
	}
}

func TestGetUserGroupIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/core/users/42/" {
			t.Errorf("path = %q, want /api/v3/core/users/42/", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"groups":["g1","g2"]}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token")
	ids, err := client.GetUserGroupIDs(context.Background(), "42")
	if err != nil {
		t.Fatalf("GetUserGroupIDs() error = %v", err)
	}
	if len(ids) != 2 || ids[0] != "g1" || ids[1] != "g2" {
		t.Errorf("ids = %v, want [g1 g2]", ids)
	}
}

func TestDiscoverUserInfoEndpoint(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/.well-known/openid-configuration" {
			t.Errorf("path = %q, want /.well-known/openid-configuration", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"userinfo_endpoint":"https://issuer.example/userinfo"}`))
	}))
	defer srv.Close()

	client := New("https://unused.example", "")

	endpoint, err := client.DiscoverUserInfoEndpoint(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("DiscoverUserInfoEndpoint() error = %v", err)
	}
	if endpoint != "https://issuer.example/userinfo" {
		t.Errorf("endpoint = %q, want https://issuer.example/userinfo", endpoint)
	}

	// A second call must not hit the network again.
	if _, err := client.DiscoverUserInfoEndpoint(context.Background(), srv.URL); err != nil {
		t.Fatalf("DiscoverUserInfoEndpoint() second call error = %v", err)
	}
	if requests != 1 {
		t.Errorf("discovery requests = %d, want 1 (result should be cached)", requests)
	}
}

func TestDiscoverUserInfoEndpoint_MissingField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := New("https://unused.example", "")
	if _, err := client.DiscoverUserInfoEndpoint(context.Background(), srv.URL); err == nil {
		t.Fatal("DiscoverUserInfoEndpoint() error = nil, want an error when userinfo_endpoint is missing")
	}
}
