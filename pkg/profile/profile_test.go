package profile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/obot-platform/enterprise-providers/authcommon"

	"github.com/dcode/obot-authentik-auth-provider/pkg/authentikapi"
)

func TestFetchGroupPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Errorf("page = %q, want 2", got)
		}
		_, _ = w.Write([]byte(`{"pagination":{"next":3},"results":[{"pk":"g1","name":"engineering"}]}`))
	}))
	defer srv.Close()

	client := authentikapi.New(srv.URL, "token")
	result, err := FetchGroupPage(context.Background(), client, authcommon.PageRequest{Limit: 50, Cursor: "2"})
	if err != nil {
		t.Fatalf("FetchGroupPage() error = %v", err)
	}
	if result.NextCursor != "3" {
		t.Errorf("NextCursor = %q, want %q", result.NextCursor, "3")
	}
	if len(result.Items) != 1 || result.Items[0].ID != "authentik/g1" {
		t.Errorf("Items = %+v, want one with ID authentik/g1", result.Items)
	}
}

func TestFetchGroupPage_InvalidCursor(t *testing.T) {
	client := authentikapi.New("https://unused.example", "token")
	_, err := FetchGroupPage(context.Background(), client, authcommon.PageRequest{Limit: 50, Cursor: "not-a-page-number"})
	if err == nil {
		t.Fatal("FetchGroupPage() error = nil, want an error for a non-numeric cursor")
	}
}

func TestFetchGroupsByIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/core/groups/g1/":
			_, _ = w.Write([]byte(`{"pk":"g1","name":"engineering"}`))
		case "/api/v3/core/groups/deleted/":
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := authentikapi.New(srv.URL, "token")
	groups, err := FetchGroupsByIDs(context.Background(), client, []string{"g1", "deleted"})
	if err != nil {
		t.Fatalf("FetchGroupsByIDs() error = %v", err)
	}
	if len(groups) != 1 || groups[0].ID != "authentik/g1" {
		t.Errorf("groups = %+v, want only authentik/g1 (deleted group silently dropped)", groups)
	}
}

func TestFetchUserGroupInfos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/core/users/42/":
			_, _ = w.Write([]byte(`{"groups":["g1"]}`))
		case "/api/v3/core/groups/g1/":
			_, _ = w.Write([]byte(`{"pk":"g1","name":"engineering"}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := authentikapi.New(srv.URL, "token")
	groups, err := FetchUserGroupInfos(context.Background(), client, "42")
	if err != nil {
		t.Fatalf("FetchUserGroupInfos() error = %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "engineering" {
		t.Errorf("groups = %+v, want one named engineering", groups)
	}
}

func TestFetchUserGroupInfos_NoGroups(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"groups":[]}`))
	}))
	defer srv.Close()

	client := authentikapi.New(srv.URL, "token")
	groups, err := FetchUserGroupInfos(context.Background(), client, "42")
	if err != nil {
		t.Fatalf("FetchUserGroupInfos() error = %v", err)
	}
	if groups == nil || len(groups) != 0 {
		t.Errorf("groups = %+v, want an empty (non-nil) list", groups)
	}
}
