package connect

import "testing"

func TestParseSSOutput(t *testing.T) {
	output := `LISTEN 0 128 127.0.0.1:8080 0.0.0.0:*
LISTEN 0 128 127.0.0.1:9000 0.0.0.0:*
LISTEN 0 128 0.0.0.0:22 0.0.0.0:*
LISTEN 0 128 [::1]:8080 [::]:*`

	maps := parseSSOutput(output)
	if len(maps) != 2 {
		t.Fatalf("expected 2 ports, got %d: %+v", len(maps), maps)
	}
	if maps[0].NSPort != 8080 || maps[1].NSPort != 9000 {
		t.Fatalf("unexpected ports: %+v", maps)
	}
}

func TestParsePortSpec(t *testing.T) {
	m, err := ParsePortSpec("8080:19080")
	if err != nil {
		t.Fatal(err)
	}
	if m.NSPort != 8080 || m.HostPort != 19080 {
		t.Fatalf("unexpected map: %+v", m)
	}
}
