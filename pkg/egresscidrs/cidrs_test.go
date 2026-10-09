package egresscidrs

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseEmpty(t *testing.T) {
	got, err := Parse("  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("prefixes = %v, want none", got)
	}
}

func TestParseAcceptsInternalPrefix(t *testing.T) {
	got, err := Parse(" 10.0.0.0/8, 192.168.10.0/24 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "10.0.0.0/8" || got[1].String() != "192.168.10.0/24" {
		t.Fatalf("prefixes = %v", got)
	}
}

func TestParseRejectsDefaultAndWideRoutes(t *testing.T) {
	for _, raw := range []string{"0.0.0.0/0", "::/0", "0.0.0.0/1", "10.0.0.0/7"} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("Parse(%q) accepted a route wider than /8", raw)
		}
	}
}

func TestParseRejectsPrivateAllowWithoutExcept(t *testing.T) {
	allows, err := Parse("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Blocks(allows, nil); err == nil {
		t.Fatal("private destination without a cluster exception was accepted")
	}
}

func TestBlocksExceptsClusterRanges(t *testing.T) {
	allows, err := Parse("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	except, err := Parse("10.42.0.0/16,10.43.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := Blocks(allows, except)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v", blocks)
	}
	block := blocks[0]
	if block.CIDR != "10.0.0.0/8" {
		t.Fatalf("cidr = %s", block.CIDR)
	}
	if strings.Join(block.Except, ",") != "10.42.0.0/16,10.43.0.0/16" {
		t.Fatalf("except = %v", block.Except)
	}
	pod := netip.MustParseAddr("10.42.0.76")
	prefix := netip.MustParsePrefix(block.CIDR)
	if !prefix.Contains(pod) {
		t.Fatal("parent prefix should still contain the pod range before exceptions")
	}
}
