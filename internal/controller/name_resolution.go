package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

var (
	// errPrincipalNotFound marks a name that matches no Pangolin object even
	// after the relevant mapping has been refreshed. Like errDomainNotFound it
	// is an expected, operator-fixable condition rather than a controller
	// fault, so callers must wrap it with %w to preserve that handling.
	errPrincipalNotFound = errors.New("not found in Pangolin")

	// errPrincipalAmbiguous marks a name matching more than one Pangolin
	// object. Picking one would silently grant access to the wrong principal,
	// so resolution refuses instead.
	errPrincipalAmbiguous = errors.New("matches more than one Pangolin object")
)

// principalResolver turns Pangolin names into the identifiers the API needs.
//
// Roles and clients are resolved from cached org-wide lists; users are
// resolved through Pangolin's by-username lookup, so their cache sits in front
// of point queries rather than a list.
type principalResolver struct {
	roles   *lookupCache[pangolin.Role]
	clients *lookupCache[pangolin.PangolinClient]
	users   *userLookup
}

func newPrincipalResolver(client *pangolin.Client, interval time.Duration) *principalResolver {
	return &principalResolver{
		roles:   newLookupCache("roles", client.ListRoles, interval),
		clients: newLookupCache("clients", client.ListClients, interval),
		users:   newUserLookup(client.GetUserByUsername, interval),
	}
}

// resolveRoles maps role names to role IDs.
func (r *principalResolver) resolveRoles(ctx context.Context, names []string) ([]int, error) {
	return resolveAll(ctx, names, func(name string) (int, error) {
		return lookupByName(ctx, r.roles, "role", name,
			func(role pangolin.Role) string { return role.Name },
			func(role pangolin.Role) int { return role.ID })
	})
}

// resolveClients maps client names to client IDs. A client is matched on its
// name or its nice ID, since both are shown as identifiers in Pangolin.
func (r *principalResolver) resolveClients(ctx context.Context, names []string) ([]int, error) {
	return resolveAll(ctx, names, func(name string) (int, error) {
		return lookupByName(ctx, r.clients, "client", name,
			func(c pangolin.PangolinClient) string {
				if c.Name != "" {
					return c.Name
				}
				return c.NiceID
			},
			func(c pangolin.PangolinClient) int { return c.ID })
	})
}

// resolveUsers maps usernames to Pangolin user IDs.
func (r *principalResolver) resolveUsers(ctx context.Context, names []string) ([]string, error) {
	return resolveAll(ctx, names, func(name string) (string, error) {
		return r.users.resolve(ctx, name)
	})
}

func resolveAll[ID any](ctx context.Context, names []string, resolve func(string) (ID, error)) ([]ID, error) {
	out := make([]ID, 0, len(names))
	for _, name := range names {
		id, err := resolve(name)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// lookupByName resolves one name against a cached list, refreshing once on a
// miss before giving up. This is the same recovery path the domain cache uses:
// a stale list can only cause a spurious miss, so one refetch and a retry is
// enough to pick up an object created after this process started.
func lookupByName[T any, ID comparable](
	ctx context.Context,
	cache *lookupCache[T],
	kind string,
	name string,
	nameOf func(T) string,
	idOf func(T) ID,
) (ID, error) {
	var zero ID

	entries, err := cache.get(ctx)
	if err != nil {
		return zero, err
	}

	id, matches := matchByName(entries, name, nameOf, idOf)
	if matches > 1 {
		return zero, fmt.Errorf("%s %q: %w", kind, name, errPrincipalAmbiguous)
	}
	if matches == 1 {
		return id, nil
	}

	refreshed, didRefresh, err := cache.refreshIfStale(ctx)
	if err != nil {
		return zero, err
	}
	if didRefresh {
		id, matches = matchByName(refreshed, name, nameOf, idOf)
		if matches > 1 {
			return zero, fmt.Errorf("%s %q: %w", kind, name, errPrincipalAmbiguous)
		}
		if matches == 1 {
			return id, nil
		}
	}

	return zero, fmt.Errorf("%s %q: %w", kind, name, errPrincipalNotFound)
}

// matchByName reports the identifier for name and how many entries matched, so
// callers can distinguish "absent" from "ambiguous".
func matchByName[T any, ID comparable](entries []T, name string, nameOf func(T) string, idOf func(T) ID) (ID, int) {
	var found ID
	matches := 0

	for _, e := range entries {
		if nameOf(e) != name {
			continue
		}
		id := idOf(e)
		if matches > 0 && id == found {
			// The same object listed twice is not an ambiguity.
			continue
		}
		found = id
		matches++
	}
	return found, matches
}

// userLookup caches Pangolin's by-username lookup.
//
// Successful resolutions are kept for the life of the process: a username's
// identifier does not change. Misses are rate-limited by the same interval the
// list caches use, so an object referencing a user who does not exist cannot
// turn repeated reconciles into sustained API traffic.
type userLookup struct {
	fetch    func(ctx context.Context, username string) (*pangolin.User, error)
	interval time.Duration
	now      func() time.Time

	mu       sync.Mutex
	ids      map[string]string
	lastMiss map[string]time.Time
}

func newUserLookup(fetch func(ctx context.Context, username string) (*pangolin.User, error), interval time.Duration) *userLookup {
	return &userLookup{
		fetch:    fetch,
		interval: interval,
		now:      time.Now,
		ids:      map[string]string{},
		lastMiss: map[string]time.Time{},
	}
}

func (u *userLookup) resolve(ctx context.Context, username string) (string, error) {
	u.mu.Lock()
	if id, ok := u.ids[username]; ok {
		u.mu.Unlock()
		return id, nil
	}
	last, missed := u.lastMiss[username]
	u.mu.Unlock()

	// Within the cooldown a known miss is reported without another API call.
	if missed && u.interval > 0 && u.now().Sub(last) < u.interval {
		return "", fmt.Errorf("user %q: %w", username, errPrincipalNotFound)
	}

	u.mu.Lock()
	u.lastMiss[username] = u.now()
	u.mu.Unlock()

	user, err := u.fetch(ctx, username)
	if err != nil {
		if pangolin.IsNotFound(err) {
			return "", fmt.Errorf("user %q: %w", username, errPrincipalNotFound)
		}
		return "", fmt.Errorf("failed to look up Pangolin user %q: %w", username, err)
	}
	if user == nil || user.ID == "" {
		return "", fmt.Errorf("user %q: %w", username, errPrincipalNotFound)
	}

	u.mu.Lock()
	u.ids[username] = user.ID
	delete(u.lastMiss, username)
	u.mu.Unlock()

	return user.ID, nil
}
