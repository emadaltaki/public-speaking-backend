package mail

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBuildMessageEncodesUTF8(t *testing.T) {
	msg := string(buildMessage("Speak Up <a@example.com>", Message{To: "b@example.com", Subject: "رمزك: 123456", Text: "مرحبًا 123456"}))
	head, body, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok {
		t.Fatal("no header/body separator")
	}
	if !strings.Contains(head, "Subject: =?utf-8?q?") || !strings.Contains(head, "charset=UTF-8") {
		t.Fatalf("headers: %s", head)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(body), "\r\n", ""))
	if err != nil || string(decoded) != "مرحبًا 123456" {
		t.Fatalf("body: %v %q", err, decoded)
	}
}
