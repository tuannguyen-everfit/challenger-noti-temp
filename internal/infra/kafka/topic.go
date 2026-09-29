package kafka

// PrefixTopic builds the on-broker topic name for the everfit MSK bus, which
// namespaces every topic by environment as `<prefix>-<topic>` (the producers'
// AWS_MSK_PREFIX). An empty prefix returns the bare topic unchanged — for
// single-tenant or local brokers that don't namespace.
func PrefixTopic(prefix, topic string) string {
	if prefix == "" {
		return topic
	}
	return prefix + "-" + topic
}
