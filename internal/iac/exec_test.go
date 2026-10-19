package iac

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// terraform apply asks "Enter a value:" on stdin. A Tool that left stdin
// closed made that prompt fail on EOF right after pvecli's own confirmation.
func TestRunFeedsStdinToTheToolsOwnPrompt(t *testing.T) {
	var out bytes.Buffer
	tool := Tool{Name: "sh", Stdin: strings.NewReader("yes\n")}
	if err := tool.Run(context.Background(), &out, &out, "-c", "read answer; printf '%s' \"$answer\""); err != nil {
		t.Fatal(err)
	}
	if out.String() != "yes" {
		t.Fatalf("the tool did not read the operator's answer: %q", out.String())
	}
}
