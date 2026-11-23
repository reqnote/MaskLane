package redact

// UsedReplacement looks up the text already substituted for a known keyword.
// Looking it up does not allocate a placeholder or advance the sequence.
func (r *Redactor) UsedReplacement(class Class, original string) (string, bool) {
	if r == nil {
		return "", false
	}
	value, found := r.dedup[class][original]
	return value, found
}
