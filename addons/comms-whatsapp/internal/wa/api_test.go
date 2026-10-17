package wa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
	yardlogs "github.com/shipyard-auto/shipyard/internal/logs"
	"github.com/shipyard-auto/shipyard/internal/signing"
)

const testAPIToken = "wat_test"

var (
	promoA = "120363000000000010@g.us"
	promoB = "120363000000000020@g.us"
	promoC = "120363000000000030@g.us"
)

type apiFixture struct {
	arch *archive.Store
	srv  *httptest.Server
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	ctx := context.Background()
	arch, err := archive.Open(ctx, filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = arch.Close() })
	for _, c := range []struct {
		jid, name string
		kind      archive.ChatKind
	}{{promoA, "Promos Pet", archive.KindGroup}, {promoB, "Promos Viagem", archive.KindGroup}, {viniJID.String(), "Vini Eduqo", archive.KindDM}} {
		mustDo(t, arch.UpsertChat(ctx, c.jid, c.name, c.kind))
		mustDo(t, arch.Watch(ctx, c.jid, 0))
	}
	api := &APIServer{
		Archive: arch,
		Token:   func() (string, error) { return testAPIToken, nil },
		Self:    func() types.JID { return ownPN },
		Log:     slog.New(yardlogs.NopHandler()),
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &apiFixture{arch: arch, srv: srv}
}

func (f *apiFixture) add(t *testing.T, chat, id string, ts time.Time, text string) {
	t.Helper()
	if _, err := f.arch.InsertMessage(context.Background(), archive.Message{
		ChatJID: chat, ID: id, SenderJID: viniJID.String(), SenderName: "Vini", Timestamp: ts, Kind: "text", Text: text,
	}); err != nil {
		t.Fatal(err)
	}
}

// call POSTs body as client with scopes (fairway's forwarded identity).
func (f *apiFixture) call(t *testing.T, path, client, scopes string, body any, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	if client != "" {
		req.Header.Set("X-Shipyard-Client-Id", client)
		req.Header.Set("X-Shipyard-Client-Scopes", scopes)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func ids(evs []Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.ID)
	}
	return strings.Join(s, ",")
}

func TestAPIRejectsBadTokenMissingIdentityAndMethod(t *testing.T) {
	f := newAPIFixture(t)
	// No token / wrong token.
	for _, auth := range []string{"", "Bearer nope", testAPIToken} {
		req, _ := http.NewRequest(http.MethodPost, f.srv.URL+APIMessagesPath, strings.NewReader("{}"))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.Header.Set("X-Shipyard-Client-Id", "promo")
		req.Header.Set("X-Shipyard-Client-Scopes", "whatsapp.read")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("auth %q → %d", auth, resp.StatusCode)
		}
	}
	// Token but no identity: the fairway route is not signed.
	var e apiErrorBody
	if code := f.call(t, APIMessagesPath, "", "", map[string]any{}, &e); code != http.StatusForbidden || !strings.Contains(e.Error.Message, "--auth signed") {
		t.Fatalf("no identity → %d %+v", code, e)
	}
	resp, err := http.Get(f.srv.URL + APIMessagesPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET → %d", resp.StatusCode)
	}
}

func TestAPIMessagesScopedToGrantedChats(t *testing.T) {
	f := newAPIFixture(t)
	f.add(t, promoA, "a1", t0Send, "ração 30% off")
	f.add(t, promoB, "b1", t0Send.Add(time.Minute), "passagem barata")
	f.add(t, viniJID.String(), "v1", t0Send.Add(2*time.Minute), "fala!")
	f.add(t, promoA, "a2", t0Send.Add(3*time.Minute), "areia 2x1")

	var r MessagesResponse
	if code := f.call(t, APIMessagesPath, "promo", "events.subscribe:whatsapp.message,whatsapp.read:"+promoA, MessagesRequest{}, &r); code != 200 {
		t.Fatalf("code=%d", code)
	}
	if ids(r.Messages) != "a1,a2" || r.HasMore || r.NextCursor == "" {
		t.Fatalf("scoped read=%s has_more=%v cursor=%q", ids(r.Messages), r.HasMore, r.NextCursor)
	}
	ev := r.Messages[0]
	if ev.SchemaVersion != 1 || ev.Channel != "whatsapp" || ev.Body != "ração 30% off" || ev.From.Raw != viniJID.String() ||
		ev.To[0].Raw != ownPN.String() || ev.Metadata["chat_jid"] != promoA || ev.Metadata["chat_name"] != "Promos Pet" ||
		ev.Metadata["chat_kind"] != "group" || !ev.Timestamp.Equal(t0Send) {
		t.Fatalf("envelope=%+v", ev)
	}

	// Globs and full access.
	r = MessagesResponse{}
	f.call(t, APIMessagesPath, "promo", "whatsapp.read:120363*@g.us", MessagesRequest{}, &r)
	if ids(r.Messages) != "a1,b1,a2" {
		t.Fatalf("glob=%s", ids(r.Messages))
	}
	r = MessagesResponse{}
	f.call(t, APIMessagesPath, "admin", "whatsapp.read", MessagesRequest{}, &r)
	if ids(r.Messages) != "a1,b1,v1,a2" {
		t.Fatalf("all=%s", ids(r.Messages))
	}
	// No read scope at all: empty, not an error.
	r = MessagesResponse{}
	if code := f.call(t, APIMessagesPath, "other", "cron.run", MessagesRequest{}, &r); code != 200 || len(r.Messages) != 0 {
		t.Fatalf("no scope → %d %s", code, ids(r.Messages))
	}
}

func TestAPIMessagesCursorPaging(t *testing.T) {
	f := newAPIFixture(t)
	for i := range 5 {
		f.add(t, promoA, fmt.Sprintf("a%d", i), t0Send.Add(time.Duration(i)*time.Minute), "x")
	}
	f.add(t, promoB, "b0", t0Send, "hidden")
	scopes := "whatsapp.read:" + promoA

	var got []string
	req := MessagesRequest{Limit: 2}
	for page := 0; ; page++ {
		var r MessagesResponse
		if code := f.call(t, APIMessagesPath, "promo", scopes, req, &r); code != 200 {
			t.Fatalf("page %d code=%d", page, code)
		}
		got = append(got, ids(r.Messages))
		if !r.HasMore {
			// The cursor is still returned, ready for the next poll.
			req.After = r.NextCursor
			break
		}
		req.After = r.NextCursor
		if page > 5 {
			t.Fatal("paging does not end")
		}
	}
	if strings.Join(got, "|") != "a0,a1|a2,a3|a4" {
		t.Fatalf("pages=%v", got)
	}
	// Polling again: nothing new; a new message then shows up exactly once.
	var r MessagesResponse
	f.call(t, APIMessagesPath, "promo", scopes, req, &r)
	if len(r.Messages) != 0 || r.NextCursor != req.After {
		t.Fatalf("idle poll=%s cursor moved=%v", ids(r.Messages), r.NextCursor != req.After)
	}
	f.add(t, promoA, "a5", t0Send.Add(time.Hour), "nova")
	f.add(t, promoB, "b1", t0Send.Add(time.Hour), "hidden")
	r = MessagesResponse{}
	f.call(t, APIMessagesPath, "promo", scopes, req, &r)
	if ids(r.Messages) != "a5" {
		t.Fatalf("catch-up=%s", ids(r.Messages))
	}
	// Hidden rows advance the cursor too.
	next := r.NextCursor
	r = MessagesResponse{}
	f.call(t, APIMessagesPath, "promo", scopes, MessagesRequest{After: next}, &r)
	if len(r.Messages) != 0 || r.NextCursor != next {
		t.Fatalf("after hidden tail=%s", ids(r.Messages))
	}
}

func TestAPIMessagesFiltersAndValidation(t *testing.T) {
	f := newAPIFixture(t)
	now := time.Now().UTC()
	f.add(t, promoA, "old", now.Add(-48*time.Hour), "velha")
	f.add(t, promoA, "new", now.Add(-time.Hour), "nova")
	f.add(t, promoB, "b", now.Add(-time.Hour), "outra")

	var r MessagesResponse
	f.call(t, APIMessagesPath, "admin", "whatsapp.read:*", MessagesRequest{Since: "24h"}, &r)
	if ids(r.Messages) != "new,b" {
		t.Fatalf("since=%s", ids(r.Messages))
	}
	r = MessagesResponse{}
	f.call(t, APIMessagesPath, "admin", "whatsapp.read:*", MessagesRequest{Chat: "Promos Pet"}, &r)
	if ids(r.Messages) != "old,new" {
		t.Fatalf("chat by name=%s", ids(r.Messages))
	}
	r = MessagesResponse{}
	f.call(t, APIMessagesPath, "admin", "whatsapp.read", MessagesRequest{Since: now.Add(-2 * time.Hour).Format(time.RFC3339), Chat: promoA}, &r)
	if ids(r.Messages) != "new" {
		t.Fatalf("rfc3339+chat=%s", ids(r.Messages))
	}

	// A second chat with the same name makes the name ambiguous.
	mustDo(t, f.arch.UpsertChat(context.Background(), promoC, "Promos Pet", archive.KindGroup))
	var e apiErrorBody
	for name, tc := range map[string]struct {
		req  any
		code int
		err  string
	}{
		"bad cursor":    {MessagesRequest{After: "garbage"}, 400, "invalid"},
		"bad since":     {MessagesRequest{Since: "yesterday"}, 400, "invalid"},
		"limit":         {MessagesRequest{Limit: 501}, 400, "invalid"},
		"unknown field": {map[string]any{"chats": "x"}, 400, "invalid"},
		"unknown chat":  {MessagesRequest{Chat: "Ninguém"}, 404, "unknown_chat"},
		"ambiguous":     {MessagesRequest{Chat: "Promos Pet"}, 400, "ambiguous"},
	} {
		e = apiErrorBody{}
		if code := f.call(t, APIMessagesPath, "admin", "whatsapp.read", tc.req, &e); code != tc.code || e.Error.Code != tc.err {
			t.Errorf("%s → %d %+v", name, code, e)
		}
	}
}

func TestAPIChatPermissionDoesNotLeakCatalog(t *testing.T) {
	f := newAPIFixture(t)
	scopes := "whatsapp.read:" + promoA
	var e apiErrorBody
	// A chat the client cannot read and one that does not exist look alike.
	for _, chat := range []string{promoB, "Promos Viagem", "Ninguém"} {
		e = apiErrorBody{}
		if code := f.call(t, APIMessagesPath, "promo", scopes, MessagesRequest{Chat: chat}, &e); code != 403 || e.Error.Code != "forbidden" {
			t.Fatalf("%q → %d %+v", chat, code, e)
		}
	}
	// "Promos Pet" names two chats, but only one is visible: not ambiguous.
	mustDo(t, f.arch.UpsertChat(context.Background(), promoC, "Promos Pet", archive.KindGroup))
	var r MessagesResponse
	f.add(t, promoA, "a1", t0Send, "x")
	if code := f.call(t, APIMessagesPath, "promo", scopes, MessagesRequest{Chat: "promos pet"}, &r); code != 200 || ids(r.Messages) != "a1" {
		t.Fatalf("visible-only resolution → %d %s", code, ids(r.Messages))
	}
}

func TestAPIChats(t *testing.T) {
	f := newAPIFixture(t)
	var r ChatsResponse
	if code := f.call(t, APIChatsPath, "promo", "whatsapp.read:120363*", ChatsRequest{}, &r); code != 200 || len(r.Chats) != 2 {
		t.Fatalf("glob chats → %d %+v", code, r.Chats)
	}
	r = ChatsResponse{}
	f.call(t, APIChatsPath, "admin", "whatsapp.read", ChatsRequest{Kind: "dm"}, &r)
	if len(r.Chats) != 1 || r.Chats[0].JID != viniJID.String() {
		t.Fatalf("kind=dm → %+v", r.Chats)
	}
	r = ChatsResponse{}
	f.call(t, APIChatsPath, "admin", "whatsapp.read", ChatsRequest{Search: "viagem"}, &r)
	if len(r.Chats) != 1 || r.Chats[0].JID != promoB {
		t.Fatalf("search → %+v", r.Chats)
	}
	var e apiErrorBody
	if code := f.call(t, APIChatsPath, "admin", "whatsapp.read", ChatsRequest{Kind: "channel"}, &e); code != 400 {
		t.Fatalf("bad kind → %d", code)
	}
}

func TestScopeGrants(t *testing.T) {
	for _, tc := range []struct {
		scopes []string
		chat   string
		want   bool
	}{
		{[]string{"whatsapp.read"}, promoA, true},
		{[]string{"whatsapp.read:*"}, promoA, true},
		{[]string{"whatsapp.*"}, promoA, true},
		{[]string{"*"}, promoA, true},
		{[]string{"whatsapp.read:" + promoA}, promoA, true},
		{[]string{"whatsapp.read:" + promoA}, promoB, false},
		{[]string{"whatsapp.read:120363*@g.us"}, promoB, true},
		{[]string{"whatsapp.read:120363*@g.us"}, viniJID.String(), false},
		{[]string{"whatsapp.send:" + promoA}, promoA, false},
		{[]string{"whatsapp.readx"}, promoA, false},
		{nil, promoA, false},
	} {
		if got := signing.Grants(tc.scopes, ReadScopeName+":"+tc.chat); got != tc.want {
			t.Errorf("signing.Grants(%v, %s) = %v", tc.scopes, tc.chat, got)
		}
	}
	if !coversAll([]string{"whatsapp.read"}, ReadScopeName) || !coversAll([]string{"whatsapp.read:*"}, ReadScopeName) ||
		coversAll([]string{"whatsapp.read:120363*"}, ReadScopeName) || coversAll([]string{"whatsapp.read"}, SendScopeName) {
		t.Fatal("coversAll")
	}
}

func TestValidateAPIAddrAndToken(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:9877", "[::1]:9877", "localhost:9877"} {
		if err := ValidateAPIAddr(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:9877", "192.168.0.10:9877", ":9877", "127.0.0.1", "127.0.0.1:0", "example.com:80"} {
		if err := ValidateAPIAddr(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	path := filepath.Join(t.TempDir(), "api.token")
	if tok, err := ReadAPIToken(path); err != nil || tok != "" {
		t.Fatalf("missing token: %q %v", tok, err)
	}
	a, err := EnsureAPIToken(path, false)
	if err != nil || !strings.HasPrefix(a, "wat_") || len(a) != 4+64 {
		t.Fatalf("token=%q err=%v", a, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", st.Mode())
	}
	b, _ := EnsureAPIToken(path, false)
	c, _ := EnsureAPIToken(path, true)
	if a != b || c == a {
		t.Fatalf("ensure must keep, rotate must replace: %q %q %q", a, b, c)
	}
}

func TestServeAPIListensOnLoopbackAndStops(t *testing.T) {
	ctx := context.Background()
	if _, err := ServeAPI(ctx, "0.0.0.0:0", http.NotFoundHandler()); err == nil {
		t.Fatal("non-loopback bind accepted")
	}
	stop, err := ServeAPI(ctx, "127.0.0.1:18977", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	if err != nil {
		t.Skipf("port busy: %v", err)
	}
	resp, err := http.Post("http://127.0.0.1:18977/", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	stop()
	if _, err := http.Post("http://127.0.0.1:18977/", "application/json", nil); err == nil {
		t.Fatal("still serving after stop")
	}
}

func TestAPIAddrConfigKey(t *testing.T) {
	c := DefaultConfig()
	mustDo(t, c.Set("api-addr", "127.0.0.1:9877"))
	if c.APIAddr != "127.0.0.1:9877" {
		t.Fatal("set")
	}
	if err := c.Set("api-addr", "0.0.0.0:9877"); err == nil {
		t.Fatal("non-loopback accepted")
	}
	c.APIAddr = "127.0.0.1:9877"
	mustDo(t, c.Set("api-addr", "off"))
	if c.APIAddr != "" {
		t.Fatal("off")
	}
}

func TestAPIScanCapReturnsCursorThroughHiddenRows(t *testing.T) {
	f := newAPIFixture(t)
	old := apiMaxScan
	apiMaxScan = 3
	t.Cleanup(func() { apiMaxScan = old })
	for i := range 5 {
		f.add(t, promoB, fmt.Sprintf("b%d", i), t0Send, "hidden")
	}
	f.add(t, promoA, "a0", t0Send, "visible")
	scopes := "whatsapp.read:" + promoA
	var r MessagesResponse
	f.call(t, APIMessagesPath, "promo", scopes, MessagesRequest{}, &r)
	if len(r.Messages) != 0 || !r.HasMore || r.NextCursor == "" {
		t.Fatalf("first page=%s has_more=%v", ids(r.Messages), r.HasMore)
	}
	next := r.NextCursor
	r = MessagesResponse{}
	f.call(t, APIMessagesPath, "promo", scopes, MessagesRequest{After: next}, &r)
	if ids(r.Messages) != "a0" || r.HasMore {
		t.Fatalf("second page=%s has_more=%v", ids(r.Messages), r.HasMore)
	}
}
