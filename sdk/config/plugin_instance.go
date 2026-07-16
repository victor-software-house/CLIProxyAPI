package config

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxPluginConfigDepth = 64

// NewPluginInstanceConfig constructs one plugin instance configuration from
// host-owned settings and schema-validated JSON-shaped plugin values.
func NewPluginInstanceConfig(enabled bool, priority int, values map[string]any) (PluginInstanceConfig, error) {
	for key := range values {
		if key == "enabled" || key == "priority" {
			return PluginInstanceConfig{}, fmt.Errorf("plugin config %s is host-owned", key)
		}
	}

	raw, errConvert := pluginConfigMapping(values, "$", 0)
	if errConvert != nil {
		return PluginInstanceConfig{}, errConvert
	}
	raw.Content = append(raw.Content,
		pluginConfigScalar("enabled", "!!str"), pluginConfigScalar(strconv.FormatBool(enabled), "!!bool"),
		pluginConfigScalar("priority", "!!str"), pluginConfigScalar(strconv.Itoa(priority), "!!int"),
	)
	return PluginInstanceConfig{Enabled: &enabled, Priority: priority, Raw: *raw}, nil
}

func pluginConfigMapping(values map[string]any, path string, depth int) (*yaml.Node, error) {
	if depth >= maxPluginConfigDepth {
		return nil, fmt.Errorf("plugin config %s exceeds maximum depth", path)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, key := range keys {
		value, errValue := pluginConfigNode(values[key], path+"[key]", depth+1)
		if errValue != nil {
			return nil, errValue
		}
		node.Content = append(node.Content, pluginConfigScalar(key, "!!str"), value)
	}
	return node, nil
}

func pluginConfigNode(value any, path string, depth int) (*yaml.Node, error) {
	if depth >= maxPluginConfigDepth {
		return nil, fmt.Errorf("plugin config %s exceeds maximum depth", path)
	}
	switch value := value.(type) {
	case nil:
		return pluginConfigScalar("null", "!!null"), nil
	case bool:
		return pluginConfigScalar(strconv.FormatBool(value), "!!bool"), nil
	case string:
		return pluginConfigScalar(value, "!!str"), nil
	case json.Number:
		text := value.String()
		float, errParse := strconv.ParseFloat(text, 64)
		if !json.Valid([]byte(text)) || errParse != nil || math.IsInf(float, 0) {
			return nil, fmt.Errorf("plugin config %s has invalid number", path)
		}
		tag := "!!int"
		if strings.ContainsAny(text, ".eE") {
			tag = "!!float"
		}
		return pluginConfigScalar(text, tag), nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("plugin config %s has invalid number", path)
		}
		return pluginConfigScalar(strconv.FormatFloat(value, 'g', -1, 64), "!!float"), nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for index, item := range value {
			child, errChild := pluginConfigNode(item, fmt.Sprintf("%s[%d]", path, index), depth+1)
			if errChild != nil {
				return nil, errChild
			}
			node.Content = append(node.Content, child)
		}
		return node, nil
	case map[string]any:
		return pluginConfigMapping(value, path, depth)
	default:
		return nil, fmt.Errorf("plugin config %s has unsupported type %T", path, value)
	}
}

func pluginConfigScalar(value, tag string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}
