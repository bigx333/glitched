package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// counted is the fake, counting the listings of its commands by workspace,
// each failing while fail is set.
type counted struct {
	*fake.Adapter
	mu    sync.Mutex
	asked map[string]int
	fail  error
}

func (c *counted) Commands(ctx context.Context, workspace string) ([]provider.Command, error) {
	c.mu.Lock()
	c.asked[workspace]++
	err := c.fail
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.Adapter.Commands(ctx, workspace)
}

func (c *counted) times(workspace string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.asked[workspace]
}

// silent is an adapter that can't list its commands.
type silent struct{ provider.Adapter }

func names(cmds []provider.Command) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, c.Name)
	}
	return out
}

func TestTheSocketListsAProvidersCommandsOncePerWorkspace(t *testing.T) {
	dir := stateDir(t)
	ad := &counted{Adapter: fake.New(nil), asked: map[string]int{}}
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Listen: []string{"127.0.0.1:0"},
		Providers: provider.Mux{"fake": ad, "silent": silent{fake.New(nil)}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	ctx := context.Background()

	for range 2 {
		cmds, err := local.ProviderCommands(ctx, "fake", "/work/a")
		if err != nil {
			t.Fatal(err)
		}
		if got := names(cmds); len(got) != 3 || got[2] != "fake:tidy" || cmds[0].ArgumentHint != "<instructions>" {
			t.Fatalf("fake's commands: %+v", cmds)
		}
	}
	if n := ad.times("/work/a"); n != 1 {
		t.Errorf("two asks for one workspace listed %d times, want once", n)
	}
	if _, err := local.ProviderCommands(ctx, "fake", "/work/b"); err != nil || ad.times("/work/b") != 1 {
		t.Errorf("another workspace: %v, listed %d times, want once", err, ad.times("/work/b"))
	}
	if cmds, err := local.ProviderCommands(ctx, "silent", "/work/a"); err != nil || len(cmds) != 0 {
		t.Errorf("a provider that can't list: %+v, %v; want none and no error", cmds, err)
	}
	if _, err := local.ProviderCommands(ctx, "nobody", "/work/a"); err == nil {
		t.Error("a provider the engine has no adapter for answered no error")
	}

	// A listing runs a CLI in whatever folder the caller names, so the
	// tailnet's callers don't get one.
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote(t, d, token).ProviderCommands(ctx, "fake", "/work/c"); err == nil || ad.times("/work/c") != 0 {
		t.Errorf("a paired token over TCP: %v, listed %d times; want it refused", err, ad.times("/work/c"))
	}
}

// answering is the fake, answering every ask with its prompt, but for
// "wait", which it never answers.
type answering struct{ *fake.Adapter }

func (answering) Ask(ctx context.Context, spec provider.AskSpec) (provider.AskResult, error) {
	if spec.Prompt == "wait" {
		<-ctx.Done()
		return provider.AskResult{}, ctx.Err()
	}
	return provider.AskResult{Text: "re: " + spec.Prompt, Model: "cheap"}, nil
}

func TestTheSocketAsksTheProviderNamed(t *testing.T) {
	dir := stateDir(t)
	d, err := Start(context.Background(), Config{Dir: dir, Hub: true, Listen: []string{"127.0.0.1:0"},
		Providers: provider.Mux{"fake": fake.New(nil), "answering": answering{fake.New(nil)}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	local := transport.NewEndpointClient(Socket(dir), api.ID{})
	defer local.Close()
	ctx := context.Background()

	if got, err := local.ProviderAsk(ctx, "answering", provider.AskSpec{Prompt: "hi", Timeout: 5 * time.Second}); err != nil || got != (provider.AskResult{Text: "re: hi", Model: "cheap"}) {
		t.Errorf("ask: %+v, %v", got, err)
	}
	if _, err := local.ProviderAsk(ctx, "fake", provider.AskSpec{Prompt: "hi"}); !errors.Is(err, provider.ErrUnsupported) {
		t.Errorf("a provider that can't answer: %v, want ErrUnsupported across the socket", err)
	}
	// The engine ends an ask at its timeout, which the caller's deadline
	// doesn't carry across the socket.
	waited := make(chan error, 1)
	go func() {
		_, err := local.ProviderAsk(ctx, "answering", provider.AskSpec{Prompt: "wait", Timeout: 50 * time.Millisecond})
		waited <- err
	}()
	select {
	case err := <-waited:
		if err == nil {
			t.Error("an ask past its timeout was answered")
		}
	case <-time.After(5 * time.Second):
		t.Error("an ask ran past its timeout")
	}
	// An ask spends the user's account, so the tailnet's callers don't get one.
	token, err := Pair(dir, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote(t, d, token).ProviderAsk(ctx, "answering", provider.AskSpec{Prompt: "hi", Timeout: 5 * time.Second}); err == nil {
		t.Error("a paired token over TCP was answered")
	}
}

func TestAListingIsAskedAgainOnceStaleOrFailed(t *testing.T) {
	ad := &counted{Adapter: fake.New(nil), asked: map[string]int{}, fail: errors.New("claude: not found")}
	at := time.Now()
	c := newCommandLists(context.Background(), provider.Mux{"fake": ad})
	c.now = func() time.Time { return at }
	ctx := context.Background()

	if _, err := c.Commands(ctx, "fake", "/w"); err == nil {
		t.Fatal("a failed listing answered no error")
	}
	ad.mu.Lock()
	ad.fail = nil
	ad.mu.Unlock()
	if cmds, err := c.Commands(ctx, "fake", "/w"); err != nil || len(cmds) != 3 || ad.times("/w") != 2 {
		t.Fatalf("after a failure: %+v, %v, listed %d times; want it listed again", cmds, err, ad.times("/w"))
	}
	at = at.Add(commandsFresh - time.Second)
	c.Commands(ctx, "fake", "/w")
	if n := ad.times("/w"); n != 2 {
		t.Errorf("a fresh listing was listed again (%d)", n)
	}
	at = at.Add(time.Second)
	c.Commands(ctx, "fake", "/w")
	if n := ad.times("/w"); n != 3 {
		t.Errorf("a stale listing listed %d times, want 3", n)
	}
}

func TestAsksWhileOneListsShareIt(t *testing.T) {
	ad := &held{counted: counted{Adapter: fake.New(nil), asked: map[string]int{}}, release: make(chan struct{})}
	c := newCommandLists(context.Background(), provider.Mux{"fake": ad})
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if cmds, err := c.Commands(context.Background(), "fake", "/w"); err != nil || len(cmds) != 3 {
				t.Errorf("a waiting ask: %+v, %v", cmds, err)
			}
		})
	}
	// One who gives up leaves the listing to the rest.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Commands(gone, "fake", "/w"); !errors.Is(err, context.Canceled) {
		t.Errorf("an ask given up: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	close(ad.release)
	wg.Wait()
	if n := ad.times("/w"); n != 1 {
		t.Errorf("asks at once listed %d times, want once", n)
	}
}

// held is counted, its listings waiting for release.
type held struct {
	counted
	release chan struct{}
}

func (h *held) Commands(ctx context.Context, workspace string) ([]provider.Command, error) {
	<-h.release
	return h.counted.Commands(ctx, workspace)
}
