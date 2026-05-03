package pattern

import "testing"

func TestFillEmptyDescriptionsInPlace(t *testing.T) {
	lib := []KnownPattern{
		{Name: "Traefik Access", Pattern: "%{GREEDYDATA:msg}", Description: ""},
		{Name: "Already set", Pattern: "%{IP:a}", Description: "Custom blurb"},
	}
	FillEmptyDescriptionsInPlace(lib)
	if lib[0].Description != "Grok pattern for Traefik Access logs." {
		t.Errorf("unexpected fill: %q", lib[0].Description)
	}
	if lib[1].Description != "Custom blurb" {
		t.Errorf("should not overwrite existing description: %q", lib[1].Description)
	}
}
