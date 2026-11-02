package hy2

import (
	"errors"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/apernet/hysteria/core/v2/server"
	"go.uber.org/zap"
)

type fragmentedUDPTestHook struct{}

type countingUDPTestHook struct{ calls int }

func (*fragmentedUDPTestHook) Check(bool, string) bool { return true }
func (*fragmentedUDPTestHook) TCP(server.HyStream, *string) ([]byte, error) {
	return nil, nil
}
func (*fragmentedUDPTestHook) UDP(packets [][]byte, addr *string) (bool, error) {
	if len(packets) < 2 {
		return false, nil
	}
	*addr = "blocked.example:443"
	return true, nil
}

func (*countingUDPTestHook) Check(bool, string) bool { return true }
func (*countingUDPTestHook) TCP(server.HyStream, *string) ([]byte, error) {
	return nil, nil
}
func (h *countingUDPTestHook) UDP([][]byte, *string) (bool, error) {
	h.calls++
	return true, nil
}

func TestUDPPolicyWaitsForFragmentedSniffThenChecksRewrittenDomain(t *testing.T) {
	newPanelRuleOutboundForTest(t, panel.Rules{Regexp: []string{`^blocked\.example$`}})
	hook := newPolicyRequestHook("hy2-test", &fragmentedUDPTestHook{}, &hy2StreamUsers{}, zap.NewNop())
	addr := "1.1.1.1:443"
	if done, err := hook.UDP([][]byte{[]byte("fragment1")}, &addr); done || err != nil {
		t.Fatalf("first fragment must wait: done=%v err=%v", done, err)
	}
	if done, err := hook.UDP([][]byte{[]byte("fragment1"), []byte("fragment2")}, &addr); !done || !errors.Is(err, errPanelRuleRejected) {
		t.Fatalf("rewritten blocked domain bypassed policy: done=%v err=%v", done, err)
	}
}

func TestUDPPolicyChecksLaterHeldDatagram(t *testing.T) {
	newPanelRuleOutboundForTest(t, panel.Rules{Protocol: []string{"bittorrent"}})
	hook := newPolicyRequestHook("hy2-test", nil, &hy2StreamUsers{}, zap.NewNop())
	addr := "allowed.example:443"
	packets := [][]byte{[]byte("ordinary packet"), {0, 0, 4, 23, 39, 16, 25, 128}}
	if _, err := hook.UDP(packets, &addr); !errors.Is(err, errPanelRuleRejected) {
		t.Fatalf("later blocked datagram bypassed policy: %v", err)
	}
}

func TestUDPPolicyBoundsSnifferInputBeforeParsing(t *testing.T) {
	newPanelRuleOutboundForTest(t, panel.Rules{})
	delegate := &countingUDPTestHook{}
	hook := newPolicyRequestHook("hy2-test", delegate, &hy2StreamUsers{}, zap.NewNop())
	addr := "1.1.1.1:443"
	if _, err := hook.UDP([][]byte{make([]byte, maxUDPSniffBytes+1)}, &addr); err == nil {
		t.Fatal("oversized UDP sniff input was accepted")
	}
	if delegate.calls != 0 {
		t.Fatal("oversized data reached the third-party sniffer")
	}
}
