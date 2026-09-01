package cf

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A hostname protected by Access almost always has a path that cannot pass a
// door: a webhook a third party posts to, a probe, an API a CLI reaches with its
// own bearer token. Without a bypass decision the only choices are leaving the
// whole hostname open or breaking those calls.
func TestBypassPolicyAdmitsEveryone(t *testing.T) {
	p := Policy{Name: "public", Decision: DecisionBypass, Include: []Rule{IncludeEveryone()}}
	if err := p.Validate(); err != nil {
		t.Fatalf("a bypass policy must be valid: %v", err)
	}
	if got := p.Describe(); got != "tout le monde" {
		t.Errorf("Describe() = %q, want %q", got, "tout le monde")
	}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	// Cloudflare expects the empty object, not a boolean or a null.
	if !strings.Contains(string(body), `"everyone":{}`) {
		t.Errorf("everyone must serialise as an empty object: %s", body)
	}
}

// "everyone" under allow admits any identity from any provider, which reads
// like a restriction and is not one. Bypass is how you say "no door".
func TestEveryoneOutsideBypassIsRefused(t *testing.T) {
	p := Policy{Decision: DecisionAllow, Include: []Rule{IncludeEveryone()}}
	if err := p.Validate(); err == nil {
		t.Error("everyone under allow must be refused")
	}
	q := Policy{Decision: DecisionBypass, Include: []Rule{IncludeEmail("a@b.c")}}
	if err := q.Validate(); err == nil {
		t.Error("a bypass that names someone filters nothing and must be refused")
	}
}

// accessApps stubs the account's application list.
func accessApps(t *testing.T, domains ...string) *Client {
	t.Helper()
	items := make([]string, 0, len(domains))
	for i, d := range domains {
		items = append(items, `{"id":"id`+string(rune('0'+i))+`","name":"`+d+`","domain":"`+d+`"}`)
	}
	return stub(t, map[string]string{
		"GET /accounts/compte/access/apps": ok("[" + strings.Join(items, ",") + "]"),
	})
}

// The bare hostname and a path beneath it are different applications, and Access
// reads them from the most specific to the most general. Resolving the hostname
// to whichever path application happened to sit under it made `app create` on
// the hostname fail — pointing at a /health bypass as the thing already
// covering it — which is exactly the pattern the documentation recommends.
func TestAppByDomainPrefersTheExactName(t *testing.T) {
	c := accessApps(t, "app.example.com/health", "app.example.com", "app.example.com/mcp")
	got, err := c.AppByDomain(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("AppByDomain: %v", err)
	}
	if got.Domain != "app.example.com" {
		t.Errorf("AppByDomain resolved to %q, want the exact name", got.Domain)
	}
	exact, err := c.AppByExactDomain(context.Background(), "app.example.com")
	if err != nil || exact.Domain != "app.example.com" {
		t.Errorf("AppByExactDomain = %q, %v", exact.Domain, err)
	}
}

// A create on the hostname must not be refused because a path beneath it
// exists: exempting a webhook or a probe from a door is what those are for.
func TestAppByExactDomainIgnoresPathsBeneathIt(t *testing.T) {
	c := accessApps(t, "app.example.com/health", "app.example.com/mcp")
	if _, err := c.AppByExactDomain(context.Background(), "app.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the hostname is free while only paths beneath it exist, got %v", err)
	}
}

// Naming a hostname that resolves to several path applications is ambiguous, so
// it is refused rather than resolved to whichever came back first.
func TestAppByDomainRefusesAmbiguity(t *testing.T) {
	c := accessApps(t, "app.example.com/health", "app.example.com/mcp")
	_, err := c.AppByDomain(context.Background(), "app.example.com")
	if err == nil || !strings.Contains(err.Error(), "2 applications") {
		t.Errorf("two applications under one name must be refused, got %v", err)
	}
	// One is unambiguous, so naming the hostname alone still reaches it.
	single := accessApps(t, "app.example.com/mcp")
	got, err := single.AppByDomain(context.Background(), "app.example.com")
	if err != nil || got.Domain != "app.example.com/mcp" {
		t.Errorf("a single application under the name must still resolve, got %q %v", got.Domain, err)
	}
}
