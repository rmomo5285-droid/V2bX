package hy2

import "testing"

func TestQUICConfigUsesConfiguredStreamWindowAndPathMTUSetting(t *testing.T) {
	node := &Hysteria2node{}
	input := &serverConfig{}
	input.QUIC.InitStreamReceiveWindow = 1 << 20
	input.QUIC.InitConnectionReceiveWindow = 4 << 20
	input.QUIC.DisablePathMTUDiscovery = true

	got, err := node.getQUICConfig(input)
	if err != nil {
		t.Fatalf("get QUIC config: %v", err)
	}
	if got.InitialStreamReceiveWindow != input.QUIC.InitStreamReceiveWindow {
		t.Fatalf("initial stream window = %d, want %d", got.InitialStreamReceiveWindow, input.QUIC.InitStreamReceiveWindow)
	}
	if got.InitialConnectionReceiveWindow != input.QUIC.InitConnectionReceiveWindow {
		t.Fatalf("initial connection window = %d, want %d", got.InitialConnectionReceiveWindow, input.QUIC.InitConnectionReceiveWindow)
	}
	if !got.DisablePathMTUDiscovery {
		t.Fatal("disablePathMTUDiscovery was ignored")
	}
}
