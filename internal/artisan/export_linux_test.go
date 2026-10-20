package artisan

// DetectedContainment is the kind newUnit picks on this host and why.
func DetectedContainment() (kind, reason string) {
	d := detectCgroup(systemCgroupEnv)
	if d.ok {
		return KindCgroup, d.reason
	}
	return KindSubreaper, d.reason
}

// NewCgroupUnit is the cgroup backend; ok is false, with the detection's
// reason, when this host has no delegated cgroup v2.
func NewCgroupUnit(name string) (u Unit, reason string, ok bool, err error) {
	d := detectCgroup(systemCgroupEnv)
	if !d.ok {
		return nil, d.reason, false, nil
	}
	u, err = newCgroupUnit(name, d)
	return u, d.reason, true, err
}

// NewSubreaperUnit is the fallback backend, whatever this host supports.
func NewSubreaperUnit(_ string) (Unit, error) { return newSubreaperUnit("forced by test") }
