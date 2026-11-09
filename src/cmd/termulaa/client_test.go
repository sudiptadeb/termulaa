package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestHelpTextsExistAndNameTheExitCodes(t *testing.T) {
	if exitTransport != 255 || exitInterrupt != 130 || exitUsage != 2 {
		t.Fatalf("exit codes changed (%d %d %d); update the help and docs", exitTransport, exitInterrupt, exitUsage)
	}
	for _, topic := range helpTopics {
		text := helpFor(topic)
		if text == "" {
			t.Fatalf("no help for %q", topic)
		}
		if strings.Contains(text, "%!") || strings.Contains(text, "%d") {
			t.Fatalf("help for %q has an unfilled placeholder:\n%s", topic, text)
		}
		if !strings.Contains(text, strconv.Itoa(exitTransport)) {
			t.Fatalf("help for %q does not name the transport exit code %d", topic, exitTransport)
		}
	}
	for _, topic := range []string{"exec", "agents"} {
		if !strings.Contains(helpFor(topic), strconv.Itoa(exitInterrupt)) {
			t.Fatalf("help for %q does not name the interrupt exit code %d", topic, exitInterrupt)
		}
	}
	for _, cmd := range []string{"pair", "machines", "shell", "exec", "help"} {
		if !isClientCommand(cmd) || !strings.Contains(usage, "termulaa "+cmd) {
			t.Fatalf("%q is not a client command listed in the top-level usage", cmd)
		}
	}
}

func TestHelpCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runClient([]string{"help"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "termulaa") {
		t.Fatalf("help: code=%d out=%q", code, out.String())
	}
	out.Reset()
	if code := runClient([]string{"help", "exec"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "usage: termulaa exec") {
		t.Fatalf("help exec: code=%d out=%q", code, out.String())
	}
	out.Reset()
	if code := runClient([]string{"exec", "--help"}, &out, &errOut); code != 0 || !strings.Contains(errOut.String(), "usage: termulaa exec") {
		t.Fatalf("exec --help: code=%d err=%q", code, errOut.String())
	}
	errOut.Reset()
	if code := runClient([]string{"help", "nosuch"}, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "agents") {
		t.Fatalf("help nosuch: code=%d err=%q", code, errOut.String())
	}
	errOut.Reset()
	if code := runClient([]string{"exec", "laptop"}, &out, &errOut); code != exitUsage {
		t.Fatalf("exec without a command: code=%d, want %d", code, exitUsage)
	}
}
