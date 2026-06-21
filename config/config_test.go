package config

import "testing"

func TestLookupByAlias(t *testing.T) {
	k, ok := Lookup("ef_search")
	if !ok {
		t.Fatal("ef_search alias not found")
	}
	if k.Name != "hnsw_ef_search" {
		t.Fatalf("ef_search resolved to %q, want hnsw_ef_search", k.Name)
	}
	for _, alias := range []string{"nprobe", "timeout", "k"} {
		if _, ok := Lookup(alias); !ok {
			t.Errorf("alias %q not found", alias)
		}
	}
	if _, ok := Lookup("EF_SEARCH"); !ok {
		t.Error("lookup is not case-insensitive")
	}
	if _, ok := Lookup("no_such_knob"); ok {
		t.Error("unknown knob reported as found")
	}
}

func TestEveryKnobHasSaneDefault(t *testing.T) {
	for _, k := range All() {
		k := k
		if k.Computed {
			continue
		}
		if _, err := k.Canonicalize(k.Default); err != nil {
			t.Errorf("%s: default %q does not validate: %v", k.Name, k.Default, err)
		}
	}
}

func TestCanonicalizeInt(t *testing.T) {
	k, _ := Lookup("page_size")
	if got, err := k.Canonicalize("8192"); err != nil || got != "8192" {
		t.Fatalf("page_size 8192: got %q err %v", got, err)
	}
	if _, err := k.Canonicalize("8000"); err == nil {
		t.Error("page_size 8000 (not power of two) should fail")
	}
	if _, err := k.Canonicalize("256"); err == nil {
		t.Error("page_size 256 (below min) should fail")
	}
	if _, err := k.Canonicalize("131072"); err == nil {
		t.Error("page_size 131072 (above max) should fail")
	}
}

func TestCanonicalizeSizeSuffix(t *testing.T) {
	k, _ := Lookup("max_query_memory")
	got, err := k.Canonicalize("512MiB")
	if err != nil {
		t.Fatalf("512MiB: %v", err)
	}
	if got != "536870912" {
		t.Fatalf("512MiB canonicalized to %q, want 536870912", got)
	}
}

func TestCanonicalizeBool(t *testing.T) {
	k, _ := Lookup("auto_analyze")
	for _, in := range []string{"on", "TRUE", "1", "yes"} {
		if got, err := k.Canonicalize(in); err != nil || got != "on" {
			t.Errorf("%q -> %q err %v, want on", in, got, err)
		}
	}
	for _, in := range []string{"off", "false", "0", "no"} {
		if got, err := k.Canonicalize(in); err != nil || got != "off" {
			t.Errorf("%q -> %q err %v, want off", in, got, err)
		}
	}
	if _, err := k.Canonicalize("maybe"); err == nil {
		t.Error("maybe should not be a boolean")
	}
}

func TestCanonicalizeEnum(t *testing.T) {
	k, _ := Lookup("synchronous")
	if got, err := k.Canonicalize("full"); err != nil || got != "FULL" {
		t.Fatalf("full -> %q err %v, want FULL", got, err)
	}
	if _, err := k.Canonicalize("sometimes"); err == nil {
		t.Error("sometimes should not be a synchronous level")
	}
}

func TestCanonicalizeFloatRange(t *testing.T) {
	k, _ := Lookup("recall_target")
	if _, err := k.Canonicalize("0.9"); err != nil {
		t.Fatalf("0.9: %v", err)
	}
	if _, err := k.Canonicalize("1.5"); err == nil {
		t.Error("recall_target 1.5 (above max) should fail")
	}
	if _, err := k.Canonicalize("NaN"); err == nil {
		t.Error("recall_target NaN should fail")
	}
}

func TestCanonicalizeFloatList(t *testing.T) {
	k, _ := Lookup("fusion_weights")
	got, err := k.Canonicalize("[0.7, 0.3]")
	if err != nil {
		t.Fatalf("[0.7, 0.3]: %v", err)
	}
	if got != "[0.7,0.3]" {
		t.Fatalf("got %q, want [0.7,0.3]", got)
	}
	if _, err := k.Canonicalize("[-1, 2]"); err == nil {
		t.Error("negative weight should fail")
	}
}

func TestReadOnlyKnobFlagged(t *testing.T) {
	k, ok := Lookup("isolation")
	if !ok || !k.ReadOnly {
		t.Fatal("isolation should be a read-only knob")
	}
}
