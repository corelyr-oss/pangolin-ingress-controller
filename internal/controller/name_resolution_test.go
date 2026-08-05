package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// fakeRoleLister serves a mutable role list and counts calls so tests can
// assert on API traffic.
type fakeRoleLister struct {
	mu    sync.Mutex
	roles []pangolin.Role
	err   error
	calls int
}

func (f *fakeRoleLister) list(context.Context) ([]pangolin.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]pangolin.Role(nil), f.roles...), nil
}

func (f *fakeRoleLister) set(roles []pangolin.Role, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roles, f.err = roles, err
}

func (f *fakeRoleLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func fixedNow(at *time.Time) func() time.Time {
	return func() time.Time { return *at }
}

func newRoleResolver(lister *fakeRoleLister, interval time.Duration) *principalResolver {
	return &principalResolver{
		roles: newLookupCache("roles", lister.list, interval),
	}
}

func TestResolveRoles_CacheHitCostsNoAPICalls(t *testing.T) {
	lister := &fakeRoleLister{roles: []pangolin.Role{{ID: 3, Name: "developers"}}}
	r := newRoleResolver(lister, time.Minute)

	for i := 0; i < 5; i++ {
		ids, err := r.resolveRoles(context.Background(), []string{"developers"})
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != 3 {
			t.Fatalf("got %v want [3]", ids)
		}
	}

	if got := lister.callCount(); got != 1 {
		t.Fatalf("expected a single list call, got %d", got)
	}
}

func TestResolveRoles_RoleCreatedAfterStartupResolvesWithoutRestart(t *testing.T) {
	lister := &fakeRoleLister{roles: []pangolin.Role{{ID: 1, Name: "admins"}}}
	r := newRoleResolver(lister, time.Minute)

	clock := time.Now()
	r.roles.now = fixedNow(&clock)

	if _, err := r.resolveRoles(context.Background(), []string{"admins"}); err != nil {
		t.Fatal(err)
	}

	// The role is created in Pangolin after the cache was populated.
	lister.set([]pangolin.Role{{ID: 1, Name: "admins"}, {ID: 7, Name: "developers"}}, nil)
	clock = clock.Add(2 * time.Minute)

	ids, err := r.resolveRoles(context.Background(), []string{"developers"})
	if err != nil {
		t.Fatalf("expected refresh-on-miss to resolve the new role, got %v", err)
	}
	if len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("got %v want [7]", ids)
	}
}

func TestResolveRoles_MissWithinCooldownDoesNotRefetch(t *testing.T) {
	lister := &fakeRoleLister{roles: []pangolin.Role{{ID: 1, Name: "admins"}}}
	r := newRoleResolver(lister, time.Minute)

	clock := time.Now()
	r.roles.now = fixedNow(&clock)

	if _, err := r.resolveRoles(context.Background(), []string{"admins"}); err != nil {
		t.Fatal(err)
	}
	before := lister.callCount()

	for i := 0; i < 3; i++ {
		if _, err := r.resolveRoles(context.Background(), []string{"nope"}); !errors.Is(err, errPrincipalNotFound) {
			t.Fatalf("got %v want errPrincipalNotFound", err)
		}
	}

	if got := lister.callCount(); got != before {
		t.Fatalf("expected no refetch inside the cooldown, calls went %d -> %d", before, got)
	}
}

func TestResolveRoles_FailedRefetchRetainsMapping(t *testing.T) {
	lister := &fakeRoleLister{roles: []pangolin.Role{{ID: 1, Name: "admins"}}}
	r := newRoleResolver(lister, time.Minute)

	clock := time.Now()
	r.roles.now = fixedNow(&clock)

	if _, err := r.resolveRoles(context.Background(), []string{"admins"}); err != nil {
		t.Fatal(err)
	}

	lister.set(nil, errors.New("pangolin unavailable"))
	clock = clock.Add(2 * time.Minute)

	// The refetch fails, but the previously cached mapping must survive.
	if _, err := r.resolveRoles(context.Background(), []string{"nope"}); err == nil {
		t.Fatal("expected the failed refetch to surface an error")
	}
	if _, err := r.resolveRoles(context.Background(), []string{"admins"}); err != nil {
		t.Fatalf("cached role should still resolve after a failed refetch, got %v", err)
	}
}

func TestResolveRoles_AmbiguousNameIsRefused(t *testing.T) {
	lister := &fakeRoleLister{roles: []pangolin.Role{
		{ID: 3, Name: "developers"},
		{ID: 9, Name: "developers"},
	}}
	r := newRoleResolver(lister, time.Minute)

	_, err := r.resolveRoles(context.Background(), []string{"developers"})
	if !errors.Is(err, errPrincipalAmbiguous) {
		t.Fatalf("got %v want errPrincipalAmbiguous", err)
	}
}

func TestResolveRoles_SameRoleListedTwiceIsNotAmbiguous(t *testing.T) {
	lister := &fakeRoleLister{roles: []pangolin.Role{
		{ID: 3, Name: "developers"},
		{ID: 3, Name: "developers"},
	}}
	r := newRoleResolver(lister, time.Minute)

	ids, err := r.resolveRoles(context.Background(), []string{"developers"})
	if err != nil {
		t.Fatalf("got %v, want the duplicate entry to resolve", err)
	}
	if len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("got %v want [3]", ids)
	}
}

func TestResolveClients_MatchesNameOrNiceID(t *testing.T) {
	clients := []pangolin.PangolinClient{
		{ID: 12, Name: "vinzenz-laptop"},
		{ID: 13, NiceID: "spare-phone"},
	}
	r := &principalResolver{
		clients: newLookupCache("clients", func(context.Context) ([]pangolin.PangolinClient, error) {
			return clients, nil
		}, time.Minute),
	}

	ids, err := r.resolveClients(context.Background(), []string{"vinzenz-laptop", "spare-phone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 12 || ids[1] != 13 {
		t.Fatalf("got %v want [12 13]", ids)
	}
}

func TestUserLookup_CachesHitsAndRateLimitsMisses(t *testing.T) {
	var calls int
	fetch := func(_ context.Context, username string) (*pangolin.User, error) {
		calls++
		if username == "office@corelyr.com" {
			return &pangolin.User{ID: "u-1", Username: username}, nil
		}
		return nil, &pangolin.NotFoundError{Message: "no such user"}
	}

	lookup := newUserLookup(fetch, time.Minute)
	clock := time.Now()
	lookup.now = fixedNow(&clock)

	for i := 0; i < 3; i++ {
		id, err := lookup.resolve(context.Background(), "office@corelyr.com")
		if err != nil {
			t.Fatal(err)
		}
		if id != "u-1" {
			t.Fatalf("got %q want u-1", id)
		}
	}
	if calls != 1 {
		t.Fatalf("expected the hit to be cached, got %d calls", calls)
	}

	before := calls
	for i := 0; i < 3; i++ {
		if _, err := lookup.resolve(context.Background(), "ghost@corelyr.com"); !errors.Is(err, errPrincipalNotFound) {
			t.Fatalf("got %v want errPrincipalNotFound", err)
		}
	}
	if calls != before+1 {
		t.Fatalf("expected misses to be rate limited to one call, got %d", calls-before)
	}

	// After the cooldown the miss is retried, and by then the user exists.
	clock = clock.Add(2 * time.Minute)
	if _, err := lookup.resolve(context.Background(), "ghost@corelyr.com"); !errors.Is(err, errPrincipalNotFound) {
		t.Fatalf("got %v want errPrincipalNotFound", err)
	}
	if calls != before+2 {
		t.Fatalf("expected one retry after the cooldown, got %d calls total", calls)
	}
}

func TestUserLookup_APIFailureIsNotAMiss(t *testing.T) {
	fetch := func(context.Context, string) (*pangolin.User, error) {
		return nil, errors.New("connection refused")
	}
	lookup := newUserLookup(fetch, time.Minute)

	_, err := lookup.resolve(context.Background(), "someone")
	if errors.Is(err, errPrincipalNotFound) {
		t.Fatal("a transport failure must not be reported as an unknown principal")
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}
