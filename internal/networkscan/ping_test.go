package networkscan

import "testing"

func TestParsePingSources(t *testing.T) {
	config := PingConfig{Range: "10.0.[1-2].[1-3]"}
	config.Normalize()
	ips, err := parsePingSources(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 6 || ips[0] != "10.0.1.1" || ips[5] != "10.0.2.3" {
		t.Fatalf("unexpected targets: %v", ips)
	}
}

func TestParsePingSourcesHonorsTargetLimit(t *testing.T) {
	config := PingConfig{Range: "10.[0-10].[0-10].[0-10]", MaxTargets: 100}
	config.Normalize()
	if _, err := parsePingSources(config); err == nil {
		t.Fatal("expected target limit error")
	}
}
