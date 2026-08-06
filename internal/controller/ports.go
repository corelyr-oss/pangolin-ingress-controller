package controller

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/vinzenz/pangolin-ingress-controller/api/v1alpha1"
)

// portRange is an inclusive range of ports. A single port is a range where
// from == to.
type portRange struct {
	From int32
	To   int32
}

// portSet is a canonical set of ports for one protocol: either every port, or
// a sorted, merged list of non-overlapping ranges.
//
// Canonicalisation is what makes comparison meaningful. Pangolin may store a
// port list in a different form than it was sent -- reordered, deduplicated, or
// with adjacent ports merged into a range -- and comparing the serialized
// strings would then report a difference on every reconcile and issue an
// endless stream of no-op updates. Both sides are reduced to this form before
// they are compared.
type portSet struct {
	All    bool
	Ranges []portRange
}

// newPortSet canonicalises ranges: sorted by start, with overlapping and
// adjacent ranges merged so that "5432,5433" and "5432-5433" are one value.
func newPortSet(all bool, ranges []portRange) portSet {
	if all {
		return portSet{All: true}
	}
	if len(ranges) == 0 {
		return portSet{}
	}

	sorted := append([]portRange(nil), ranges...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].From != sorted[j].From {
			return sorted[i].From < sorted[j].From
		}
		return sorted[i].To < sorted[j].To
	})

	merged := []portRange{sorted[0]}
	for _, r := range sorted[1:] {
		last := &merged[len(merged)-1]
		// Merge on overlap and on adjacency: 5432 followed by 5433 is the
		// same set of ports as 5432-5433.
		if r.From <= last.To+1 {
			if r.To > last.To {
				last.To = r.To
			}
			continue
		}
		merged = append(merged, r)
	}
	return portSet{Ranges: merged}
}

// Empty reports whether the set exposes no ports at all.
func (p portSet) Empty() bool {
	return !p.All && len(p.Ranges) == 0
}

// String renders the set in Pangolin's port range syntax.
func (p portSet) String() string {
	if p.All {
		return "*"
	}
	if len(p.Ranges) == 0 {
		return ""
	}

	parts := make([]string, 0, len(p.Ranges))
	for _, r := range p.Ranges {
		if r.From == r.To {
			parts = append(parts, strconv.Itoa(int(r.From)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r.From, r.To))
	}
	return strings.Join(parts, ",")
}

// Equal compares two sets by the ports they contain, not by how they were
// written.
func (p portSet) Equal(other portSet) bool {
	if p.All != other.All {
		return false
	}
	if len(p.Ranges) != len(other.Ranges) {
		return false
	}
	for i := range p.Ranges {
		if p.Ranges[i] != other.Ranges[i] {
			return false
		}
	}
	return true
}

// parsePortSet reads Pangolin's port range syntax: "*", or a comma-separated
// list of ports and "from-to" ranges.
func parsePortSet(s string) (portSet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return portSet{}, nil
	}
	if s == "*" {
		return portSet{All: true}, nil
	}

	var ranges []portRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		from, to, found := strings.Cut(part, "-")
		if !found {
			port, err := parsePort(part)
			if err != nil {
				return portSet{}, err
			}
			ranges = append(ranges, portRange{From: port, To: port})
			continue
		}

		lo, err := parsePort(from)
		if err != nil {
			return portSet{}, err
		}
		hi, err := parsePort(to)
		if err != nil {
			return portSet{}, err
		}
		if lo > hi {
			return portSet{}, fmt.Errorf("invalid port range %q: start exceeds end", part)
		}
		ranges = append(ranges, portRange{From: lo, To: hi})
	}
	return newPortSet(false, ranges), nil
}

func parsePort(s string) (int32, error) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid port %q: %w", s, err)
	}
	if v < 1 || v > 65535 {
		return 0, fmt.Errorf("invalid port %d: out of range", v)
	}
	return int32(v), nil
}

// portSetsFromSpec builds the per-protocol sets declared on an endpoint.
func portSetsFromSpec(ports []v1alpha1.EndpointPort) (tcp, udp portSet) {
	var tcpRanges, udpRanges []portRange
	var tcpAll, udpAll bool

	for _, p := range ports {
		all := p.All != nil && *p.All

		var r []portRange
		switch {
		case all:
			// handled via the All flag
		case p.Port != nil:
			r = []portRange{{From: *p.Port, To: *p.Port}}
		case p.From != nil && p.To != nil:
			r = []portRange{{From: *p.From, To: *p.To}}
		}

		if p.Protocol == v1alpha1.ProtocolUDP {
			udpAll = udpAll || all
			udpRanges = append(udpRanges, r...)
			continue
		}
		// Protocol defaults to TCP, so an unset value lands here.
		tcpAll = tcpAll || all
		tcpRanges = append(tcpRanges, r...)
	}

	return newPortSet(tcpAll, tcpRanges), newPortSet(udpAll, udpRanges)
}

// portSetsFromService derives the port sets from the backing Service. This is
// what an unset spec.private.ports means: track the Service.
//
// SCTP ports are skipped -- Pangolin's private resources carry only TCP and
// UDP port ranges, so there is nowhere to put them.
func portSetsFromService(svc *corev1.Service) (tcp, udp portSet, skippedSCTP bool) {
	var tcpRanges, udpRanges []portRange

	for _, p := range svc.Spec.Ports {
		switch p.Protocol {
		case corev1.ProtocolUDP:
			udpRanges = append(udpRanges, portRange{From: p.Port, To: p.Port})
		case corev1.ProtocolSCTP:
			skippedSCTP = true
		default:
			// An empty Protocol defaults to TCP in the Service API.
			tcpRanges = append(tcpRanges, portRange{From: p.Port, To: p.Port})
		}
	}

	return newPortSet(false, tcpRanges), newPortSet(false, udpRanges), skippedSCTP
}

// singleTCPPort returns the port when the set is exactly one TCP port.
//
// SPIKE (task 1.4): it is not yet confirmed whether Pangolin's destinationPort
// interacts with the port range strings in mode "host", or whether it is only
// meaningful for the http/ssh modes. Sending it for the unambiguous
// single-port case and omitting it otherwise is the conservative reading.
func singleTCPPort(p portSet) (int32, bool) {
	if p.All || len(p.Ranges) != 1 {
		return 0, false
	}
	if p.Ranges[0].From != p.Ranges[0].To {
		return 0, false
	}
	return p.Ranges[0].From, true
}
