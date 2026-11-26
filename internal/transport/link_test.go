package transport

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/hub"
)

// The roles whose interface has a method of its own name, so a struct can
// embed them only under another.
type (
	launchesRole = Launches
	skillsRole   = Skills
)

// linkEndpoint's info says which link asked, as the engine's does.
type linkEndpoint struct{ fakeEndpoint }

func (e *linkEndpoint) Info(ctx context.Context) (api.Environment, error) {
	l, _ := api.LinkOf(ctx)
	return api.Environment{Name: "b", Link: l.ID}, nil
}

// linkReplies answers child.reply for the linkEndpoint's link, as the
// engine checks its own scope.
type linkReplies struct{}

func (linkReplies) ChildReply(ctx context.Context, q api.ChildReplyQuery) (api.ChildReply, error) {
	if _, ok := api.LinkOf(ctx); !ok {
		return api.ChildReply{}, errors.New("no link")
	}
	return api.ChildReply{Text: "said"}, nil
}

// A link's socket reaches info, read, submit, subscribe and child.reply,
// each of which checks its scope in the endpoint, and nothing else: every
// other method a server with every role registers is refused before its
// handler runs, which would panic here, as each role is a nil interface.
func TestALinksSocketMayCallOnlyItsMethods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	link := api.Link{ID: api.NewID(), Label: "a", Access: api.AccessAutoEdits}
	srv := &Server{
		Transport: Tailnet{},
		Endpoint:  &linkEndpoint{},
		Hub:       &hub.Replica{},
		Plugins:   struct{ Plugins }{},
		Providers: struct{ Providers }{},
		Launches:  struct{ launchesRole }{},
		CLIs:      struct{ CLIs }{},
		Asks:      struct{ Asks }{},
		Secrets:   struct{ Secrets }{},
		Projects:  struct{ Projects }{},
		Skills:    struct{ skillsRole }{},
		Replies:   linkReplies{},
		Terminals: struct{ Terminals }{},
		Authorize: func(_ *http.Request, proved func(Key) bool) (Grant, error) {
			for _, token := range []string{"link", "full"} {
				if k := KeyOf(token); proved(k) {
					g := Grant{Key: k, Done: context.Background()}
					if token == "link" {
						g.Link = &link
					}
					return g, nil
				}
			}
			return Grant{}, errors.New("unknown token")
		},
	}
	addr := serveTailnet(t, srv)
	c := dial(Tailnet{Address: addr, Token: "link"}, api.ID{})
	defer c.close()

	var env api.Environment
	if err := c.call(ctx, "info", struct{}{}, &env); err != nil || env.Link != link.ID {
		t.Fatalf("info on a link's socket: %+v %v, want link %s", env, err, link.ID)
	}
	methods := srv.methods()
	var said api.ChildReply
	if err := c.call(ctx, "child.reply", api.ChildReplyQuery{Thread: api.NewID(), Run: api.NewID()}, &said); err != nil || said.Text != "said" {
		t.Fatalf("child.reply on a link's socket: %+v %v, want the endpoint's answer with the link in its ctx", said, err)
	}
	allowed := []string{"info", "read", "submit", "subscribe", "child.reply"}
	refused := []string{
		"plugins.tools", "plugins.tool", "plugins.call",
		"provider.commands", "provider.describe", "provider.default", "provider.ask", "provider.launches", "provider.launch",
		"provider.custom", "provider.custom.add", "provider.custom.remove",
		"providers", "provider.update", "credentials.ask", "credentials.provide", "credentials.forget",
		"projects.folders", "projects.repos", "projects.clone",
		"skills.list", "skills.sync", "skills.put", "skills.drop",
		"hub.append", "hub.acked", "hub.events", "hub.follow",
		"terminal.open", "terminal.input", "terminal.resize", "terminal.claim",
	}
	for _, name := range slices.Concat(allowed, refused) {
		if methods[name] == nil {
			t.Fatalf("the server registers no %s; the test means every role to be set", name)
		}
	}
	for name := range methods {
		if !slices.Contains(allowed, name) && !slices.Contains(refused, name) {
			t.Errorf("the server registers %s, which this test neither allows nor refuses a link; name it in one list", name)
		}
	}
	for _, name := range refused {
		if err := c.call(ctx, name, struct{}{}, nil); !errors.Is(err, api.ErrNotAllowed) {
			t.Errorf("%s on a link's socket: %v, want api.ErrNotAllowed", name, err)
		}
	}
	if err := c.call(ctx, "info", struct{}{}, &env); err != nil {
		t.Fatalf("info after the refusals: %v; a refusal must leave the socket open", err)
	}

	full := dial(Tailnet{Address: addr, Token: "full"}, api.ID{})
	defer full.close()
	var fullEnv api.Environment
	if err := full.call(ctx, "info", struct{}{}, &fullEnv); err != nil || fullEnv.Link != (api.ID{}) {
		t.Fatalf("info on a full token's socket: %+v %v, want no link", fullEnv, err)
	}
	var snap api.Snapshot
	if err := full.call(ctx, "read", api.Query{Thread: api.NewID()}, &snap); err != nil {
		t.Fatalf("read on a full token's socket: %v", err)
	}
}
