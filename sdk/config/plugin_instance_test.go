package config

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNewPluginInstanceConfigCanonicalValues(t *testing.T) {
	nested := map[string]any{"name": "before"}
	items := []any{nil, false, "text", float64(1.5)}
	instance, errNew := NewPluginInstanceConfig(true, -4, map[string]any{
		"zeta":   json.Number("12.5"),
		"alpha":  nested,
		"items":  items,
		"number": float64(2),
	})
	if errNew != nil {
		t.Fatalf("NewPluginInstanceConfig() error = %v", errNew)
	}
	if instance.Enabled == nil || !*instance.Enabled || instance.Priority != -4 {
		t.Fatalf("host fields = (%v, %d), want (true, -4)", instance.Enabled, instance.Priority)
	}
	assertNodeKeys(t, &instance.Raw, "alpha", "items", "number", "zeta", "enabled", "priority")
	assertNodeKeys(t, nodeValue(t, &instance.Raw, "alpha"), "name")
	itemNodes := nodeValue(t, &instance.Raw, "items").Content
	if itemNodes[0].Tag != "!!null" || itemNodes[1].Tag != "!!bool" || itemNodes[2].Tag != "!!str" || itemNodes[3].Tag != "!!float" {
		t.Fatal("items did not retain canonical JSON scalar types")
	}
	if got := nodeValue(t, &instance.Raw, "zeta"); got.Tag != "!!float" || got.Value != "12.5" {
		t.Fatal("json.Number was not retained")
	}

	nested["name"] = "after"
	items[2] = "after"
	if got := nodeValue(t, nodeValue(t, &instance.Raw, "alpha"), "name").Value; got != "before" {
		t.Fatalf("nested value = %q, want before", got)
	}
	if got := nodeValue(t, &instance.Raw, "items").Content[2].Value; got != "text" {
		t.Fatalf("slice value = %q, want text", got)
	}
}

func TestNewPluginInstanceConfigRefusesInvalidValues(t *testing.T) {
	withinDepth := any("leaf")
	for range maxPluginConfigDepth / 2 {
		withinDepth = map[string]any{"nested": withinDepth}
	}
	if _, errNew := NewPluginInstanceConfig(false, 0, map[string]any{"nested": withinDepth}); errNew != nil {
		t.Fatalf("NewPluginInstanceConfig() error = %v, want nil", errNew)
	}

	deep := any("leaf")
	for range maxPluginConfigDepth {
		deep = []any{deep}
	}
	tests := []struct {
		name   string
		values map[string]any
	}{
		{name: "reserved", values: map[string]any{"enabled": false}},
		{name: "unsupported", values: map[string]any{"secret": 1}},
		{name: "invalid number", values: map[string]any{"number": json.Number("01")}},
		{name: "nan", values: map[string]any{"number": math.NaN()}},
		{name: "infinity", values: map[string]any{"number": math.Inf(1)}},
		{name: "depth", values: map[string]any{"nested": deep}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, errNew := NewPluginInstanceConfig(false, 0, test.values)
			if errNew == nil {
				t.Fatal("NewPluginInstanceConfig() error = nil, want error")
			}
			if strings.Contains(errNew.Error(), "secret") {
				t.Fatalf("error leaks a value: %q", errNew)
			}
		})
	}
}

func assertNodeKeys(t *testing.T, node *yaml.Node, want ...string) {
	t.Helper()
	if node.Kind != yaml.MappingNode || len(node.Content) != len(want)*2 {
		t.Fatalf("mapping shape = (%d, %d), want mapping with %d entries", node.Kind, len(node.Content), len(want))
	}
	for index, key := range want {
		if got := node.Content[index*2].Value; got != key {
			t.Fatalf("key %d = %q, want %q", index, got, key)
		}
	}
}

func nodeValue(t *testing.T, node *yaml.Node, want string) *yaml.Node {
	t.Helper()
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == want {
			return node.Content[index+1]
		}
	}
	t.Fatalf("mapping does not contain %q", want)
	return nil
}
