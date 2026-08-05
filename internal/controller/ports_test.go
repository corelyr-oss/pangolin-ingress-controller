package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/vinzenz/pangolin-ingress-controller/api/v1alpha1"
)

func ptrInt32(v int32) *int32 { return &v }
func ptrBool(v bool) *bool    { return &v }

func TestPortSetString(t *testing.T) {
	tests := []struct {
		name string
		set  portSet
		want string
	}{
		{"empty", newPortSet(false, nil), ""},
		{"all", newPortSet(true, nil), "*"},
		{"single", newPortSet(false, []portRange{{5432, 5432}}), "5432"},
		{"range", newPortSet(false, []portRange{{8000, 9000}}), "8000-9000"},
		{"mixed sorted", newPortSet(false, []portRange{{8000, 9000}, {5432, 5432}}), "5432,8000-9000"},
		{"adjacent merged", newPortSet(false, []portRange{{5432, 5432}, {5433, 5433}}), "5432-5433"},
		{"overlapping merged", newPortSet(false, []portRange{{100, 200}, {150, 300}}), "100-300"},
		{"duplicates collapse", newPortSet(false, []portRange{{80, 80}, {80, 80}}), "80"},
		{"all wins over ranges", newPortSet(true, []portRange{{80, 80}}), "*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.set.String(); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestParsePortSet(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty", "", "", false},
		{"all", "*", "*", false},
		{"single", "5432", "5432", false},
		{"list", "80,443", "80,443", false},
		{"range", "8000-9000", "8000-9000", false},
		{"whitespace tolerated", " 80 , 443 ", "80,443", false},
		{"unsorted canonicalised", "443,80", "80,443", false},
		{"adjacent canonicalised", "5432,5433", "5432-5433", false},
		{"not a number", "http", "", true},
		{"out of range", "70000", "", true},
		{"zero", "0", "", true},
		{"inverted range", "9000-8000", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePortSet(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.String() != tt.want {
				t.Fatalf("got %q want %q", got.String(), tt.want)
			}
		})
	}
}

// The point of canonicalisation: Pangolin may store a port list in a different
// form than it was sent. If these compared unequal, the controller would issue
// a no-op update on every single reconcile.
func TestPortSetEqualIgnoresServerNormalisation(t *testing.T) {
	tests := []struct {
		name            string
		desired, server string
		want            bool
	}{
		{"identical", "5432", "5432", true},
		{"reordered", "5432,8080", "8080,5432", true},
		{"adjacent merged by server", "5432,5433", "5432-5433", true},
		{"range split by server", "8000-8002", "8000,8001,8002", true},
		{"duplicate echoed", "80", "80,80", true},
		{"genuinely different", "5432", "5432,9187", false},
		{"all vs list", "*", "1-65535", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired, err := parsePortSet(tt.desired)
			if err != nil {
				t.Fatal(err)
			}
			server, err := parsePortSet(tt.server)
			if err != nil {
				t.Fatal(err)
			}
			if got := desired.Equal(server); got != tt.want {
				t.Fatalf("Equal(%q, %q) = %v want %v", tt.desired, tt.server, got, tt.want)
			}
		})
	}
}

func TestPortSetsFromSpec(t *testing.T) {
	ports := []v1alpha1.EndpointPort{
		{Protocol: v1alpha1.ProtocolTCP, Port: ptrInt32(5432)},
		{Protocol: v1alpha1.ProtocolTCP, From: ptrInt32(8000), To: ptrInt32(9000)},
		{Protocol: v1alpha1.ProtocolUDP, All: ptrBool(true)},
		{Port: ptrInt32(9187)}, // protocol unset defaults to TCP
	}

	tcp, udp := portSetsFromSpec(ports)
	if got, want := tcp.String(), "5432,8000-9000,9187"; got != want {
		t.Fatalf("tcp = %q want %q", got, want)
	}
	if got, want := udp.String(), "*"; got != want {
		t.Fatalf("udp = %q want %q", got, want)
	}
}

func TestPortSetsFromService(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "data"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Port: 5432, Protocol: corev1.ProtocolTCP},
				{Port: 9187},
				{Port: 53, Protocol: corev1.ProtocolUDP},
				{Port: 9999, Protocol: corev1.ProtocolSCTP},
			},
		},
	}

	tcp, udp, skippedSCTP := portSetsFromService(svc)
	if got, want := tcp.String(), "5432,9187"; got != want {
		t.Fatalf("tcp = %q want %q", got, want)
	}
	if got, want := udp.String(), "53"; got != want {
		t.Fatalf("udp = %q want %q", got, want)
	}
	if !skippedSCTP {
		t.Fatal("expected SCTP ports to be reported as skipped")
	}
}

func TestSingleTCPPort(t *testing.T) {
	tests := []struct {
		name   string
		set    portSet
		want   int32
		wantOK bool
	}{
		{"single port", newPortSet(false, []portRange{{5432, 5432}}), 5432, true},
		{"range", newPortSet(false, []portRange{{8000, 9000}}), 0, false},
		{"two ports", newPortSet(false, []portRange{{80, 80}, {443, 443}}), 0, false},
		{"all", newPortSet(true, nil), 0, false},
		{"empty", newPortSet(false, nil), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := singleTCPPort(tt.set)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("got (%d,%v) want (%d,%v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
