package graph

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neutron-build/neutron/mail"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func serve(t *testing.T, handler http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.Client())
}

func TestExpiredDeltaTokenBecomesAReset(t *testing.T) {
	// Graph reports an aged-out delta token as 410 Gone. It has to reach
	// the engine as a reset, not an error, so it joins the one recovery
	// path shared with IMAP's UIDVALIDITY change and JMAP's
	// cannotCalculateChanges.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	a := New(srv.Client())
	changes, err := a.Sync(context.Background(), "inbox", mail.Cursor(srv.URL+"/stale"))
	if err != nil {
		t.Fatalf("expected a reset, got error: %v", err)
	}
	if !changes.Reset {
		t.Error("410 Gone did not produce a reset")
	}
}

func TestRemovedAnnotationBecomesADestroy(t *testing.T) {
	body := `{"value":[
		{"id":"AAA","subject":"live"},
		{"id":"BBB","@removed":{"reason":"deleted"}}
	],"@odata.deltaLink":"https://next"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	a := New(srv.Client())
	changes, err := a.Sync(context.Background(), "inbox", mail.Cursor(srv.URL+"/delta"))
	if err != nil {
		t.Fatal(err)
	}

	var destroyed, updated int
	for _, c := range changes.Changes {
		switch c.Kind {
		case mail.ChangeDestroyed:
			destroyed++
			if c.ID != mail.NativeMessageID(mail.ProviderGraph, "BBB") {
				t.Errorf("destroyed id = %s, want BBB", c.ID)
			}
		case mail.ChangeUpdated:
			updated++
		}
	}
	if destroyed != 1 || updated != 1 {
		t.Errorf("got %d destroyed and %d updated, want 1 each", destroyed, updated)
	}
}

func TestDeltaContinuationIsNeverComplete(t *testing.T) {
	// Only an initial enumeration can be authoritative. A delta
	// continuation reports changes, not contents — marking it complete
	// would make the engine sweep every message that simply had not
	// changed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[],"@odata.deltaLink":"https://next"}`))
	}))
	defer srv.Close()

	a := New(srv.Client())
	changes, err := a.Sync(context.Background(), "inbox", mail.Cursor(srv.URL+"/delta"))
	if err != nil {
		t.Fatal(err)
	}
	if changes.Complete {
		t.Error("a delta continuation was marked as a complete listing")
	}
	if changes.More {
		t.Error("a page with a deltaLink and no nextLink reported more pages")
	}
	if changes.Next != "https://next" {
		t.Errorf("Next = %q, want the deltaLink", changes.Next)
	}
}

func TestNextLinkPagesBeforeDeltaLink(t *testing.T) {
	// While a nextLink is present the run is still paging, and the cursor
	// must follow it rather than jumping to a deltaLink.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"value":[],"@odata.nextLink":"https://page2"}`))
	}))
	defer srv.Close()

	a := New(srv.Client())
	changes, err := a.Sync(context.Background(), "inbox", mail.Cursor(srv.URL+"/delta"))
	if err != nil {
		t.Fatal(err)
	}
	if !changes.More {
		t.Error("a page with a nextLink did not report more pages")
	}
	if changes.Next != "https://page2" {
		t.Errorf("Next = %q, want the nextLink", changes.Next)
	}
}

func TestMailboxesFollowsEveryNextLink(t *testing.T) {
	var pages []string
	a := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.RawQuery, "$select=id") {
			// Well-known alias lookups carry no nextLink.
			return respondJSON(`{"id":"x"}`)
		}
		pages = append(pages, r.URL.String())
		body := `{"value":[{"id":"inbox","displayName":"Inbox"}],"@odata.nextLink":"https://graph.test/page2"}`
		if len(pages) == 2 {
			body = `{"value":[{"id":"archive","displayName":"Archive"}]}`
		}
		return respondJSON(body)
	})})

	boxes, err := a.Mailboxes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 2 || boxes[0].ID != "inbox" || boxes[1].ID != "archive" {
		t.Fatalf("mailboxes = %+v, want both pages", boxes)
	}
	if len(pages) != 2 || pages[1] != "https://graph.test/page2" {
		t.Fatalf("listing requests = %v, want the nextLink page", pages)
	}
}

func respondJSON(body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestStatusMapping(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, mail.ErrReauthRequired},
		{http.StatusForbidden, mail.ErrReauthRequired},
		{http.StatusNotFound, mail.ErrNotFound},
		{http.StatusTooManyRequests, mail.ErrRateLimited},
		{http.StatusServiceUnavailable, mail.ErrRateLimited},
		{http.StatusGone, mail.ErrCursorInvalid},
	}
	for _, tt := range tests {
		resp := &http.Response{
			StatusCode: tt.status,
			Body:       http.NoBody,
		}
		err := statusError(resp)
		if !errors.Is(err, tt.want) {
			t.Errorf("status %d mapped to %v, want %v", tt.status, err, tt.want)
		}
	}
}

func TestSuccessStatusesAreNotErrors(t *testing.T) {
	for _, code := range []int{200, 201, 202, 204} {
		resp := &http.Response{StatusCode: code, Body: http.NoBody}
		if err := statusError(resp); err != nil {
			t.Errorf("status %d = %v, want nil", code, err)
		}
	}
}

func TestMailboxesTraversesNestedFoldersAndAliases(t *testing.T) {
	// Root lists one direct child plus a parent with children; roles must
	// come from well-known alias lookups, not display names (audit
	// GRAPH-01).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/mailFolders/inbox"):
			_, _ = w.Write([]byte(`{"id":"folder-inbox"}`))
		case strings.HasSuffix(r.URL.Path, "/mailFolders/sentitems"):
			_, _ = w.Write([]byte(`{"id":"folder-sent"}`))
		case strings.HasSuffix(r.URL.Path, "/mailFolders/drafts"):
			_, _ = w.Write([]byte(`{"id":"folder-drafts"}`))
		case strings.HasSuffix(r.URL.Path, "/mailFolders/deleteditems"):
			_, _ = w.Write([]byte(`{"id":"folder-trash"}`))
		case strings.HasSuffix(r.URL.Path, "/mailFolders/junkemail"):
			_, _ = w.Write([]byte(`{"id":"folder-junk"}`))
		case strings.HasSuffix(r.URL.Path, "/mailFolders/archive"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"ErrorFolderNotFound"}}`))
		case strings.Contains(r.URL.Path, "/childFolders"):
			_, _ = w.Write([]byte(`{"value":[{"id":"nested-1","displayName":"Deep","parentFolderId":"parent-1","childFolderCount":0}]}`))
		case strings.HasSuffix(r.URL.Path, "/mailFolders"):
			_, _ = w.Write([]byte(`{"value":[
				{"id":"folder-inbox","displayName":"Posteingang","parentFolderId":"","childFolderCount":0},
				{"id":"folder-sent","displayName":"Gesendete Objekte","parentFolderId":"","childFolderCount":0},
				{"id":"parent-1","displayName":"Archive","parentFolderId":"","childFolderCount":1}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldBase := baseURL
	baseURL = srv.URL
	t.Cleanup(func() { baseURL = oldBase })
	a := New(srv.Client())

	boxes, err := a.Mailboxes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[mail.MailboxID]mail.Mailbox{}
	for _, b := range boxes {
		byID[b.ID] = b
	}
	if len(byID) != 4 {
		t.Fatalf("got %d folders (%v), want 4 including the nested one", len(byID), byID)
	}
	nested, ok := byID["nested-1"]
	if !ok {
		t.Fatal("nested folder was not discovered")
	}
	if nested.ParentID != "parent-1" {
		t.Errorf("nested parent = %q", nested.ParentID)
	}
	if byID["folder-inbox"].Role != mail.RoleInbox {
		t.Errorf("localised inbox display name lost its role: %+v", byID["folder-inbox"])
	}
	if byID["folder-sent"].Role != mail.RoleSent {
		t.Errorf("sent role = %q", byID["folder-sent"].Role)
	}
	if _, hasArchive := byID["folder-archive"]; hasArchive {
		t.Error("a 404 alias lookup materialized a folder")
	}
}

// A traversal failure mid-walk must surface as an error, never as a
// partial-but-authoritative listing the store would prune to.
func TestMailboxesFailsOnTraversalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "?") {
			return
		}
		if strings.Contains(r.URL.Path, "/mailFolders/") && !strings.Contains(r.URL.Path, "/childFolders") {
			_, _ = w.Write([]byte(`{"id":"x"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/childFolders") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"value":[{"id":"parent-1","displayName":"P","parentFolderId":"","childFolderCount":1}]}`))
	}))
	t.Cleanup(srv.Close)
	oldBase := baseURL
	baseURL = srv.URL
	t.Cleanup(func() { baseURL = oldBase })
	a := New(srv.Client())
	if _, err := a.Mailboxes(context.Background()); err == nil {
		t.Fatal("a failed child traversal returned a successful folder list")
	}
}

// Attachment metadata is paginated; every page must land and a failure must
// fail the body rather than caching an attachment-less "complete" copy
// (audit GRAPH-03).
func TestBodyFollowsAttachmentPaginationAndFailsHard(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/attachments") {
			if strings.Contains(r.URL.RawQuery, "page=2") || strings.HasSuffix(r.URL.Path, "page2") {
				_, _ = w.Write([]byte(`{"value":[{"id":"att-2","name":"b.txt","contentType":"text/plain","size":2}]}`))
				return
			}
			next := srv.URL + r.URL.Path + "?$select=id,name,contentType,size,isInline,contentId&page=2"
			_, _ = w.Write([]byte(`{"value":[{"id":"att-1","name":"a.txt","contentType":"text/plain","size":1}],"@odata.nextLink":"` + next + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"body":{"contentType":"text","content":"hi"}}`))
	}))
	t.Cleanup(srv.Close)
	oldBase := baseURL
	baseURL = srv.URL
	t.Cleanup(func() { baseURL = oldBase })
	a := New(srv.Client())

	body, err := a.Body(context.Background(), mail.NativeMessageID(mail.ProviderGraph, "m1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Parts) != 2 {
		t.Fatalf("got %d attachment parts, want both pages", len(body.Parts))
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/attachments") {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"body":{"contentType":"text","content":"hi"}}`))
	}))
	t.Cleanup(srv2.Close)
	baseURL = srv2.URL
	if _, err := a.Body(context.Background(), mail.NativeMessageID(mail.ProviderGraph, "m1")); err == nil {
		t.Fatal("an attachment-metadata failure produced a successful body")
	}
}

// Only seen/flagged have real single-property mappings; an arbitrary
// keyword must be refused rather than overwriting the whole category
// collection (audit GRAPH-04).
func TestApplyRejectsArbitraryKeywords(t *testing.T) {
	a := serve(t, func(w http.ResponseWriter, r *http.Request) {})
	err := a.Apply(context.Background(), mail.Operation{
		Kind: mail.OpAddKeyword, Keyword: "custom",
		IDs: []mail.MessageID{mail.NativeMessageID(mail.ProviderGraph, "m1")},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("arbitrary keyword mutation was accepted: %v", err)
	}
}

func TestToEnvelopeNormalisesFlags(t *testing.T) {
	m := graphMessage{
		ID:                "AAA",
		ConversationID:    "conv-1",
		Subject:           "Hello",
		IsRead:            true,
		IsDraft:           false,
		InternetMessageID: "<abc@example.com>",
		ParentFolderID:    "inbox-id",
	}
	m.Flag = &struct {
		FlagStatus string `json:"flagStatus"`
	}{FlagStatus: "flagged"}
	m.From = &graphRecipient{}
	m.From.EmailAddress.Name = "Alice"
	m.From.EmailAddress.Address = "alice@example.com"

	env := m.toEnvelope()

	if !env.Keywords.Seen {
		t.Error("isRead did not map to Seen")
	}
	if !env.Keywords.Flagged {
		t.Error("flagStatus flagged did not map to Flagged")
	}
	if env.Keywords.Draft {
		t.Error("Draft set despite isDraft false")
	}
	if len(env.From) != 1 || env.From[0].Email != "alice@example.com" {
		t.Errorf("From = %+v, want alice@example.com", env.From)
	}
	if env.From[0].Name != "Alice" {
		t.Errorf("From name = %q, want Alice", env.From[0].Name)
	}
	if env.ThreadID != "conv-1" {
		t.Errorf("ThreadID = %q, want conv-1", env.ThreadID)
	}
	if len(env.MailboxIDs) != 1 || env.MailboxIDs[0] != "inbox-id" {
		t.Errorf("MailboxIDs = %v, want [inbox-id]", env.MailboxIDs)
	}
	if env.Fingerprint == "" {
		t.Error("fingerprint was not computed")
	}
}

func TestKeywordPatchInvertsCorrectly(t *testing.T) {
	seen := keywordPatch(mail.Operation{Kind: mail.OpAddKeyword, Keyword: "seen"})
	if seen["isRead"] != true {
		t.Errorf("add seen = %v, want isRead true", seen)
	}
	unseen := keywordPatch(mail.Operation{Kind: mail.OpRemoveKeyword, Keyword: "seen"})
	if unseen["isRead"] != false {
		t.Errorf("remove seen = %v, want isRead false", unseen)
	}

	flagged := keywordPatch(mail.Operation{Kind: mail.OpAddKeyword, Keyword: "flagged"})
	flag, ok := flagged["flag"].(map[string]any)
	if !ok || flag["flagStatus"] != "flagged" {
		t.Errorf("add flagged = %v, want flagStatus flagged", flagged)
	}
}

func TestNativeIDStripsThePrefix(t *testing.T) {
	id := mail.NativeMessageID(mail.ProviderGraph, "AAMkAGI2")
	if got := nativeID(id); got != "AAMkAGI2" {
		t.Errorf("nativeID = %q, want AAMkAGI2", got)
	}
	// An identity from another provider must not be silently mangled.
	other := mail.NativeMessageID(mail.ProviderGmail, "xyz")
	if got := nativeID(other); !strings.Contains(got, "gmail") {
		t.Errorf("nativeID stripped a foreign provider prefix: %q", got)
	}
}
