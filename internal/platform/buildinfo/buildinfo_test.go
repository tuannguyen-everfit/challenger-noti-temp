package buildinfo

import (
	"strings"
	"testing"
)

func TestVersion_ReturnsTrimmedFileContents(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("Version() returned empty string — VERSION file missing or empty")
	}
	if strings.ContainsAny(v, " \t\n\r") {
		t.Errorf("Version() = %q contains whitespace, should be trimmed", v)
	}
	// rawVersion ends with a newline in the file; ensure trimming worked.
	if rawVersion == v && strings.HasSuffix(rawVersion, "\n") {
		t.Errorf("Version() did not strip trailing newline from %q", rawVersion)
	}
}
