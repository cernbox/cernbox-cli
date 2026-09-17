package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cernbox/cernbox-cli/pkg/cberr"
)

// User is the authenticated identity.
type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
	Mail        string `json:"mail,omitempty"`
}

// Space is one CERNBox space (a "drive" in Graph terms).
type Space struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type is "personal", "project", or another server-defined type.
	Type string `json:"type"`
	// Alias is the server's drive alias, which at CERN is the space root path
	// without its leading slash, for example "eos/user/g/gdelmont".
	Alias string `json:"alias,omitempty"`
	// Path is the absolute CERNBox path of the space root.
	Path string `json:"path"`
	// WebDavURL addresses the space root directly.
	WebDavURL string `json:"webdav_url,omitempty"`

	QuotaTotal     int64 `json:"quota_total,omitempty"`
	QuotaUsed      int64 `json:"quota_used,omitempty"`
	QuotaRemaining int64 `json:"quota_remaining,omitempty"`
}

// Unified role identifiers, as defined by libregraph and accepted by ocgraph.
// The CLI exposes short names and translates here, so that users type
// --role editor rather than a UUID.
const (
	RoleViewer      = "b1e2218d-eef8-4d4c-b82d-0f1a1b48f3b5"
	RoleSpaceViewer = "a8d5fe5e-96e3-418d-825b-534dbdf22b99"
	RoleEditor      = "fb6c3e19-e378-47e5-b277-9732f9de6e21"
	RoleSpaceEditor = "58c63c02-1d89-4572-916a-870abc5a1b7d"
	RoleFileEditor  = "2d00ce52-1fc2-4dbc-8b95-a73b73395f5a"
	RoleManager     = "312c0871-5ef7-4b3a-85b6-0e4074c64049"
	RoleDenied      = "5d3754fa-a6a6-4985-b0af-dd1359e5d616"
)

// roleNames maps the CLI's short role names to unified role ids.
var roleNames = map[string]string{
	"viewer":  RoleViewer,
	"reader":  RoleViewer,
	"editor":  RoleEditor,
	"writer":  RoleEditor,
	"collab":  RoleManager,
	"manager": RoleManager,
	"denied":  RoleDenied,
}

// RoleID translates a short role name to a unified role id.
func RoleID(name string) (string, error) {
	if id, ok := roleNames[strings.ToLower(name)]; ok {
		return id, nil
	}
	return "", cberr.Usagef("unknown role %q: want viewer, editor, collab, or denied", name)
}

// RoleName translates a unified role id back to the CLI's short name, falling
// back to the raw id so an unknown role is still displayed rather than hidden.
func RoleName(id string) string {
	switch id {
	case RoleViewer, RoleSpaceViewer:
		return "viewer"
	case RoleEditor, RoleSpaceEditor, RoleFileEditor:
		return "editor"
	case RoleManager:
		return "collab"
	case RoleDenied:
		return "denied"
	default:
		return id
	}
}

// Identity is a share recipient or link creator.
type Identity struct {
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// Type is "user" or "group".
	Type string `json:"type,omitempty"`
}

// Link describes a public link permission.
type Link struct {
	// Type is the libregraph link type, "view" or "edit".
	Type string `json:"type,omitempty"`
	URL  string `json:"url,omitempty"`
	// HasPassword reports whether the link is password protected.
	HasPassword bool `json:"has_password,omitempty"`
}

// Permission is a share or a public link on a resource.
type Permission struct {
	ID        string     `json:"id"`
	Role      string     `json:"role,omitempty"`
	GrantedTo *Identity  `json:"granted_to,omitempty"`
	Link      *Link      `json:"link,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Path is the resource the permission applies to, when the server reports
	// it. Listings fill it in so the user can tell shares apart.
	Path string `json:"path,omitempty"`
}

// ── identity ─────────────────────────────────────────────────────────────────

type graphUser struct {
	ID                       *string `json:"id"`
	DisplayName              string  `json:"displayName"`
	Mail                     *string `json:"mail"`
	OnPremisesSamAccountName string  `json:"onPremisesSamAccountName"`
}

// Me returns the authenticated user, fetching it at most once per process. The
// username is needed to build every path-addressed WebDAV URL, so this runs
// before almost any other call.
func (c *Client) Me(ctx context.Context) (*User, error) {
	c.meOnce.Do(func() {
		c.me, c.meErr = c.fetchMe(ctx)
	})
	return c.me, c.meErr
}

func (c *Client) fetchMe(ctx context.Context) (*User, error) {
	var gu graphUser
	if err := c.getJSON(ctx, c.URL(graphV1+"/me"), "identify the current user", "", &gu); err != nil {
		// A 404 here is not a missing file, it is a missing API. The default
		// "no such file or directory" sends whoever hits it looking for a path
		// that was never involved.
		if cberr.KindOf(err) == cberr.KindNotFound {
			return nil, cberr.New(cberr.KindOther, "identify the current user", "",
				"this server does not expose the graph API at "+graphV1+"/me")
		}
		return nil, err
	}
	u := &User{
		DisplayName: gu.DisplayName,
		Username:    gu.OnPremisesSamAccountName,
	}
	if gu.ID != nil {
		u.ID = *gu.ID
	}
	if gu.Mail != nil {
		u.Mail = *gu.Mail
	}
	if u.Username == "" {
		return nil, cberr.New(cberr.KindOther, "identify the current user", "",
			"the server did not return a username")
	}
	return u, nil
}

// ── spaces ───────────────────────────────────────────────────────────────────

type graphDrive struct {
	ID         *string `json:"id"`
	Name       string  `json:"name"`
	DriveType  *string `json:"driveType"`
	DriveAlias *string `json:"driveAlias"`
	Root       *struct {
		WebDavURL *string `json:"webDavUrl"`
	} `json:"root"`
	Quota *struct {
		Total     *int64 `json:"total"`
		Used      *int64 `json:"used"`
		Remaining *int64 `json:"remaining"`
	} `json:"quota"`
}

type graphCollection[T any] struct {
	Value []T `json:"value"`
}

// Spaces returns the caller's spaces, cached for the configured TTL. The
// listing backs both "cernbox space list" and every space-alias resolution, so
// caching it keeps a shell loop from re-fetching it on each invocation.
func (c *Client) Spaces(ctx context.Context) ([]Space, error) {
	c.spacesMu.Lock()
	defer c.spacesMu.Unlock()

	if c.spacesOnce && time.Since(c.spacesAt) < c.spacesTTL {
		return c.spaces, c.spacesErr
	}

	var coll graphCollection[graphDrive]
	err := c.getJSON(ctx, c.URL(graphBeta+"/me/drives"), "list spaces", "", &coll)
	if err != nil {
		c.spaces, c.spacesErr, c.spacesAt, c.spacesOnce = nil, err, time.Now(), true
		return nil, err
	}

	spaces := make([]Space, 0, len(coll.Value))
	for _, d := range coll.Value {
		s := Space{Name: d.Name}
		if d.ID != nil {
			s.ID = *d.ID
		}
		if d.DriveType != nil {
			s.Type = *d.DriveType
		}
		if d.DriveAlias != nil {
			s.Alias = *d.DriveAlias
			s.Path = cleanPath("/" + *d.DriveAlias)
		}
		if d.Root != nil && d.Root.WebDavURL != nil {
			s.WebDavURL = *d.Root.WebDavURL
		}
		if d.Quota != nil {
			if d.Quota.Total != nil {
				s.QuotaTotal = *d.Quota.Total
			}
			if d.Quota.Used != nil {
				s.QuotaUsed = *d.Quota.Used
			}
			if d.Quota.Remaining != nil {
				s.QuotaRemaining = *d.Quota.Remaining
			}
		}
		spaces = append(spaces, s)
	}

	c.spaces, c.spacesErr, c.spacesAt, c.spacesOnce = spaces, nil, time.Now(), true
	return spaces, nil
}

// ResolveSpace maps a space alias to the absolute path of that space's root,
// implementing pathspec.SpaceResolver.
//
// Several spellings are accepted, because users arrive from different places:
// "home" for the personal space, "project/<name>" or just "<name>" for a
// project, the server's own drive alias, and the raw space id that a script
// might have stored.
func (c *Client) ResolveSpace(ctx context.Context, alias string) (string, error) {
	spaces, err := c.Spaces(ctx)
	if err != nil {
		return "", err
	}

	want := strings.ToLower(strings.Trim(alias, "/"))
	projectName := strings.TrimPrefix(want, "project/")

	var personal *Space
	for i := range spaces {
		s := &spaces[i]
		if s.Type == "personal" && personal == nil {
			personal = s
		}
		switch {
		case strings.EqualFold(s.ID, alias),
			strings.EqualFold(s.Alias, want),
			strings.EqualFold(s.Name, want):
			return spaceRoot(s)
		case s.Type == "project" && strings.EqualFold(s.Name, projectName):
			return spaceRoot(s)
		}
	}

	if want == "home" || want == "personal" {
		if personal == nil {
			return "", cberr.New(cberr.KindNotFound, "resolve space", alias,
				"you have no personal space on this instance")
		}
		return spaceRoot(personal)
	}

	return "", cberr.New(cberr.KindNotFound, "resolve space", alias,
		fmt.Sprintf("no space named %q — run 'cernbox space list' to see what you have", alias))
}

func spaceRoot(s *Space) (string, error) {
	if s.Path == "" {
		return "", cberr.New(cberr.KindOther, "resolve space", s.Name,
			"the server did not report a path for this space")
	}
	return s.Path, nil
}

// SpaceAlias returns the alias the CLI displays for a space.
func SpaceAlias(s Space) string {
	switch s.Type {
	case "personal":
		return "home"
	case "project":
		return "project/" + s.Name
	default:
		if s.Alias != "" {
			return s.Alias
		}
		return s.Name
	}
}

// ── shares and links ─────────────────────────────────────────────────────────

// splitResourceID splits a stringified resource id, "storage$space!item", into
// the space id the Graph route needs and the item id.
func splitResourceID(id string) (spaceID, itemID string, ok bool) {
	spaceID, itemID, ok = strings.Cut(id, "!")
	if !ok || spaceID == "" || itemID == "" {
		return "", "", false
	}
	return spaceID, id, true
}

func (c *Client) itemURL(resourceID, suffix string) (string, error) {
	spaceID, fullID, ok := splitResourceID(resourceID)
	if !ok {
		return "", cberr.New(cberr.KindOther, "address resource", resourceID,
			"the server returned a resource id in a form this client does not understand")
	}
	return c.URL(fmt.Sprintf("%s/drives/%s/items/%s%s",
		graphBeta, url.PathEscape(spaceID), url.PathEscape(fullID), suffix)), nil
}

// Recipient names a share target.
type Recipient struct {
	// ID is the username or group name.
	ID string
	// Type is "user" or "group".
	Type string
}

type inviteRequest struct {
	Recipients []inviteRecipient `json:"recipients"`
	Roles      []string          `json:"roles"`
	// Expiration is omitted entirely when unset, because ocgraph decodes the
	// body with DisallowUnknownFields and a null would be rejected.
	Expiration *time.Time `json:"expirationDateTime,omitempty"`
}

type inviteRecipient struct {
	ObjectID string `json:"objectId"`
	Type     string `json:"@libre.graph.recipient.type"`
}

type graphPermission struct {
	ID          *string `json:"id"`
	Roles       []string
	GrantedToV2 *struct {
		User  *graphIdentity `json:"user"`
		Group *graphIdentity `json:"group"`
	} `json:"grantedToV2"`
	GrantedTo *struct {
		User  *graphIdentity `json:"user"`
		Group *graphIdentity `json:"group"`
	} `json:"grantedTo"`
	Link *struct {
		Type             *string `json:"type"`
		WebURL           *string `json:"webUrl"`
		PreventsDownload *bool   `json:"preventsDownload"`
		HasPassword      *bool   `json:"@libre.graph.permissions.link.hasPassword"`
	} `json:"link"`
	ExpirationDateTime *time.Time `json:"expirationDateTime"`
}

type graphIdentity struct {
	ID          *string `json:"id"`
	DisplayName *string `json:"displayName"`
}

func (p graphPermission) toPermission() Permission {
	out := Permission{}
	if p.ID != nil {
		out.ID = *p.ID
	}
	if len(p.Roles) > 0 {
		out.Role = RoleName(p.Roles[0])
	}
	granted := p.GrantedToV2
	if granted == nil {
		granted = p.GrantedTo
	}
	if granted != nil {
		switch {
		case granted.User != nil:
			out.GrantedTo = identityOf(granted.User, "user")
		case granted.Group != nil:
			out.GrantedTo = identityOf(granted.Group, "group")
		}
	}
	if p.Link != nil {
		l := &Link{}
		if p.Link.Type != nil {
			l.Type = *p.Link.Type
		}
		if p.Link.WebURL != nil {
			l.URL = *p.Link.WebURL
		}
		if p.Link.HasPassword != nil {
			l.HasPassword = *p.Link.HasPassword
		}
		out.Link = l
	}
	out.ExpiresAt = p.ExpirationDateTime
	return out
}

func identityOf(id *graphIdentity, kind string) *Identity {
	out := &Identity{Type: kind}
	if id.ID != nil {
		out.ID = *id.ID
	}
	if id.DisplayName != nil {
		out.DisplayName = *id.DisplayName
	}
	return out
}

// UnmarshalJSON decodes the roles array, which libregraph names "roles".
func (p *graphPermission) UnmarshalJSON(b []byte) error {
	type alias graphPermission
	var raw struct {
		alias
		Roles []string `json:"roles"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*p = graphPermission(raw.alias)
	p.Roles = raw.Roles
	return nil
}

// Share grants recipients a role on a resource.
func (c *Client) Share(ctx context.Context, resourceID string, recipients []Recipient, role string, expiry *time.Time) ([]Permission, error) {
	u, err := c.itemURL(resourceID, "/invite")
	if err != nil {
		return nil, err
	}
	req := inviteRequest{Roles: []string{role}, Expiration: expiry}
	for _, r := range recipients {
		req.Recipients = append(req.Recipients, inviteRecipient{ObjectID: r.ID, Type: r.Type})
	}

	var coll graphCollection[graphPermission]
	if err := c.postJSON(ctx, u, "share", "", req, &coll); err != nil {
		return nil, err
	}
	return toPermissions(coll.Value), nil
}

// ListPermissions returns the shares and links on a resource.
func (c *Client) ListPermissions(ctx context.Context, resourceID string) ([]Permission, error) {
	u, err := c.itemURL(resourceID, "/permissions")
	if err != nil {
		return nil, err
	}
	var coll graphCollection[graphPermission]
	if err := c.getJSON(ctx, u, "list shares", "", &coll); err != nil {
		return nil, err
	}
	return toPermissions(coll.Value), nil
}

// UpdatePermission changes the role, the expiry, or both on an existing share.
func (c *Client) UpdatePermission(ctx context.Context, resourceID, permissionID, role string, expiry *time.Time) (*Permission, error) {
	u, err := c.itemURL(resourceID, "/permissions/"+url.PathEscape(permissionID))
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if role != "" {
		body["roles"] = []string{role}
	}
	if expiry != nil {
		body["expirationDateTime"] = expiry
	}
	if len(body) == 0 {
		return nil, cberr.Usagef("nothing to update: pass --role or --expiry")
	}

	var out graphPermission
	if err := c.sendJSON(ctx, http.MethodPatch, u, "update share", permissionID, body, &out); err != nil {
		return nil, err
	}
	p := out.toPermission()
	return &p, nil
}

// RemovePermission deletes a share or link.
func (c *Client) RemovePermission(ctx context.Context, resourceID, permissionID string) error {
	u, err := c.itemURL(resourceID, "/permissions/"+url.PathEscape(permissionID))
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, request{
		method:  http.MethodDelete,
		url:     u,
		op:      "remove share",
		path:    permissionID,
		expects: []int{http.StatusNoContent, http.StatusOK},
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// LinkOptions configures a public link.
type LinkOptions struct {
	// Type is "view" or "edit".
	Type        string
	DisplayName string
	Password    string
	Expiry      *time.Time
}

// CreateLink creates a public link on a resource.
func (c *Client) CreateLink(ctx context.Context, resourceID string, opts LinkOptions) (*Permission, error) {
	u, err := c.itemURL(resourceID, "/createLink")
	if err != nil {
		return nil, err
	}
	body := map[string]any{"type": opts.Type}
	if opts.DisplayName != "" {
		body["displayName"] = opts.DisplayName
	}
	if opts.Password != "" {
		body["password"] = opts.Password
	}
	if opts.Expiry != nil {
		body["expirationDateTime"] = opts.Expiry
	}

	var out graphPermission
	if err := c.postJSON(ctx, u, "create link", "", body, &out); err != nil {
		return nil, err
	}
	p := out.toPermission()
	return &p, nil
}

// SetLinkPassword sets or clears the password on an existing link.
func (c *Client) SetLinkPassword(ctx context.Context, resourceID, permissionID, password string) error {
	u, err := c.itemURL(resourceID, "/permissions/"+url.PathEscape(permissionID)+"/setPassword")
	if err != nil {
		return err
	}
	return c.postJSON(ctx, u, "set link password", permissionID,
		map[string]any{"password": password}, nil)
}

// DriveItem is an entry in a shared-with-me or shared-by-me listing.
type DriveItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
	// Path is the CERNBox path when the server reports one.
	Path string `json:"path,omitempty"`
	// SharedBy is who granted the share, for received shares.
	SharedBy *Identity `json:"shared_by,omitempty"`
	// Role is the role the caller holds on the item.
	Role string `json:"role,omitempty"`
	// Accepted reports whether a received share has been accepted.
	Accepted bool `json:"accepted"`
	// Federated reports whether the share came from another OCM provider.
	Federated bool `json:"federated,omitempty"`
}

type graphDriveItem struct {
	ID              *string `json:"id"`
	Name            *string `json:"name"`
	Size            *int64  `json:"size"`
	ParentReference *struct {
		Path *string `json:"path"`
	} `json:"parentReference"`
	RemoteItem *struct {
		ID   *string `json:"id"`
		Name *string `json:"name"`
		Size *int64  `json:"size"`
	} `json:"remoteItem"`
	UIHidden          *bool             `json:"@UI.Hidden"`
	ClientSynchronize *bool             `json:"@client.synchronize"`
	Permissions       []graphPermission `json:"permissions"`
	CreatedBy         *struct {
		User *graphIdentity `json:"user"`
	} `json:"createdBy"`
}

func (d graphDriveItem) toDriveItem() DriveItem {
	out := DriveItem{}
	if d.ID != nil {
		out.ID = *d.ID
	}
	if d.Name != nil {
		out.Name = *d.Name
	}
	if d.Size != nil {
		out.Size = *d.Size
	}
	if d.RemoteItem != nil {
		if out.Name == "" && d.RemoteItem.Name != nil {
			out.Name = *d.RemoteItem.Name
		}
		if out.Size == 0 && d.RemoteItem.Size != nil {
			out.Size = *d.RemoteItem.Size
		}
		if d.RemoteItem.ID != nil {
			out.ID = *d.RemoteItem.ID
			// reva encodes the id of a received OCM share with this prefix, so
			// it is what distinguishes a federated share from a local one in a
			// listing that holds both.
			out.Federated = strings.HasPrefix(*d.RemoteItem.ID, OCMReceivedPrefix)
		}
	}
	if d.ParentReference != nil && d.ParentReference.Path != nil {
		out.Path = *d.ParentReference.Path
	}
	if d.CreatedBy != nil && d.CreatedBy.User != nil {
		out.SharedBy = identityOf(d.CreatedBy.User, "user")
	}
	if len(d.Permissions) > 0 && len(d.Permissions[0].Roles) > 0 {
		out.Role = RoleName(d.Permissions[0].Roles[0])
	}
	// A received share that has been accepted is synchronised and not hidden.
	out.Accepted = d.ClientSynchronize != nil && *d.ClientSynchronize
	return out
}

// SharedWithMe lists shares other people have granted to the caller.
func (c *Client) SharedWithMe(ctx context.Context) ([]DriveItem, error) {
	return c.driveItems(ctx, c.URL(graphBeta+"/me/drive/sharedWithMe"), "list received shares")
}

// SharedByMe lists shares the caller has granted to other people.
func (c *Client) SharedByMe(ctx context.Context) ([]DriveItem, error) {
	return c.driveItems(ctx, c.URL(graphBeta+"/me/drive/sharedByMe"), "list shares")
}

func (c *Client) driveItems(ctx context.Context, u, op string) ([]DriveItem, error) {
	var coll graphCollection[graphDriveItem]
	if err := c.getJSON(ctx, u, op, "", &coll); err != nil {
		return nil, err
	}
	out := make([]DriveItem, 0, len(coll.Value))
	for _, item := range coll.Value {
		out = append(out, item.toDriveItem())
	}
	return out, nil
}

// SetReceivedShareState accepts or declines a received share.
func (c *Client) SetReceivedShareState(ctx context.Context, resourceID string, accept bool) error {
	u, err := c.itemURL(resourceID, "")
	if err != nil {
		return err
	}
	body := map[string]any{
		"@UI.Hidden":          !accept,
		"@client.synchronize": accept,
	}
	return c.sendJSON(ctx, http.MethodPatch, u, "update received share", resourceID, body, nil)
}

func toPermissions(in []graphPermission) []Permission {
	out := make([]Permission, 0, len(in))
	for _, p := range in {
		out = append(out, p.toPermission())
	}
	return out
}

// ── JSON helpers ─────────────────────────────────────────────────────────────

func (c *Client) getJSON(ctx context.Context, u, op, path string, out any) error {
	resp, err := c.do(ctx, request{
		method: http.MethodGet,
		url:    u,
		header: http.Header{"Accept": []string{"application/json"}},
		op:     op,
		path:   path,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeJSON(resp.Body, out, op, path)
}

func (c *Client) postJSON(ctx context.Context, u, op, path string, in, out any) error {
	return c.sendJSON(ctx, http.MethodPost, u, op, path, in, out)
}

func (c *Client) sendJSON(ctx context.Context, method, u, op, path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return cberr.Wrap(cberr.KindOther, op, path, err)
	}
	resp, err := c.do(ctx, request{
		method: method,
		url:    u,
		header: http.Header{
			"Content-Type": []string{"application/json"},
			"Accept":       []string{"application/json"},
		},
		body: bytesBody(payload),
		op:   op,
		path: path,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	return decodeJSON(resp.Body, out, op, path)
}

func decodeJSON(r io.Reader, out any, op, path string) error {
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(r).Decode(out); err != nil {
		return cberr.Wrap(cberr.KindOther, op, path, fmt.Errorf("malformed server response: %w", err))
	}
	return nil
}
