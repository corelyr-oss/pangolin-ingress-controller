package controller

import (
	"context"
	"testing"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

func TestMatchHostToDomains(t *testing.T) {
	// Domains sorted by BaseDomain length descending (longest first),
	// which is how loadDomains returns them.
	domains := []pangolin.Domain{
		{ID: "id-example-co-uk", BaseDomain: "example.co.uk"},
		{ID: "id-example-com", BaseDomain: "example.com"},
		{ID: "id-tunnel-tf", BaseDomain: "tunnel.tf"},
		{ID: "id-co-uk", BaseDomain: "co.uk"},
	}

	tests := []struct {
		name         string
		host         string
		domains      []pangolin.Domain
		wantSub      string
		wantDomainID string
		wantMatched  bool
	}{
		{
			name:         "simple subdomain match",
			host:         "app.example.com",
			domains:      domains,
			wantSub:      "app",
			wantDomainID: "id-example-com",
			wantMatched:  true,
		},
		{
			name:         "ccTLD subdomain match (longest wins)",
			host:         "app.example.co.uk",
			domains:      domains,
			wantSub:      "app",
			wantDomainID: "id-example-co-uk",
			wantMatched:  true,
		},
		{
			name:         "deep subdomain",
			host:         "deep.sub.example.com",
			domains:      domains,
			wantSub:      "deep.sub",
			wantDomainID: "id-example-com",
			wantMatched:  true,
		},
		{
			name:         "bare domain (no subdomain)",
			host:         "example.com",
			domains:      domains,
			wantSub:      "",
			wantDomainID: "id-example-com",
			wantMatched:  true,
		},
		{
			name:         "bare ccTLD domain",
			host:         "example.co.uk",
			domains:      domains,
			wantSub:      "",
			wantDomainID: "id-example-co-uk",
			wantMatched:  true,
		},
		{
			name:         "no matching domain",
			host:         "app.unknown.org",
			domains:      domains,
			wantSub:      "",
			wantDomainID: "",
			wantMatched:  false,
		},
		{
			name:         "host matching shorter domain when longer also present",
			host:         "app.co.uk",
			domains:      domains,
			wantSub:      "app",
			wantDomainID: "id-co-uk",
			wantMatched:  true,
		},
		{
			name:         "tunnel.tf domain",
			host:         "myapp.tunnel.tf",
			domains:      domains,
			wantSub:      "myapp",
			wantDomainID: "id-tunnel-tf",
			wantMatched:  true,
		},
		{
			name:         "empty domains list",
			host:         "app.example.com",
			domains:      nil,
			wantSub:      "",
			wantDomainID: "",
			wantMatched:  false,
		},
		{
			name:         "empty host",
			host:         "",
			domains:      domains,
			wantSub:      "",
			wantDomainID: "",
			wantMatched:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, domainID, matched := matchHostToDomains(tt.host, tt.domains)
			if sub != tt.wantSub {
				t.Errorf("subdomain = %q, want %q", sub, tt.wantSub)
			}
			if domainID != tt.wantDomainID {
				t.Errorf("domainID = %q, want %q", domainID, tt.wantDomainID)
			}
			if matched != tt.wantMatched {
				t.Errorf("matched = %v, want %v", matched, tt.wantMatched)
			}
		})
	}
}

func TestResolveHostDomain(t *testing.T) {
	// Pre-sorted longest first (as the domain cache stores them)
	cachedDomains := []pangolin.Domain{
		{ID: "id-example-co-uk", BaseDomain: "example.co.uk"},
		{ID: "id-example-com", BaseDomain: "example.com"},
		{ID: "id-tunnel-tf", BaseDomain: "tunnel.tf"},
	}

	tests := []struct {
		name         string
		host         string
		domainCache  []pangolin.Domain
		wantSub      string
		wantDomainID string
		wantErr      bool
	}{
		{
			name:         "direct suffix match",
			host:         "app.example.com",
			domainCache:  cachedDomains,
			wantSub:      "app",
			wantDomainID: "id-example-com",
		},
		{
			name:         "ccTLD suffix match",
			host:         "app.example.co.uk",
			domainCache:  cachedDomains,
			wantSub:      "app",
			wantDomainID: "id-example-co-uk",
		},
		{
			name:         "bare domain",
			host:         "tunnel.tf",
			domainCache:  cachedDomains,
			wantSub:      "",
			wantDomainID: "id-tunnel-tf",
		},
		{
			name:        "empty host",
			host:        "",
			domainCache: cachedDomains,
			wantErr:     true,
		},
		{
			name:        "whitespace host",
			host:        "   ",
			domainCache: cachedDomains,
			wantErr:     true,
		},
		{
			name:        "no matching domain and PSL fallback fails",
			host:        "uk",
			domainCache: cachedDomains,
			wantErr:     true,
		},
		{
			name:        "PSL parses domain but no Pangolin domain matches",
			host:        "app.unknown.org",
			domainCache: cachedDomains,
			wantErr:     true,
		},
		{
			name: "PSL fallback succeeds when suffix match misses",
			host: "app.example.com",
			// Domain listed without the host being a direct suffix match
			// scenario: exact BaseDomain match via PSL path
			domainCache: []pangolin.Domain{
				{ID: "id-example-com", BaseDomain: "example.com"},
			},
			wantSub:      "app",
			wantDomainID: "id-example-com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Refresh disabled so these cases exercise pure matching against a
			// warm cache, with no API involvement.
			r := &IngressReconciler{
				domains: warmDomainCache(tt.domainCache, 0),
			}

			sub, domainID, err := r.resolveHostDomain(context.Background(), tt.host)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if sub != tt.wantSub {
				t.Errorf("subdomain = %q, want %q", sub, tt.wantSub)
			}
			if domainID != tt.wantDomainID {
				t.Errorf("domainID = %q, want %q", domainID, tt.wantDomainID)
			}
		})
	}
}

func TestMatchHostToDomains_LongestMatchWins(t *testing.T) {
	// Both example.co.uk and co.uk are registered. app.example.co.uk should
	// match the longer example.co.uk, not the shorter co.uk.
	domains := []pangolin.Domain{
		{ID: "id-example-co-uk", BaseDomain: "example.co.uk"},
		{ID: "id-co-uk", BaseDomain: "co.uk"},
	}

	sub, domainID, matched := matchHostToDomains("app.example.co.uk", domains)
	if !matched {
		t.Fatal("expected match")
	}
	if domainID != "id-example-co-uk" {
		t.Errorf("domainID = %q, want %q", domainID, "id-example-co-uk")
	}
	if sub != "app" {
		t.Errorf("subdomain = %q, want %q", sub, "app")
	}
}

func TestMatchHostToDomains_AvoidsPartialLabelMatch(t *testing.T) {
	// "notexample.com" should NOT match domain "example.com" because
	// the suffix check requires a dot boundary.
	domains := []pangolin.Domain{
		{ID: "id-example-com", BaseDomain: "example.com"},
	}

	_, _, matched := matchHostToDomains("notexample.com", domains)
	if matched {
		t.Error("expected no match for partial label overlap, but got a match")
	}
}
