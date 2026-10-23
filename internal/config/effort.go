package config

import "fmt"

// DefaultEffortKey names the managed/local fallback effort setting.
const DefaultEffortKey = "default_effort"

// DefaultEffort is the effort for sessions and profiles without an explicit effort or configured fallback.
const DefaultEffort = "medium"

// EffectiveEffort returns explicit when nonempty, otherwise the effective default_effort setting,
// or DefaultEffort when absent. It does not mutate the config; callers supply a validated config.
// Explicit values are validated by the selected runtime, not by this resolver.
func (c *Config) EffectiveEffort(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v, ok := c.Setting(DefaultEffortKey); ok {
		return v
	}
	return DefaultEffort
}

func checkDefaultEffort(v string) error {
	switch v {
	case "low", "medium", "high", "xhigh", "max":
		return nil
	default:
		return fmt.Errorf("%s %q must be low, medium, high, xhigh or max", DefaultEffortKey, v)
	}
}
