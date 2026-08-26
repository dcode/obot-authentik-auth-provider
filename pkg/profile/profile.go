// Package profile maps Authentik API responses onto the shapes Obot's auth-provider contract
// expects, using github.com/obot-platform/enterprise-providers/authcommon for the
// provider-agnostic parts (cursor encoding, concurrent ID resolution, HTTP handler plumbing).
package profile

import (
	"context"
	"fmt"
	"strconv"

	"github.com/obot-platform/enterprise-providers/authcommon"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"

	"github.com/dcode/obot-authentik-auth-provider/pkg/authentikapi"
)

// groupIDPrefix namespaces every group ID this provider hands to Obot, so a group ID can be
// traced back to Authentik and never collides with another provider's IDs. It must match the
// groupIDPrefix declared in auth-providers/authentik-auth-provider.yaml.
const groupIDPrefix = "authentik/"

func toGroupInfo(g authentikapi.Group) state.GroupInfo {
	return state.GroupInfo{
		ID:   groupIDPrefix + g.PK,
		Name: g.Name,
		// Authentik has no per-group icon/photo concept.
		IconURL: nil,
	}
}

func toGroupInfoList(groups []authentikapi.Group) state.GroupInfoList {
	infos := make(state.GroupInfoList, len(groups))
	for i, g := range groups {
		infos[i] = toGroupInfo(g)
	}
	return infos
}

// FetchGroupPage fetches one page of Authentik's group directory, for
// GET /obot-list-auth-groups. The authcommon cursor wraps Authentik's own page number.
func FetchGroupPage(ctx context.Context, client *authentikapi.Client, req authcommon.PageRequest) (authcommon.PageResult, error) {
	page := 0
	if req.Cursor != "" {
		p, err := strconv.Atoi(req.Cursor)
		if err != nil {
			return authcommon.PageResult{}, fmt.Errorf("%w: not a page number", authcommon.ErrInvalidCursor)
		}
		page = p
	}

	groups, nextPage, err := client.ListGroups(ctx, req.NameFilter, page, req.Limit)
	if err != nil {
		return authcommon.PageResult{}, err
	}

	var nextCursor string
	if nextPage > 0 {
		nextCursor = strconv.Itoa(nextPage)
	}

	return authcommon.PageResult{
		Items:      toGroupInfoList(groups),
		NextCursor: nextCursor,
	}, nil
}

// FetchGroupsByIDs resolves group IDs to their current names, for GET /obot-get-auth-groups.
//
// Authentik's API has no batch group read, so -- like Okta's provider -- this is one request
// per ID, overlapped by authcommon.ResolveGroupsByLookup.
func FetchGroupsByIDs(ctx context.Context, client *authentikapi.Client, ids []string) (state.GroupInfoList, error) {
	return authcommon.ResolveGroupsByLookup(ctx, ids, func(ctx context.Context, id string) (*state.GroupInfo, error) {
		group, err := client.GetGroup(ctx, id)
		if err != nil {
			return nil, err
		}
		if group == nil {
			return nil, nil
		}
		info := toGroupInfo(*group)
		return &info, nil
	})
}

// FetchUserGroupInfos returns the groups the given Authentik user belongs to, for
// POST /obot-list-user-auth-groups. userID must be Authentik's own user ID (the OIDC "sub"
// claim, given Subject mode "Based on the User's ID" -- see docs/configuration.md).
func FetchUserGroupInfos(ctx context.Context, client *authentikapi.Client, userID string) (state.GroupInfoList, error) {
	ids, err := client.GetUserGroupIDs(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch group memberships for user %s: %w", userID, err)
	}
	if len(ids) == 0 {
		return state.GroupInfoList{}, nil
	}

	// GetUserGroupIDs already returns bare Authentik PKs -- the same shape FetchGroupsByIDs
	// expects, since its other caller (GetGroupsHandler) strips the "authentik/" prefix before
	// handing IDs over.
	return FetchGroupsByIDs(ctx, client, ids)
}
